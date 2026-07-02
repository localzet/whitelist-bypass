package controlplane

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type SessionKind string

const (
	SessionKindService SessionKind = "service"
	SessionKindWork    SessionKind = "work"
)

type Config struct {
	MaxUsers          int
	MaxServicePerUser int
	MaxWorkPerUser    int
	ServiceTTL        time.Duration
	WorkTTL           time.Duration
}

type CreateSessionInput struct {
	UserID    string
	RequestID string
	Kind      SessionKind
	EgressID  string
	JoinLink  string
	TTL       time.Duration
}

type Session struct {
	ID        string
	UserID    string
	RequestID string
	Kind      SessionKind
	EgressID  string
	JoinLink  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type Manager struct {
	mu           sync.Mutex
	cfg          Config
	now          func() time.Time
	sessions     map[string]Session
	byUserKind   map[string]map[SessionKind]map[string]struct{}
	byRequest    map[string]string
	reservations map[string]int
}

func NewManager(cfg Config) *Manager {
	if cfg.MaxUsers <= 0 {
		cfg.MaxUsers = 128
	}
	if cfg.MaxServicePerUser <= 0 {
		cfg.MaxServicePerUser = 1
	}
	if cfg.MaxWorkPerUser <= 0 {
		cfg.MaxWorkPerUser = 1
	}
	if cfg.ServiceTTL <= 0 {
		cfg.ServiceTTL = 24 * time.Hour
	}
	if cfg.WorkTTL <= 0 {
		cfg.WorkTTL = 30 * time.Minute
	}
	return &Manager{
		cfg:          cfg,
		now:          time.Now,
		sessions:     make(map[string]Session),
		byUserKind:   make(map[string]map[SessionKind]map[string]struct{}),
		byRequest:    make(map[string]string),
		reservations: make(map[string]int),
	}
}

func (m *Manager) AcquireUserSlot(userID string) (func(), error) {
	if userID == "" {
		return nil, errors.New("controlplane: user id is required")
	}
	m.mu.Lock()
	_, active := m.byUserKind[userID]
	_, reserved := m.reservations[userID]
	if !active && !reserved && m.activeUserCountLocked()+m.reservedUserCountLocked() >= m.cfg.MaxUsers {
		m.mu.Unlock()
		return nil, fmt.Errorf("controlplane: max users limit reached")
	}
	m.reservations[userID]++
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.reservations[userID]--
			if m.reservations[userID] <= 0 {
				delete(m.reservations, userID)
			}
		})
	}, nil
}

func (m *Manager) SetClockForTest(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

func (m *Manager) CreateOrReplace(input CreateSessionInput) (Session, []Session, error) {
	if err := validateCreateInput(input); err != nil {
		return Session{}, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := m.cleanupExpiredLocked(m.now())
	if existingID := m.byRequest[requestKey(input.UserID, input.RequestID)]; existingID != "" {
		if session, ok := m.sessions[existingID]; ok {
			return session, removed, nil
		}
		delete(m.byRequest, requestKey(input.UserID, input.RequestID))
	}
	if _, exists := m.byUserKind[input.UserID]; !exists && m.activeUserCountLocked() >= m.cfg.MaxUsers {
		return Session{}, nil, fmt.Errorf("controlplane: max users limit reached")
	}

	limit := m.limitForKind(input.Kind)
	for m.countByKindLocked(input.UserID, input.Kind) >= limit {
		oldest, ok := m.oldestByKindLocked(input.UserID, input.Kind)
		if !ok {
			break
		}
		removed = append(removed, oldest)
		m.deleteLocked(oldest.ID)
	}

	now := m.now()
	ttl := input.TTL
	if ttl <= 0 {
		ttl = m.ttlForKind(input.Kind)
	}
	session := Session{
		ID:        uuid.NewString(),
		UserID:    input.UserID,
		RequestID: input.RequestID,
		Kind:      input.Kind,
		EgressID:  input.EgressID,
		JoinLink:  input.JoinLink,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	m.sessions[session.ID] = session
	if _, ok := m.byUserKind[session.UserID]; !ok {
		m.byUserKind[session.UserID] = make(map[SessionKind]map[string]struct{})
	}
	if _, ok := m.byUserKind[session.UserID][session.Kind]; !ok {
		m.byUserKind[session.UserID][session.Kind] = make(map[string]struct{})
	}
	m.byUserKind[session.UserID][session.Kind][session.ID] = struct{}{}
	m.byRequest[requestKey(session.UserID, session.RequestID)] = session.ID
	return session, removed, nil
}

func (m *Manager) CleanupExpired() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanupExpiredLocked(m.now())
}

func (m *Manager) Get(id string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	return session, ok
}

func (m *Manager) GetByRequest(userID, requestID string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.byRequest[requestKey(userID, requestID)]
	if id == "" {
		return Session{}, false
	}
	session, ok := m.sessions[id]
	return session, ok
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *Manager) cleanupExpiredLocked(now time.Time) []Session {
	var removed []Session
	for id, session := range m.sessions {
		if !session.ExpiresAt.After(now) {
			removed = append(removed, session)
			m.deleteLocked(id)
		}
	}
	return removed
}

func (m *Manager) deleteLocked(id string) {
	session, ok := m.sessions[id]
	if !ok {
		return
	}
	delete(m.sessions, id)
	delete(m.byRequest, requestKey(session.UserID, session.RequestID))
	if byKind, ok := m.byUserKind[session.UserID]; ok {
		if ids, ok := byKind[session.Kind]; ok {
			delete(ids, session.ID)
			if len(ids) == 0 {
				delete(byKind, session.Kind)
			}
		}
		if len(byKind) == 0 {
			delete(m.byUserKind, session.UserID)
		}
	}
}

func (m *Manager) activeUserCountLocked() int {
	return len(m.byUserKind)
}

func (m *Manager) reservedUserCountLocked() int {
	count := 0
	for userID := range m.reservations {
		if _, active := m.byUserKind[userID]; !active {
			count++
		}
	}
	return count
}

func (m *Manager) countByKindLocked(userID string, kind SessionKind) int {
	if byKind, ok := m.byUserKind[userID]; ok {
		return len(byKind[kind])
	}
	return 0
}

func (m *Manager) oldestByKindLocked(userID string, kind SessionKind) (Session, bool) {
	var oldest Session
	found := false
	if byKind, ok := m.byUserKind[userID]; ok {
		for id := range byKind[kind] {
			session, ok := m.sessions[id]
			if !ok {
				continue
			}
			if !found || session.CreatedAt.Before(oldest.CreatedAt) {
				oldest = session
				found = true
			}
		}
	}
	return oldest, found
}

func (m *Manager) limitForKind(kind SessionKind) int {
	if kind == SessionKindService {
		return m.cfg.MaxServicePerUser
	}
	return m.cfg.MaxWorkPerUser
}

func (m *Manager) ttlForKind(kind SessionKind) time.Duration {
	if kind == SessionKindService {
		return m.cfg.ServiceTTL
	}
	return m.cfg.WorkTTL
}

func validateCreateInput(input CreateSessionInput) error {
	switch {
	case input.UserID == "":
		return errors.New("controlplane: user id is required")
	case input.RequestID == "":
		return errors.New("controlplane: request id is required")
	case input.JoinLink == "":
		return errors.New("controlplane: join link is required")
	}
	switch input.Kind {
	case SessionKindService:
	case SessionKindWork:
		if input.EgressID == "" {
			return errors.New("controlplane: work session egress id is required")
		}
	default:
		return fmt.Errorf("controlplane: unsupported session kind %q", input.Kind)
	}
	return nil
}

func requestKey(userID, requestID string) string {
	return userID + "\x00" + requestID
}

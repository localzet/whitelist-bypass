package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"whitelist-bypass/relay/egress"
	"whitelist-bypass/relay/tunnel"
)

type WorkCallRequest struct {
	UserID    string
	RequestID string
	EgressID  string
	Platform  string
	Mode      string
}

type WorkCall struct {
	JoinLink string
	TTL      time.Duration
}

type WorkCallFactory interface {
	CreateWorkCall(ctx context.Context, request WorkCallRequest) (WorkCall, error)
	CloseWorkCall(ctx context.Context, session Session) error
}

type Orchestrator struct {
	manager  *Manager
	egress   *egress.Registry
	factory  WorkCallFactory
	workTTL  time.Duration
	mu       sync.Mutex
	inflight map[string]*inflightSession
}

type inflightSession struct {
	done  chan struct{}
	ready tunnel.SessionReady
	err   error
}

func NewOrchestrator(manager *Manager, registry *egress.Registry, factory WorkCallFactory, workTTL time.Duration) (*Orchestrator, error) {
	if manager == nil {
		return nil, errors.New("controlplane: manager is required")
	}
	if registry == nil {
		registry = egress.DirectRegistry()
	}
	if factory == nil {
		return nil, errors.New("controlplane: work call factory is required")
	}
	if workTTL <= 0 {
		workTTL = 30 * time.Minute
	}
	return &Orchestrator{
		manager:  manager,
		egress:   registry,
		factory:  factory,
		workTTL:  workTTL,
		inflight: make(map[string]*inflightSession),
	}, nil
}

func (o *Orchestrator) HandleSessionCreate(ctx context.Context, userID string, request tunnel.SessionCreateRequest) (tunnel.SessionReady, error) {
	if userID == "" {
		userID = request.UserID
	}
	if userID == "" {
		return tunnel.SessionReady{}, errors.New("controlplane: user id is required")
	}
	if request.RequestID == "" {
		return tunnel.SessionReady{}, errors.New("controlplane: request id is required")
	}
	if existing, ok := o.manager.GetByRequest(userID, request.RequestID); ok {
		return sessionReady(existing), nil
	}
	key := requestKey(userID, request.RequestID)
	inflight, leader := o.beginInflight(key)
	if !leader {
		select {
		case <-ctx.Done():
			return tunnel.SessionReady{}, ctx.Err()
		case <-inflight.done:
			return inflight.ready, inflight.err
		}
	}
	defer func() {
		o.finishInflight(key, inflight)
	}()
	releaseSlot, err := o.manager.AcquireUserSlot(userID)
	if err != nil {
		inflight.err = err
		return tunnel.SessionReady{}, err
	}
	defer releaseSlot()

	_, resolvedEgressID, err := o.egress.Select(request.EgressID)
	if err != nil {
		inflight.err = fmt.Errorf("controlplane: select egress: %w", err)
		return tunnel.SessionReady{}, inflight.err
	}
	call, err := o.factory.CreateWorkCall(ctx, WorkCallRequest{
		UserID:    userID,
		RequestID: request.RequestID,
		EgressID:  resolvedEgressID,
		Platform:  request.Platform,
		Mode:      request.Mode,
	})
	if err != nil {
		inflight.err = err
		return tunnel.SessionReady{}, err
	}
	ttl := call.TTL
	if ttl <= 0 {
		ttl = o.workTTL
	}
	session, removed, err := o.manager.CreateOrReplace(CreateSessionInput{
		UserID:    userID,
		RequestID: request.RequestID,
		Kind:      SessionKindWork,
		EgressID:  resolvedEgressID,
		JoinLink:  call.JoinLink,
		TTL:       ttl,
	})
	if err != nil {
		_ = o.factory.CloseWorkCall(ctx, Session{JoinLink: call.JoinLink, EgressID: resolvedEgressID, Kind: SessionKindWork})
		inflight.err = err
		return tunnel.SessionReady{}, err
	}
	for _, old := range removed {
		if closeErr := o.factory.CloseWorkCall(ctx, old); closeErr != nil {
			inflight.err = fmt.Errorf("controlplane: close replaced session: %w", closeErr)
			return tunnel.SessionReady{}, inflight.err
		}
	}
	inflight.ready = sessionReady(session)
	return inflight.ready, nil
}

func sessionReady(session Session) tunnel.SessionReady {
	ttlSeconds := int64(session.ExpiresAt.Sub(session.CreatedAt).Seconds())
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	return tunnel.SessionReady{
		RequestID:  session.RequestID,
		SessionID:  session.ID,
		JoinLink:   session.JoinLink,
		EgressID:   session.EgressID,
		TTLSeconds: ttlSeconds,
	}
}

func (o *Orchestrator) CleanupExpired(ctx context.Context) error {
	var errs []error
	for _, session := range o.manager.CleanupExpired() {
		if err := o.factory.CloseWorkCall(ctx, session); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (o *Orchestrator) beginInflight(key string) (*inflightSession, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if existing := o.inflight[key]; existing != nil {
		return existing, false
	}
	inflight := &inflightSession{done: make(chan struct{})}
	o.inflight[key] = inflight
	return inflight, true
}

func (o *Orchestrator) finishInflight(key string, inflight *inflightSession) {
	o.mu.Lock()
	delete(o.inflight, key)
	o.mu.Unlock()
	close(inflight.done)
}

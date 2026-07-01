package egress

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"whitelist-bypass/relay/common"
)

const (
	TypeDirect = "direct"
	TypeSOCKS5 = "socks5"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type UDPSession interface {
	WriteTo(data []byte, dst string) error
	Read(buf []byte) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

type Dialer interface {
	ID() string
	DialTCP(dst string, timeout time.Duration) (net.Conn, error)
	UDPAssociate(dst string, timeout time.Duration) (UDPSession, error)
}

type Config struct {
	SchemaVersion int       `json:"schemaVersion"`
	DefaultEgress string    `json:"defaultEgress"`
	Egresses      []Profile `json:"egresses"`
}

type Profile struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Address     string `json:"address,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	PasswordEnv string `json:"passwordEnv,omitempty"`
	PasswordFile string `json:"passwordFile,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type Registry struct {
	defaultID string
	dialers   map[string]Dialer
}

func NewRegistry(defaultID string, dialers ...Dialer) (*Registry, error) {
	out := &Registry{defaultID: strings.TrimSpace(defaultID), dialers: make(map[string]Dialer)}
	for _, dialer := range dialers {
		if dialer == nil {
			continue
		}
		id := dialer.ID()
		if err := validateID(id); err != nil {
			return nil, err
		}
		if _, exists := out.dialers[id]; exists {
			return nil, fmt.Errorf("egress: duplicate id %q", id)
		}
		out.dialers[id] = dialer
	}
	if out.defaultID == "" {
		out.defaultID = TypeDirect
	}
	if _, ok := out.dialers[out.defaultID]; !ok {
		return nil, fmt.Errorf("egress: default %q is not defined", out.defaultID)
	}
	return out, nil
}

func DirectRegistry() *Registry {
	reg, _ := NewRegistry(TypeDirect, DirectDialer{ProfileID: TypeDirect})
	return reg
}

func LegacySOCKSRegistry(addr, user, pass string) (*Registry, error) {
	if strings.TrimSpace(addr) == "" {
		return DirectRegistry(), nil
	}
	return NewRegistry("legacy-default", SOCKS5Dialer{
		ProfileID: "legacy-default",
		Upstream:  common.NewSocks5Upstream(addr, user, pass),
	})
}

func LoadConfig(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("egress: parse config: %w", err)
	}
	return RegistryFromConfig(cfg)
}

func RegistryFromConfig(cfg Config) (*Registry, error) {
	if cfg.SchemaVersion != 1 {
		return nil, fmt.Errorf("egress: unsupported schemaVersion %d", cfg.SchemaVersion)
	}
	var dialers []Dialer
	for _, profile := range cfg.Egresses {
		if !profile.Enabled {
			continue
		}
		dialer, err := dialerFromProfile(profile)
		if err != nil {
			return nil, err
		}
		dialers = append(dialers, dialer)
	}
	if len(dialers) == 0 {
		return nil, errors.New("egress: no enabled profiles")
	}
	return NewRegistry(cfg.DefaultEgress, dialers...)
}

func (r *Registry) Select(requestedID string) (Dialer, string, error) {
	if r == nil {
		r = DirectRegistry()
	}
	id := strings.TrimSpace(requestedID)
	if id == "" {
		id = r.defaultID
	}
	if err := validateID(id); err != nil {
		return nil, "", err
	}
	dialer, ok := r.dialers[id]
	if !ok {
		return nil, "", fmt.Errorf("egress: profile %q is not available", id)
	}
	return dialer, id, nil
}

func dialerFromProfile(profile Profile) (Dialer, error) {
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Type = strings.ToLower(strings.TrimSpace(profile.Type))
	if err := validateID(profile.ID); err != nil {
		return nil, err
	}
	switch profile.Type {
	case TypeDirect:
		return DirectDialer{ProfileID: profile.ID}, nil
	case TypeSOCKS5:
		addr := strings.TrimSpace(profile.Address)
		if addr == "" {
			return nil, fmt.Errorf("egress: socks5 profile %q has empty address", profile.ID)
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf("egress: socks5 profile %q address: %w", profile.ID, err)
		}
		pass, err := resolvePassword(profile)
		if err != nil {
			return nil, err
		}
		return SOCKS5Dialer{
			ProfileID: profile.ID,
			Upstream:  common.NewSocks5Upstream(addr, profile.Username, pass),
		}, nil
	default:
		return nil, fmt.Errorf("egress: profile %q has unsupported type %q", profile.ID, profile.Type)
	}
}

func resolvePassword(profile Profile) (string, error) {
	sources := 0
	if profile.Password != "" {
		sources++
	}
	if profile.PasswordEnv != "" {
		sources++
	}
	if profile.PasswordFile != "" {
		sources++
	}
	if sources > 1 {
		return "", fmt.Errorf("egress: profile %q defines multiple password sources", profile.ID)
	}
	switch {
	case profile.Password != "":
		return profile.Password, nil
	case profile.PasswordEnv != "":
		return os.Getenv(profile.PasswordEnv), nil
	case profile.PasswordFile != "":
		raw, err := os.ReadFile(profile.PasswordFile)
		if err != nil {
			return "", fmt.Errorf("egress: read password file for %q: %w", profile.ID, err)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	default:
		return "", nil
	}
}

func validateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("egress: invalid id %q", id)
	}
	return nil
}

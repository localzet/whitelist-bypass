package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"whitelist-bypass/relay/controlplane"
	"whitelist-bypass/relay/egress"
	"whitelist-bypass/relay/tunnel"
)

type serviceControlOptions struct {
	UserIDs        string
	VaultDir       string
	VaultKey       string
	BinsDir        string
	SessionsDir    string
	EgressConfig   string
	Resources      string
	WorkPlatform   string
	MaxActiveUsers int
	WorkTTL        time.Duration
}

func configureServiceControl(opts serviceControlOptions) (func(*tunnel.RelayBridge) error, error) {
	allowedUsers := parseServiceUserIDs(opts.UserIDs)
	if len(allowedUsers) == 0 {
		return nil, fmt.Errorf("--service-user-ids is required")
	}
	if opts.VaultDir == "" || opts.VaultKey == "" || opts.BinsDir == "" {
		return nil, fmt.Errorf("--vault-dir, --vault-key-base64 and --bins-dir are required")
	}
	if opts.MaxActiveUsers <= 0 || opts.WorkTTL <= 0 {
		return nil, fmt.Errorf("--max-active-users and --work-ttl must be positive")
	}

	key, err := controlplane.CookieVaultKeyFromBase64(opts.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("vault key: %w", err)
	}
	vault, err := controlplane.NewCookieVault(opts.VaultDir, key)
	if err != nil {
		return nil, fmt.Errorf("cookie vault: %w", err)
	}
	registry := egress.DirectRegistry()
	if opts.EgressConfig != "" {
		registry, err = egress.LoadConfig(opts.EgressConfig)
		if err != nil {
			return nil, fmt.Errorf("egress config: %w", err)
		}
	}
	factory, err := controlplane.NewProcessWorkCallFactory(controlplane.ProcessFactoryConfig{
		BinsDir:         opts.BinsDir,
		SessionsDir:     opts.SessionsDir,
		Resources:       opts.Resources,
		EgressConfig:    opts.EgressConfig,
		Cookies:         vault,
		DefaultPlatform: opts.WorkPlatform,
	})
	if err != nil {
		return nil, fmt.Errorf("work factory: %w", err)
	}
	manager := controlplane.NewManager(controlplane.Config{MaxUsers: opts.MaxActiveUsers, WorkTTL: opts.WorkTTL})
	orchestrator, err := controlplane.NewOrchestrator(manager, registry, factory, opts.WorkTTL)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: %w", err)
	}
	go cleanupExpiredWorkCalls(orchestrator)

	handler := controlplane.ServiceHandler{
		AllowedUserIDs: allowedUsers,
		CookieVault:    vault,
		Sessions:       orchestrator,
	}
	log.Printf("[service] transport=telemost allowed-users=%d max-active-users=%d work-ttl=%s", len(allowedUsers), opts.MaxActiveUsers, opts.WorkTTL)
	return func(bridge *tunnel.RelayBridge) error {
		return handler.BindBridge(context.Background(), bridge)
	}, nil
}

func parseServiceUserIDs(csv string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, value := range strings.Split(csv, ",") {
		if userID := strings.TrimSpace(value); userID != "" {
			allowed[userID] = struct{}{}
		}
	}
	return allowed
}

func cleanupExpiredWorkCalls(orchestrator *controlplane.Orchestrator) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if err := orchestrator.CleanupExpired(context.Background()); err != nil {
			log.Printf("[service] cleanup expired work calls: %v", err)
		}
	}
}

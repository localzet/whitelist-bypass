package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"whitelist-bypass/relay/tunnel"
)

const serviceSessionReadyMarker = "SERVICE_SESSION_READY:"
const serviceControlErrorMarker = "STATUS:ERROR:"

const (
	serviceSessionRetryInterval = 2 * time.Second
	serviceCookieRetryInterval  = 10 * time.Second
)

type serviceControlConfig struct {
	Enabled        bool
	UserID         string
	RequestID      string
	EgressID       string
	CookieFile     string
	CookiePlatform string
	WorkPlatform   string
	TunnelMode     string
}

type serviceControlRuntime struct {
	ready       chan struct{}
	readyOnce   sync.Once
	loopRunning atomic.Bool
	cookieAcked atomic.Bool
}

func newServiceControlRuntime() *serviceControlRuntime {
	return &serviceControlRuntime{ready: make(chan struct{})}
}

func (rt *serviceControlRuntime) markReady() {
	if rt == nil {
		return
	}
	rt.readyOnce.Do(func() { close(rt.ready) })
}

func (rt *serviceControlRuntime) markCookieAcked() {
	if rt != nil {
		rt.cookieAcked.Store(true)
	}
}

func (rt *serviceControlRuntime) startRequestLoop(ctx context.Context, bridge *tunnel.RelayBridge, cfg serviceControlConfig) {
	if rt == nil {
		requestServiceSession(bridge, cfg)
		return
	}
	if !rt.loopRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer rt.loopRunning.Store(false)
		sessionTicker := time.NewTicker(serviceSessionRetryInterval)
		defer sessionTicker.Stop()
		cookieTicker := time.NewTicker(serviceCookieRetryInterval)
		defer cookieTicker.Stop()

		requestTimer := time.NewTimer(serviceSessionRetryInterval)
		defer requestTimer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-rt.ready:
				return
			case <-requestTimer.C:
				requestServiceSession(bridge, cfg)
			case <-sessionTicker.C:
				requestServiceSessionWithoutCookies(bridge, cfg)
			case <-cookieTicker.C:
				if cfg.CookieFile != "" && !rt.cookieAcked.Load() {
					requestServiceSession(bridge, cfg)
				}
			}
		}
	}()
}

func (cfg *serviceControlConfig) normalize() error {
	if !cfg.Enabled {
		return nil
	}
	cfg.UserID = strings.TrimSpace(cfg.UserID)
	cfg.RequestID = strings.TrimSpace(cfg.RequestID)
	cfg.CookieFile = strings.TrimSpace(cfg.CookieFile)
	cfg.CookiePlatform = strings.TrimSpace(cfg.CookiePlatform)
	cfg.WorkPlatform = strings.TrimSpace(cfg.WorkPlatform)
	cfg.TunnelMode = strings.TrimSpace(cfg.TunnelMode)
	if cfg.UserID == "" {
		return fmt.Errorf("--service-user-id is required with --service-control")
	}
	if cfg.WorkPlatform == "" {
		return fmt.Errorf("--service-work-platform is required with --service-control")
	}
	if cfg.RequestID == "" {
		cfg.RequestID = newServiceRequestID()
	}
	if cfg.CookieFile != "" && cfg.CookiePlatform == "" {
		return fmt.Errorf("--service-cookie-platform is required with --service-cookie-file")
	}
	return nil
}

func configureServiceBridge(bridge *tunnel.RelayBridge, cfg serviceControlConfig, rt *serviceControlRuntime, emit func(string)) {
	bridge.SetOnCookieAck(func(ack tunnel.CookieAck) {
		log.Printf("[service] cookie stored platform=%q request=%q", ack.Platform, ack.RequestID)
		rt.markCookieAcked()
	})
	bridge.SetOnControlError(func(controlErr tunnel.ControlError) {
		log.Printf("[service] control error %s: %s", controlErr.Code, controlErr.SafeMessage)
		if isTerminalServiceError(controlErr) {
			rt.markReady()
			emit(serviceControlErrorMarker + controlErr.SafeMessage)
		}
	})
	bridge.SetOnSessionReady(func(session tunnel.SessionReady) {
		payload, err := json.Marshal(session)
		if err != nil {
			log.Printf("[service] encode session ready: %v", err)
			return
		}
		rt.markReady()
		emit(serviceSessionReadyMarker + string(payload))
	})
}

func isTerminalServiceError(controlErr tunnel.ControlError) bool {
	switch controlErr.Code {
	case "session_create_failed", "cookie_submit_failed", "bad_session_request", "bad_cookie_submit", "session_control_unavailable", "cookie_vault_unavailable":
		return true
	default:
		return false
	}
}

func requestServiceSession(bridge *tunnel.RelayBridge, cfg serviceControlConfig) {
	if cfg.CookieFile != "" {
		payload, err := os.ReadFile(cfg.CookieFile)
		if err != nil {
			log.Printf("[service] read cookies: %v", err)
		} else {
			bridge.SubmitCookies(tunnel.CookieSubmit{
				RequestID: cfg.RequestID + "-cookies",
				UserID:    cfg.UserID,
				Platform:  cfg.CookiePlatform,
				Format:    "json",
				Payload:   string(payload),
			})
			log.Printf("[service] submitted cookies platform=%q request=%q", cfg.CookiePlatform, cfg.RequestID+"-cookies")
		}
	}
	bridge.RequestSession(tunnel.SessionCreateRequest{
		RequestID: cfg.RequestID,
		UserID:    cfg.UserID,
		EgressID:  cfg.EgressID,
		Platform:  cfg.WorkPlatform,
		Mode:      cfg.TunnelMode,
	})
	log.Printf("[service] requested work session platform=%q egress=%q request=%q", cfg.WorkPlatform, cfg.EgressID, cfg.RequestID)
}

func requestServiceSessionWithoutCookies(bridge *tunnel.RelayBridge, cfg serviceControlConfig) {
	bridge.RequestSession(tunnel.SessionCreateRequest{
		RequestID: cfg.RequestID,
		UserID:    cfg.UserID,
		EgressID:  cfg.EgressID,
		Platform:  cfg.WorkPlatform,
		Mode:      cfg.TunnelMode,
	})
	log.Printf("[service] retried work session request platform=%q egress=%q request=%q", cfg.WorkPlatform, cfg.EgressID, cfg.RequestID)
}

func newServiceRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

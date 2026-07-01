package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"whitelist-bypass/relay/tunnel"
)

const serviceSessionReadyMarker = "SERVICE_SESSION_READY:"

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

func configureServiceBridge(bridge *tunnel.RelayBridge, cfg serviceControlConfig, emit func(string)) {
	bridge.SetOnCookieAck(func(ack tunnel.CookieAck) {
		log.Printf("[service] cookie stored platform=%q request=%q", ack.Platform, ack.RequestID)
	})
	bridge.SetOnSessionReady(func(session tunnel.SessionReady) {
		payload, err := json.Marshal(session)
		if err != nil {
			log.Printf("[service] encode session ready: %v", err)
			return
		}
		emit(serviceSessionReadyMarker + string(payload))
	})
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

func newServiceRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

package controlplane

import (
	"context"
	"errors"

	"whitelist-bypass/relay/tunnel"
)

type SessionController interface {
	HandleSessionCreate(ctx context.Context, userID string, request tunnel.SessionCreateRequest) (tunnel.SessionReady, error)
}

type ServiceHandler struct {
	UserID      string
	CookieVault *CookieVault
	Sessions    SessionController
}

func (h ServiceHandler) BindBridge(ctx context.Context, bridge *tunnel.RelayBridge) error {
	if bridge == nil {
		return errors.New("controlplane: relay bridge is required")
	}
	if h.UserID == "" {
		return errors.New("controlplane: user id is required")
	}
	if h.CookieVault != nil {
		bridge.SetOnCookieSubmit(func(submit tunnel.CookieSubmit) (tunnel.CookieAck, error) {
			return h.CookieVault.StoreSubmit(h.UserID, submit)
		})
	}
	if h.Sessions != nil {
		bridge.SetOnSessionCreate(func(request tunnel.SessionCreateRequest) (tunnel.SessionReady, error) {
			return h.Sessions.HandleSessionCreate(ctx, h.UserID, request)
		})
	}
	return nil
}

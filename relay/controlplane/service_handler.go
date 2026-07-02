package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"whitelist-bypass/relay/tunnel"
)

type SessionController interface {
	HandleSessionCreate(ctx context.Context, userID string, request tunnel.SessionCreateRequest) (tunnel.SessionReady, error)
}

type ServiceHandler struct {
	UserID         string
	AllowedUserIDs map[string]struct{}
	CookieVault    *CookieVault
	Sessions       SessionController
}

func (h ServiceHandler) BindBridge(ctx context.Context, bridge *tunnel.RelayBridge) error {
	if bridge == nil {
		return errors.New("controlplane: relay bridge is required")
	}
	if h.UserID == "" && len(h.AllowedUserIDs) == 0 {
		return errors.New("controlplane: at least one user id is required")
	}
	if h.CookieVault != nil {
		bridge.SetOnCookieSubmit(func(submit tunnel.CookieSubmit) (tunnel.CookieAck, error) {
			userID, err := h.authorizeUser(submit.UserID)
			if err != nil {
				return tunnel.CookieAck{}, err
			}
			return h.CookieVault.StoreSubmit(userID, submit)
		})
	}
	if h.Sessions != nil {
		bridge.SetOnSessionCreate(func(request tunnel.SessionCreateRequest) (tunnel.SessionReady, error) {
			userID, err := h.authorizeUser(request.UserID)
			if err != nil {
				return tunnel.SessionReady{}, err
			}
			return h.Sessions.HandleSessionCreate(ctx, userID, request)
		})
	}
	bridge.SetOnEgressListRequest(func(request tunnel.EgressListRequest) ([]tunnel.EgressDescriptor, error) {
		if _, err := h.authorizeUser(request.UserID); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return nil
}

func (h ServiceHandler) authorizeUser(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if len(h.AllowedUserIDs) > 0 {
		if _, ok := h.AllowedUserIDs[requested]; !ok {
			return "", fmt.Errorf("controlplane: user is not authorized")
		}
		return requested, nil
	}
	if requested != "" && requested != h.UserID {
		return "", fmt.Errorf("controlplane: user is not authorized")
	}
	return h.UserID, nil
}

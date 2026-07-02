package controlplane

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"whitelist-bypass/relay/tunnel"
)

type fakeDataTunnel struct {
	mu     sync.Mutex
	onData func([]byte)
	sent   [][]byte
}

func (f *fakeDataTunnel) SendData(data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, cp)
}

func (f *fakeDataTunnel) SetOnData(fn func([]byte)) {
	f.onData = fn
}

func (f *fakeDataTunnel) SetOnClose(func()) {}

func (f *fakeDataTunnel) Reconfigure(_, _ int) {}

func (f *fakeDataTunnel) emit(data []byte) {
	f.onData(data)
}

func (f *fakeDataTunnel) waitSent(t *testing.T) []byte {
	return f.waitSentAfter(t, 0)
}

func (f *fakeDataTunnel) waitSentAfter(t *testing.T, previous int) []byte {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		f.mu.Lock()
		if len(f.sent) > previous {
			frame := f.sent[len(f.sent)-1]
			f.mu.Unlock()
			return frame
		}
		f.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("expected sent frame")
		case <-ticker.C:
		}
	}
}

func TestServiceHandlerStoresCookiesThroughBridge(t *testing.T) {
	vault, err := NewCookieVault(t.TempDir(), bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	fakeTunnel := &fakeDataTunnel{}
	bridge := tunnel.NewRelayBridge(fakeTunnel, "creator", 1024, t.Logf)
	handler := ServiceHandler{UserID: "user-1", CookieVault: vault}
	if err := handler.BindBridge(context.Background(), bridge); err != nil {
		t.Fatal(err)
	}

	fakeTunnel.emit(tunnel.EncodeFrame(tunnel.ControlConnID, tunnel.MsgCookieSubmit, tunnel.EncodeCookieSubmitPayload(tunnel.CookieSubmit{
		RequestID: "cookie-1",
		Platform:  "tm",
		Format:    CookieFormatJSON,
		Payload:   `[{"name":"Session_id","value":"secret"}]`,
	})))
	sent := fakeTunnel.waitSent(t)
	var ack tunnel.CookieAck
	tunnel.DecodeFrames(sent, func(_ uint32, msgType byte, payload []byte) {
		if msgType != tunnel.MsgCookieAck {
			t.Fatalf("msgType = %d, want CookieAck", msgType)
		}
		var ok bool
		ack, ok = tunnel.DecodeCookieAck(payload)
		if !ok {
			t.Fatal("DecodeCookieAck() failed")
		}
	})
	if !ack.Stored || ack.Platform != PlatformTelemost {
		t.Fatalf("ack = %+v", ack)
	}
	if _, err := vault.Load("user-1", PlatformTelemost); err != nil {
		t.Fatal(err)
	}
}

func TestServiceHandlerCreatesSessionThroughBridge(t *testing.T) {
	manager := NewManager(Config{})
	factory := &fakeWorkCallFactory{next: WorkCall{JoinLink: "call-work", TTL: time.Minute}}
	orchestrator, err := NewOrchestrator(manager, nil, factory, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fakeTunnel := &fakeDataTunnel{}
	bridge := tunnel.NewRelayBridge(fakeTunnel, "creator", 1024, t.Logf)
	handler := ServiceHandler{UserID: "user-1", Sessions: orchestrator}
	if err := handler.BindBridge(context.Background(), bridge); err != nil {
		t.Fatal(err)
	}

	fakeTunnel.emit(tunnel.EncodeFrame(tunnel.ControlConnID, tunnel.MsgSessionCreate, tunnel.EncodeSessionCreatePayload(tunnel.SessionCreateRequest{
		RequestID: "req-1",
		EgressID:  "direct",
	})))
	sent := fakeTunnel.waitSent(t)
	var ready tunnel.SessionReady
	tunnel.DecodeFrames(sent, func(_ uint32, msgType byte, payload []byte) {
		if msgType != tunnel.MsgSessionReady {
			t.Fatalf("msgType = %d, want SessionReady", msgType)
		}
		var ok bool
		ready, ok = tunnel.DecodeSessionReady(payload)
		if !ok {
			t.Fatal("DecodeSessionReady() failed")
		}
	})
	if ready.JoinLink != "call-work" || ready.EgressID != "direct" {
		t.Fatalf("ready = %+v", ready)
	}
}

func TestServiceHandlerRejectsInvalidBinding(t *testing.T) {
	if err := (ServiceHandler{UserID: "user-1"}).BindBridge(context.Background(), nil); err == nil {
		t.Fatal("BindBridge() expected nil bridge error")
	}
	fakeTunnel := &fakeDataTunnel{}
	bridge := tunnel.NewRelayBridge(fakeTunnel, "creator", 1024, t.Logf)
	if err := (ServiceHandler{}).BindBridge(context.Background(), bridge); err == nil {
		t.Fatal("BindBridge() expected missing user id error")
	}
}

func TestServiceHandlerAuthorizesListedUsers(t *testing.T) {
	handler := ServiceHandler{AllowedUserIDs: map[string]struct{}{"user-1": {}, "user-2": {}}}
	for _, userID := range []string{"user-1", "user-2"} {
		got, err := handler.authorizeUser(userID)
		if err != nil || got != userID {
			t.Fatalf("authorizeUser(%q) = %q, %v", userID, got, err)
		}
	}
	if _, err := handler.authorizeUser("user-3"); err == nil {
		t.Fatal("authorizeUser() expected unauthorized error")
	}
	if _, err := handler.authorizeUser(""); err == nil {
		t.Fatal("authorizeUser() expected empty user error")
	}
}

func TestServiceHandlerStoresListedUsersSeparately(t *testing.T) {
	vault, err := NewCookieVault(t.TempDir(), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	fakeTunnel := &fakeDataTunnel{}
	bridge := tunnel.NewRelayBridge(fakeTunnel, "creator", 1024, t.Logf)
	handler := ServiceHandler{
		AllowedUserIDs: map[string]struct{}{"user-1": {}, "user-2": {}},
		CookieVault:    vault,
	}
	if err := handler.BindBridge(context.Background(), bridge); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"user-1", "user-2"} {
		fakeTunnel.mu.Lock()
		previousSent := len(fakeTunnel.sent)
		fakeTunnel.mu.Unlock()
		fakeTunnel.emit(tunnel.EncodeFrame(tunnel.ControlConnID, tunnel.MsgCookieSubmit, tunnel.EncodeCookieSubmitPayload(tunnel.CookieSubmit{
			RequestID: "cookie-" + userID,
			UserID:    userID,
			Platform:  PlatformTelemost,
			Format:    CookieFormatJSON,
			Payload:   `[{"name":"Session_id","value":"` + userID + `"}]`,
		})))
		fakeTunnel.waitSentAfter(t, previousSent)
		stored, err := vault.Load(userID, PlatformTelemost)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stored.Payload, userID) {
			t.Fatalf("vault payload for %q does not contain its marker", userID)
		}
	}
}

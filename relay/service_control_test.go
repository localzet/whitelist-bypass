package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"whitelist-bypass/relay/tunnel"
)

type serviceFakeTunnel struct {
	onData func([]byte)
	sent   [][]byte
}

func (f *serviceFakeTunnel) SendData(data []byte) {
	f.sent = append(f.sent, append([]byte(nil), data...))
}

func (f *serviceFakeTunnel) SetOnData(fn func([]byte)) { f.onData = fn }
func (f *serviceFakeTunnel) SetOnClose(func())         {}
func (f *serviceFakeTunnel) Reconfigure(_, _ int)      {}

func TestServiceControlConfigNormalize(t *testing.T) {
	cfg := serviceControlConfig{Enabled: true, UserID: " user-1 ", WorkPlatform: " telemost "}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.UserID != "user-1" || cfg.WorkPlatform != "telemost" || cfg.RequestID == "" {
		t.Fatalf("unexpected normalized config: %+v", cfg)
	}
}

func TestRequestServiceSessionSendsCookiesAndRequest(t *testing.T) {
	cookieFile := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(cookieFile, []byte(`[{"name":"Session_id","value":"secret"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &serviceFakeTunnel{}
	bridge := tunnel.NewRelayBridge(fake, "joiner", 1024, t.Logf)
	requestServiceSession(bridge, serviceControlConfig{
		UserID: "user-1", RequestID: "request-1", EgressID: "fast",
		CookieFile: cookieFile, CookiePlatform: "telemost", WorkPlatform: "telemost", TunnelMode: "video",
	})
	if len(fake.sent) != 2 {
		t.Fatalf("got %d frames, want 2", len(fake.sent))
	}

	var messageType byte
	var payload []byte
	tunnel.DecodeFrames(fake.sent[0], func(_ uint32, gotType byte, gotPayload []byte) {
		messageType, payload = gotType, gotPayload
	})
	if messageType != tunnel.MsgCookieSubmit {
		t.Fatalf("first frame type=%x", messageType)
	}
	cookie, ok := tunnel.DecodeCookieSubmit(payload)
	if !ok || cookie.UserID != "user-1" || cookie.Payload == "" {
		t.Fatalf("unexpected cookie submit: %+v", cookie)
	}
	tunnel.DecodeFrames(fake.sent[1], func(_ uint32, gotType byte, gotPayload []byte) {
		messageType, payload = gotType, gotPayload
	})
	if messageType != tunnel.MsgSessionCreate {
		t.Fatalf("second frame type=%x", messageType)
	}
	request, ok := tunnel.DecodeSessionCreate(payload)
	if !ok || request.RequestID != "request-1" || request.EgressID != "fast" {
		t.Fatalf("unexpected session request: %+v", request)
	}
}

func TestConfigureServiceBridgeEmitsReadyMarker(t *testing.T) {
	fake := &serviceFakeTunnel{}
	bridge := tunnel.NewRelayBridge(fake, "joiner", 1024, t.Logf)
	var emitted string
	want := tunnel.SessionReady{
		RequestID: "request-1", SessionID: "session-1", JoinLink: "https://example.test/call",
		EgressID: "direct", TTLSeconds: 300,
	}
	runtime := newServiceControlRuntime()
	configureServiceBridge(bridge, serviceControlConfig{}, runtime, func(line string) { emitted = line })
	fake.onData(tunnel.EncodeFrame(tunnel.ControlConnID, tunnel.MsgSessionReady, tunnel.EncodeSessionReadyPayload(want)))
	if len(emitted) <= len(serviceSessionReadyMarker) || emitted[:len(serviceSessionReadyMarker)] != serviceSessionReadyMarker {
		t.Fatalf("unexpected marker: %q", emitted)
	}
	var got tunnel.SessionReady
	if err := json.Unmarshal([]byte(emitted[len(serviceSessionReadyMarker):]), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	select {
	case <-runtime.ready:
	default:
		t.Fatal("runtime was not marked ready")
	}
}

func TestConfigureServiceBridgeStopsOnTerminalError(t *testing.T) {
	fake := &serviceFakeTunnel{}
	bridge := tunnel.NewRelayBridge(fake, "joiner", 1024, t.Logf)
	runtime := newServiceControlRuntime()
	var emitted string
	configureServiceBridge(bridge, serviceControlConfig{}, runtime, func(line string) { emitted = line })

	fake.onData(tunnel.EncodeFrame(
		tunnel.ControlConnID,
		tunnel.MsgControlErr,
		tunnel.EncodeControlErrorPayload("session_create_failed", "work call failed"),
	))

	if emitted != serviceControlErrorMarker+"work call failed" {
		t.Fatalf("emitted = %q", emitted)
	}
	select {
	case <-runtime.ready:
	default:
		t.Fatal("runtime was not stopped")
	}
}

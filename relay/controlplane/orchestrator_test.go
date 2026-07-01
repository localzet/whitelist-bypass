package controlplane

import (
	"context"
	"sync"
	"testing"
	"time"

	"whitelist-bypass/relay/egress"
	"whitelist-bypass/relay/tunnel"
)

type fakeWorkCallFactory struct {
	mu     sync.Mutex
	calls  []WorkCallRequest
	closed []Session
	next   WorkCall
	block  chan struct{}
}

func (f *fakeWorkCallFactory) CreateWorkCall(_ context.Context, request WorkCallRequest) (WorkCall, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, request)
	if f.next.JoinLink == "" {
		return WorkCall{JoinLink: "call-" + request.EgressID}, nil
	}
	return f.next, nil
}

func (f *fakeWorkCallFactory) CloseWorkCall(_ context.Context, session Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, session)
	return nil
}

func (f *fakeWorkCallFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestOrchestratorCreatesWorkSessionWithDefaultEgress(t *testing.T) {
	manager := NewManager(Config{})
	registry, err := egress.NewRegistry("direct", egress.DirectDialer{ProfileID: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	factory := &fakeWorkCallFactory{next: WorkCall{JoinLink: "call-default", TTL: 5 * time.Minute}}
	orchestrator, err := NewOrchestrator(manager, registry, factory, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	ready, err := orchestrator.HandleSessionCreate(context.Background(), "user-1", tunnel.SessionCreateRequest{
		RequestID: "req-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready.EgressID != "direct" || ready.JoinLink != "call-default" || ready.TTLSeconds != 300 {
		t.Fatalf("ready = %+v", ready)
	}
	if factory.callCount() != 1 || factory.calls[0].EgressID != "direct" {
		t.Fatalf("factory calls = %+v", factory.calls)
	}
}

func TestOrchestratorIsIdempotentBeforeCreatingCall(t *testing.T) {
	manager := NewManager(Config{})
	factory := &fakeWorkCallFactory{}
	orchestrator, err := NewOrchestrator(manager, nil, factory, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := tunnel.SessionCreateRequest{RequestID: "req-1", EgressID: "direct"}

	first, err := orchestrator.HandleSessionCreate(context.Background(), "user-1", request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := orchestrator.HandleSessionCreate(context.Background(), "user-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID != second.SessionID || first.JoinLink != second.JoinLink {
		t.Fatalf("idempotent response mismatch: first=%+v second=%+v", first, second)
	}
	if factory.callCount() != 1 {
		t.Fatalf("factory calls = %d, want 1", factory.callCount())
	}
}

func TestOrchestratorCoalescesConcurrentRequests(t *testing.T) {
	manager := NewManager(Config{})
	block := make(chan struct{})
	factory := &fakeWorkCallFactory{block: block}
	orchestrator, err := NewOrchestrator(manager, nil, factory, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := tunnel.SessionCreateRequest{RequestID: "req-1", EgressID: "direct"}

	type result struct {
		ready tunnel.SessionReady
		err   error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ready, err := orchestrator.HandleSessionCreate(context.Background(), "user-1", request)
			results <- result{ready: ready, err: err}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(block)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("errors: first=%v second=%v", first.err, second.err)
	}
	if first.ready.SessionID != second.ready.SessionID {
		t.Fatalf("session ids differ: first=%+v second=%+v", first.ready, second.ready)
	}
	if factory.callCount() != 1 {
		t.Fatalf("factory calls = %d, want 1", factory.callCount())
	}
}

func TestOrchestratorReplacesPreviousWorkSession(t *testing.T) {
	manager := NewManager(Config{})
	registry, err := egress.NewRegistry(
		"direct",
		egress.DirectDialer{ProfileID: "direct"},
		egress.DirectDialer{ProfileID: "de-fra-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	factory := &fakeWorkCallFactory{}
	orchestrator, err := NewOrchestrator(manager, registry, factory, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	first, err := orchestrator.HandleSessionCreate(context.Background(), "user-1", tunnel.SessionCreateRequest{
		RequestID: "req-1",
		EgressID:  "direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := orchestrator.HandleSessionCreate(context.Background(), "user-1", tunnel.SessionCreateRequest{
		RequestID: "req-2",
		EgressID:  "de-fra-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == second.SessionID {
		t.Fatal("second session reused first session id")
	}
	if len(factory.closed) != 1 || factory.closed[0].ID != first.SessionID {
		t.Fatalf("closed = %+v, want first session", factory.closed)
	}
	if manager.Count() != 1 {
		t.Fatalf("manager.Count() = %d, want 1", manager.Count())
	}
}

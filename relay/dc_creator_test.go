package main

import (
	"net"
	"testing"
	"time"

	"vconnect/relay/egress"
	"vconnect/relay/tunnel"
)

type testDialer struct {
	id string
}

func (d testDialer) ID() string {
	return d.id
}

func (d testDialer) DialTCP(string, time.Duration) (net.Conn, error) {
	return nil, nil
}

func (d testDialer) UDPAssociate(string, time.Duration) (egress.UDPSession, error) {
	return nil, nil
}

func TestDCCreatorRelaySelectsRequestedEgress(t *testing.T) {
	registry, err := egress.NewRegistry("ee", testDialer{id: "ee"}, testDialer{id: "fi"})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := newDCCreatorRelay(registry)
	if err != nil {
		t.Fatal(err)
	}

	if _, id := relay.selectedDialer(); id != "ee" {
		t.Fatalf("default egress = %q, want %q", id, "ee")
	}
	relay.selectEgress(tunnel.EncodeClientHelloPayload("fi"))
	if _, id := relay.selectedDialer(); id != "fi" {
		t.Fatalf("selected egress = %q, want %q", id, "fi")
	}
}

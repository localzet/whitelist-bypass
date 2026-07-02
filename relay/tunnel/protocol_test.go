package tunnel

import "testing"

func TestEgressDiscoveryPayloads(t *testing.T) {
	request, ok := DecodeEgressListRequest(EncodeEgressListRequestPayload("user-1"))
	if !ok || request.UserID != "user-1" {
		t.Fatalf("egress list request round trip = %+v, %v", request, ok)
	}
	emptyRequest, ok := DecodeEgressListRequest(nil)
	if !ok || emptyRequest.UserID != "" {
		t.Fatalf("empty egress list request = %+v, %v", emptyRequest, ok)
	}

	want := []EgressDescriptor{{ID: "ee", IsDefault: true}, {ID: "fi"}}
	list, ok := DecodeEgressList(EncodeEgressListPayload(want))
	if !ok || len(list.Egresses) != 2 || list.Egresses[0] != want[0] || list.Egresses[1] != want[1] {
		t.Fatalf("egress list round trip = %+v, %v", list, ok)
	}

	probeRequest, ok := DecodeEgressProbeRequest(EncodeEgressProbeRequestPayload("fi"))
	if !ok || probeRequest.ID != "fi" {
		t.Fatalf("probe request round trip = %+v, %v", probeRequest, ok)
	}

	wantResult := EgressProbeResult{ID: "fi", Available: true, LatencyMS: 42}
	result, ok := DecodeEgressProbeResult(EncodeEgressProbeResultPayload(wantResult))
	if !ok || result != wantResult {
		t.Fatalf("probe result round trip = %+v, %v", result, ok)
	}
}

func TestSessionControlPayloads(t *testing.T) {
	wantRequest := SessionCreateRequest{
		RequestID: "req-1",
		UserID:    "user-1",
		EgressID:  "de-fra-1",
		Platform:  "telemost",
		Mode:      "video",
	}
	request, ok := DecodeSessionCreate(EncodeSessionCreatePayload(wantRequest))
	if !ok || request != wantRequest {
		t.Fatalf("session create round trip = %+v, %v", request, ok)
	}

	wantReady := SessionReady{
		RequestID:  "req-1",
		SessionID:  "sess-1",
		JoinLink:   "https://telemost.yandex.ru/j/abc",
		EgressID:   "de-fra-1",
		TTLSeconds: 600,
	}
	ready, ok := DecodeSessionReady(EncodeSessionReadyPayload(wantReady))
	if !ok || ready != wantReady {
		t.Fatalf("session ready round trip = %+v, %v", ready, ok)
	}
}

func TestCookiePayloadsAllowLargerControlBody(t *testing.T) {
	largePayload := make([]byte, MaxControlPayload+128)
	for i := range largePayload {
		largePayload[i] = 'x'
	}
	want := CookieSubmit{
		RequestID: "cookie-1",
		UserID:    "user-1",
		Platform:  "telemost",
		Format:    "json",
		Payload:   string(largePayload),
	}
	got, ok := DecodeCookieSubmit(EncodeCookieSubmitPayload(want))
	if !ok || got != want {
		t.Fatalf("cookie submit round trip = %+v, %v", got, ok)
	}

	ackWant := CookieAck{RequestID: "cookie-1", Platform: "telemost", Stored: true}
	ack, ok := DecodeCookieAck(EncodeCookieAckPayload(ackWant))
	if !ok || ack != ackWant {
		t.Fatalf("cookie ack round trip = %+v, %v", ack, ok)
	}
}

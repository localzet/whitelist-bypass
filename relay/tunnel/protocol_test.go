package tunnel

import "testing"

func TestEgressDiscoveryPayloads(t *testing.T) {
	want := []EgressDescriptor{{ID: "ee", IsDefault: true}, {ID: "fi"}}
	list, ok := DecodeEgressList(EncodeEgressListPayload(want))
	if !ok || len(list.Egresses) != 2 || list.Egresses[0] != want[0] || list.Egresses[1] != want[1] {
		t.Fatalf("egress list round trip = %+v, %v", list, ok)
	}

	request, ok := DecodeEgressProbeRequest(EncodeEgressProbeRequestPayload("fi"))
	if !ok || request.ID != "fi" {
		t.Fatalf("probe request round trip = %+v, %v", request, ok)
	}

	wantResult := EgressProbeResult{ID: "fi", Available: true, LatencyMS: 42}
	result, ok := DecodeEgressProbeResult(EncodeEgressProbeResultPayload(wantResult))
	if !ok || result != wantResult {
		t.Fatalf("probe result round trip = %+v, %v", result, ok)
	}
}

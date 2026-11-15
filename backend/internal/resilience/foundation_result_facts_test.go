//go:build unit

package resilience

import (
	"testing"
	"time"
)

func TestFoundationResultFacts(t *testing.T) {
	var absent *RequestState
	if _, ok := absent.GatewayRequestScope(); ok || absent.SetGatewayResponseFacts(1, true, false, "missing", false, false, "json") {
		t.Fatal("nil state accepted")
	}
	s := &RequestState{}
	if s.SetGatewayResponseFacts(1, true, false, "missing", false, false, "json") {
		t.Fatal("missing trace")
	}
	s.EnableGatewayRequest(time.Now())
	s.NoteGatewaySend(1)
	if _, ok := s.GatewayRequestScope(); !ok {
		t.Fatal("missing scope trace")
	}
	for _, tc := range []struct {
		known, present bool
		source         string
		seen, valid    bool
		kind           string
		want           bool
	}{
		{false, false, "missing", false, false, "json", false},
		{true, false, "missing", false, false, "json", true},
		{true, true, "upstream_json", false, false, "json", true},
		{true, true, "upstream_sse_terminal", true, true, "sse", true},
		{true, false, "missing", true, false, "sse", true},
		{true, false, "upstream_json", false, false, "json", false},
		{true, true, "missing", false, false, "json", false},
		{true, true, "unknown", false, false, "json", false},
		{true, false, "missing", false, true, "sse", false},
		{true, false, "missing", true, true, "json", false},
		{true, false, "missing", false, false, "binary", false},
	} {
		if got := s.SetGatewayResponseFacts(1, tc.known, tc.present, tc.source, tc.seen, tc.valid, tc.kind); got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
	if s.SetGatewayResponseFacts(2, true, false, "missing", false, false, "json") {
		t.Fatal("wrong account")
	}
	prior := s.gatewayRecord.UsageObserved
	s.SetGatewayResponseFacts(1, true, true, "upstream_json", false, false, "json")
	if prior == s.gatewayRecord.UsageObserved || *prior {
		t.Fatal("facts reused mutable pointer")
	}
	_, _ = s.ClaimGatewayRequest(false)
	if s.SetGatewayResponseFacts(1, true, false, "missing", false, false, "json") {
		t.Fatal("modified claimed trace")
	}
}

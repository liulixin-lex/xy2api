//go:build unit

package schedquality

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBasisEvidenceJSONRoundTrip(t *testing.T) {
	r := RuleBResult{NLocal: 11.25, Counts: RuleBCounts{N: 30, K: 7, X: 2, S: 3, D: 2}, Prior: .12, AbsoluteProbability: .81, Tolerance: .17, Confidence: .91}
	basis := Basis{RuleVersion: RuleVersion, Line: DimensionTTFO, Kind: "review", Threshold: 7, TTFT: NewTTFTEvidence(r, 7)}
	data, err := json.Marshal(basis)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct{ TTFT map[string]any }
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"limit_s": float64(7), "n_local": 11.25, "n": float64(30), "k": float64(7), "x": float64(2), "s": float64(3), "d": float64(2), "prior_mu": .12, "p_abs": .81, "p_rel": nil, "u": nil, "z": nil, "causes": []any{}, "tolerance": .17, "confidence": .91}
	if !reflect.DeepEqual(wire.TTFT, want) {
		t.Fatalf("flat evidence: %s", data)
	}
	var restored Basis
	if err = json.Unmarshal(data, &restored); err != nil || !reflect.DeepEqual(restored, basis) {
		t.Fatalf("roundtrip: %+v / %v", restored, err)
	}
	// Relative evidence in old rows remains readable, and missing settings stay unknown.
	var historical TTFTEvidence
	if err = json.Unmarshal([]byte("{\"peer_nu\":0.23,\"p_rel\":0.71,\"u\":0.21,\"z\":0.26}"), &historical); err != nil {
		t.Fatal(err)
	}
	if historical.PeerNu != .23 || historical.PRel == nil || *historical.PRel != .71 || historical.Tolerance != nil || historical.Confidence != nil {
		t.Fatalf("history: %+v", historical)
	}

	// No causes is an empty string array, not JSON null.
	data, err = json.Marshal(NewTTFTEvidence(RuleBResult{}, 7))
	if err != nil || !strings.Contains(string(data), `"causes":[]`) {
		t.Fatalf("empty causes: %s / %v", data, err)
	}
}

func TestVerdictTextRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		verdict Verdict
		text    string
	}{{VerdictPass, "pass"}, {VerdictFail, "fail"}, {VerdictUnknown, "unknown"}} {
		data, err := json.Marshal(tc.verdict)
		if err != nil || string(data) != `"`+tc.text+`"` {
			t.Fatalf("serialized verdict = %s / %v", data, err)
		}
		var restored Verdict
		if err := json.Unmarshal(data, &restored); err != nil || restored != tc.verdict {
			t.Fatalf("restored verdict = %v / %v", restored, err)
		}
	}
	if _, err := json.Marshal(Verdict(2)); err == nil {
		t.Fatal("undefined verdict must not serialize as a valid verdict")
	}
	v := VerdictPass
	if err := v.UnmarshalText([]byte("invalid")); err == nil || v != VerdictPass {
		t.Fatal("invalid verdict must fail without changing the receiver")
	}
}

func TestDomainZeroTimesJSONRoundTrip(t *testing.T) {
	for _, value := range []any{Basis{}, Decision{}, LineState{}, LineView{}, OverrideRef{}, AttemptSample{}, AccountSummary{}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "0001-01-01") {
			t.Fatalf("%T leaked a zero date: %s", value, data)
		}
		restored := reflect.New(reflect.TypeOf(value))
		if err := json.Unmarshal(data, restored.Interface()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored.Elem().Interface(), value) {
			t.Fatalf("zero-time round trip changed %T", value)
		}
	}
}

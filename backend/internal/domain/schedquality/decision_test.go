//go:build unit

package schedquality

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBasisEvidenceJSONRoundTrip(t *testing.T) {
	pRel, u, z := .71, .21, .26
	for _, peersEnough := range []bool{false, true} {
		name := "insufficient-peers"
		if peersEnough {
			name = "enough-peers"
		}
		t.Run(name, func(t *testing.T) {
			r := RuleBResult{
				NLocal:     11.25,
				Counts:     RuleBCounts{N: 30, K: 7, X: 2, S: 3, D: 2},
				PeerCounts: RuleBCounts{N: 25, X: 5}, Prior: .12, PeerPrior: .23,
				AbsoluteProbability: .81, RelativeProbability: &pRel, U: &u, RelativeThreshold: &z,
				UnknownReasons: []RuleBUnknownReason{RuleBRelativeUnconfirmed},
			}
			cut := machineNow
			if !peersEnough {
				r.PeerCounts.X = 6
				r.UnknownReasons = []RuleBUnknownReason{RuleBPeerKnownInsufficient, RuleBNoPeers}
				cut = time.Time{}
			}
			basis := Basis{
				RuleVersion: RuleVersion, Line: DimensionTTFO, Kind: "review", Cut: cut,
				Threshold: 7, SourceGroups: []int64{10, 20}, TTFT: NewTTFTEvidence(r, 7),
				Error: &ErrorAssessment{Verdict: VerdictFail}, Cache: &CacheAssessment{Verdict: VerdictUnknown},
			}
			data, err := json.Marshal(basis)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				TTFT  map[string]any `json:"ttft"`
				Error struct {
					Verdict string `json:"verdict"`
				} `json:"error"`
				Cache struct {
					Verdict string `json:"verdict"`
				} `json:"cache"`
			}
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"limit_s": float64(7), "n_local": 11.25, "n": float64(30), "k": float64(7), "x": float64(2),
				"s": float64(3), "d": float64(2), "prior_mu": .12, "peer_nu": .23, "p_abs": .81,
				"p_rel": nil, "u": nil, "z": nil,
				"causes": []any{"peer_known_insufficient", "no_peers"},
			}
			if peersEnough {
				want["p_rel"], want["u"], want["z"] = pRel, u, z
				want["causes"] = []any{"relative_unconfirmed"}
			}
			if !reflect.DeepEqual(wire.TTFT, want) || wire.Error.Verdict != "fail" || wire.Cache.Verdict != "unknown" {
				t.Fatalf("incorrect flat evidence contract: %s", data)
			}
			if cut.IsZero() && strings.Contains(string(data), `"cut"`) {
				t.Fatalf("zero cut must be omitted: %s", data)
			}
			var restored Basis
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored, basis) {
				t.Fatalf("stored basis round trip changed evidence: %+v", restored)
			}
		})
	}
	// No causes is an empty string array, not JSON null.
	data, err := json.Marshal(NewTTFTEvidence(RuleBResult{}, 7))
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

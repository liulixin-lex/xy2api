//go:build unit

package service

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

// This fixture also runs unchanged against the pre-change source to capture the
// old response. Persisted evidence is decoded as JSON so old readers ignore new keys.
func TestReadResponseCompatibility(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var basis q.Basis
	require.NoError(t, json.Unmarshal([]byte(`{"rule_version":"4.0","line":"ttfo","kind":"review","cut":"2026-01-01T06:00:00Z","threshold":1,"source_groups":[1,2],"samples_local":12,"samples_total":24,"ttft":{"n":24,"k":9,"s":3,"d":2,"tolerance":0.17,"window_from":"2026-01-01T08:00:00Z","window_to":"2026-01-01T12:00:00Z"}}`), &basis))
	states := []q.LineState{}
	for _, model := range []string{"mixed", "rear", "trial"} {
		for _, line := range []q.Dimension{q.DimensionTTFO, q.DimensionCache} {
			b := basis
			b.Line = line
			if line == q.DimensionCache {
				b.TTFT = nil
				require.NoError(t, json.Unmarshal([]byte(`{"line":"cache","threshold":0.8,"cache":{"min_ratio":0.8,"ratio":0.62,"turns":24,"sessions":2,"path":"single5","low_sessions_10":1,"window_from":"2026-01-01T11:30:00Z","window_to":"2026-01-01T12:00:00Z"}}`), &b))
			}
			state := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 1, Model: model, Line: line}, Identity: q.IdentityRear, Until: at.Add(20 * time.Minute), Level: 3, Basis: &b}
			if line == q.DimensionCache {
				state.Until, state.Level = at.Add(40*time.Minute), 1
			}
			if model == "trial" || model == "mixed" && line == q.DimensionCache {
				state.Identity, state.Trial, state.Until, state.Epoch, state.TrialUntil = q.IdentityUnknown, true, time.Time{}, at, at.Add(time.Hour)
				state.LastAssessment = q.RecordAssessment(state, at.Add(time.Minute), q.EventTrial, q.VerdictPass, "", q.Basis{Line: line, Cache: &q.CacheAssessment{Turns: 40}})
				state.EvaluatedAt = at.Add(time.Minute)
			}
			states = append(states, state)
		}
	}
	f := newQualityFixture(t)
	f.e.members = map[int64][]int64{1: {1}}
	f.e.policies = map[int64]SchedPolicyRecord{1: {Policy: q.Policy{Enabled: true, TTFTSeconds: 9, MinCacheRatio: .95, MaxErrorRate: .01}}}
	f.e.loaded = true
	f.e.publish(&schedQualityData{now: at, globalEnabled: true}, states, nil, nil, at)
	s, e, _ := schedAdminFixture()
	s.d.Now = func() time.Time { return at }
	e.snapshot = f.e.Snapshot()
	e.perf = &SchedPerfSnapshot{at: at, from: at.Add(-12 * time.Hour), complete: true, policies: e.snapshot.Policies, rows: map[schedQualityKey]*schedPerfRow{{1, "mixed", 1}: {}, {1, "rear", 1}: {}, {1, "trial", 1}: {}}}
	detail, err := s.Detail(t.Context(), 1)
	require.NoError(t, err)
	summary, err := s.Summary(t.Context(), []int64{1})
	require.NoError(t, err)
	performance, err := s.AccountPerformance(t.Context(), 1)
	require.NoError(t, err)
	response := map[string]any{"summary": summary, "detail": detail, "performance": performance}
	raw, err := json.MarshalIndent(response, "", "  ")
	require.NoError(t, err)
	if capture := os.Getenv("READ_RESPONSE_CAPTURE"); capture != "" {
		require.NoError(t, os.WriteFile(capture, raw, 0600))
	} else {
		baseline, err := os.ReadFile("testdata/sched_quality/read_baseline.json")
		require.NoError(t, err)
		var old, current any
		require.NoError(t, json.Unmarshal(baseline, &old))
		require.NoError(t, json.Unmarshal(raw, &current))
		readPreservesFields(t, old, current)
	}
	// Fifty synthetic accounts, each with two rear composites and one trial composite.
	account := summary.Accounts[0]
	summary.Accounts = nil
	for id := int64(1); id <= 50; id++ {
		item := account
		item.AccountID = id
		summary.Accounts = append(summary.Accounts, item)
	}
	page, err := json.Marshal(summary)
	require.NoError(t, err)
	t.Logf("50-account summary JSON bytes=%d", len(page))
}

func readPreservesFields(t *testing.T, old, current any) {
	t.Helper()
	switch want := old.(type) {
	case map[string]any:
		got, ok := current.(map[string]any)
		require.True(t, ok)
		for key, value := range want {
			require.Contains(t, got, key)
			readPreservesFields(t, value, got[key])
		}
	case []any:
		got, ok := current.([]any)
		require.True(t, ok)
		require.Len(t, got, len(want))
		for i := range want {
			readPreservesFields(t, want[i], got[i])
		}
	default:
		require.Equal(t, old, current)
	}
}

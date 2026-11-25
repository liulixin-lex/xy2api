//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func readBasis(line q.Dimension, at time.Time) q.Basis {
	b := q.Basis{RuleVersion: q.RuleVersion, Line: line, Kind: "regular", Cut: at.Add(-6 * time.Hour), SourceGroups: []int64{1, 2}, SampleCounts: q.NewSampleCounts(12, 24)}
	switch line {
	case q.DimensionTTFO:
		b.Threshold = 1
		b.TTFT = &q.TTFTEvidence{N: 24, K: 9, S: 3, D: 2, Tolerance: new(.17), WindowFrom: at.Add(-q.RuleBWindow), WindowTo: at}
	case q.DimensionError:
		b.Threshold = .05
		b.Error = &q.ErrorAssessment{Clear: 50, Failed: 10, Limit: .05, WindowFrom: at.Add(-q.ErrorWindow), WindowTo: at}
	case q.DimensionCache:
		b.Threshold = .8
		b.Cache = &q.CacheAssessment{MinRatio: .8, Ratio: .62, Turns: 24, Sessions: 2, LowSessions10: new(1), Path: q.CachePathSingle5, WindowFrom: at.Add(-q.CacheWindow), WindowTo: at}
	}
	return b
}

func TestReadRestrictedLineCombinations(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Each line independently occupies normal/rear/trial: all 3^3 combinations.
	for combo := 0; combo < 27; combo++ {
		for _, mode := range []string{"active", "exempt", "not_published", "membership_removed"} {
			t.Run(fmt.Sprintf("%02d/%s", combo, mode), func(t *testing.T) {
				var states []q.LineState
				var wanted []SchedAdminLineDetail
				var rearLines []q.Dimension
				var rearLast, trialLast q.LineState
				value := combo
				for i, line := range q.Lines {
					state := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 1, Model: "synthetic-model", Line: line}}
					b := readBasis(line, at)
					state.Basis = &b
					switch value % 3 {
					case 1:
						state.Identity, state.Level, state.Until = q.IdentityRear, i+1, at.Add(time.Duration(i+1)*20*time.Minute)
						wanted = append(wanted, SchedAdminLineDetail{Line: line, Identity: "rear", Until: schedAdminStamp(state.Until), Basis: SchedAdminDecisionBasis(b)})
						rearLines, rearLast = append(rearLines, line), state
					case 2:
						state.Trial, state.Level, state.Epoch, state.TrialUntil = true, i+1, at, at.Add(time.Duration(i+1)*30*time.Minute)
						state.LastAssessment = q.RecordAssessment(state, at.Add(time.Minute), q.EventTrial, q.VerdictUnknown, "", b)
						state.EvaluatedAt = at.Add(time.Minute)
						wanted = append(wanted, SchedAdminLineDetail{Line: line, Identity: "trial", Until: schedAdminStamp(state.TrialUntil), LastEval: SchedAdminLastEvalFromState(state)})
						trialLast = state
					}
					value /= 3
					states = append(states, state)
				}
				f := newQualityFixture(t)
				f.e.loaded = true
				f.e.members = map[int64][]int64{1: {1}}
				f.e.policies = map[int64]SchedPolicyRecord{1: {Policy: q.Policy{Enabled: true, TTFTSeconds: 1, MinCacheRatio: .8}}}
				var overrides []SchedOverride
				if mode == "exempt" {
					overrides = []SchedOverride{{AccountID: 1, GroupID: 1, Mode: q.OverrideExempt}}
				}
				if mode == "not_published" {
					f.e.members[1] = nil
				}
				f.e.publish(&schedQualityData{now: at, globalEnabled: true}, states, overrides, nil, at)
				s, e, repo := schedAdminFixture()
				s.d.Now = func() time.Time { return at }
				e.snapshot = f.e.Snapshot()
				if mode == "membership_removed" {
					repo.rows[1].Groups, repo.rows[1].GroupIDs = nil, nil
				}
				summary, err := s.Summary(t.Context(), []int64{1})
				require.NoError(t, err)
				a := summary.Accounts[0]
				if mode == "not_published" || mode == "membership_removed" {
					require.Empty(t, a.Rear)
					require.Empty(t, a.Trial)
					detail, err := s.Detail(t.Context(), 1)
					require.NoError(t, err)
					for _, group := range detail.Groups {
						require.Empty(t, group.ByModel)
					}
					if mode == "not_published" {
						require.Empty(t, e.snapshot.RowsForAccount(1))
					}
					return
				}
				if len(rearLines) > 0 {
					require.Empty(t, a.Trial)
					require.Len(t, a.Rear, 1)
					r := a.Rear[0]
					require.Equal(t, wanted, r.LineDetails)
					require.Equal(t, rearLines, r.Lines)
					require.Equal(t, rearLast.Line, r.UntilLine)
					require.Equal(t, rearLast.Level, r.Level)
					require.Equal(t, schedAdminStamp(rearLast.Until), r.Until)
					require.Equal(t, mode == "exempt", r.Exempt)
					require.Equal(t, mode == "active", r.Effective)
				} else if len(wanted) > 0 && mode == "active" {
					require.Empty(t, a.Rear)
					require.Len(t, a.Trial, 1)
					r := a.Trial[0]
					require.Equal(t, wanted, r.LineDetails)
					require.Equal(t, trialLast.Line, r.UntilLine)
					require.Equal(t, schedAdminStamp(trialLast.TrialUntil), r.Until)
				} else {
					require.Empty(t, a.Rear)
					require.Empty(t, a.Trial)
				}
			})
		}
	}
}

func TestReadErrorWindowDelayedInsertionRemainsInFlight(t *testing.T) {
	f := newQualityFixture(t)
	at := f.now
	f.e.policies = f.store.Policies
	first := engineSamples(1, at.Add(-2*time.Second), false)[0]
	first.RequestID, first.AttemptID = "same-request", "first-attempt"
	first.StartedAt = at.Add(-10 * time.Second)
	first.Outcome, first.Class = q.OutcomeAccountFault, q.ClassUpstream
	last := first
	last.AttemptID, last.StartedAt, last.FinishedAt, last.InsertedAt = "final-attempt", at.Add(-3*time.Second), at.Add(-time.Second), at.Add(time.Second)
	key := q.LineKey{AccountID: 1, GroupID: 10, Model: last.Model, Line: q.DimensionError}
	d := &schedQualityData{now: at, samples: map[SchedAccountModel][]q.AttemptSample{{1, last.Model}: {first, last}}}
	for _, offset := range []time.Duration{-time.Nanosecond, 0} {
		when := last.InsertedAt.Add(offset)
		_, basis := f.e.assessLine(d, q.LineState{LineKey: key}, q.EventRegular, when, nil)
		require.Equal(t, when.Add(-q.ErrorWindow), basis.Error.WindowFrom)
		require.Equal(t, when, basis.Error.WindowTo)
		if offset < 0 {
			require.Zero(t, basis.Error.Clear)
			require.Equal(t, 1, basis.Error.Unknown)
			require.Empty(t, basis.AttemptIDs, "neither earlier failure nor future final result is exposed")
		} else {
			require.Equal(t, 1, basis.Error.Clear)
			require.Zero(t, basis.Error.Unknown)
			require.Equal(t, []string{last.AttemptID}, basis.AttemptIDs)
		}
	}
}

// Exercise the real publication pass, without evaluating or writing a transition.
func readPublishedService(t *testing.T, at time.Time, states ...q.LineState) (*SchedAdminService, *schedAdminEngine) {
	t.Helper()
	f := newQualityFixture(t)
	f.e.members = map[int64][]int64{1: {1}}
	f.e.policies = map[int64]SchedPolicyRecord{1: {Policy: q.Policy{Enabled: true, TTFTSeconds: 9, MinCacheRatio: .95, MaxErrorRate: .01}}}
	f.e.loaded = true
	f.e.publish(&schedQualityData{now: at, globalEnabled: true}, states, nil, nil, at)
	s, e, _ := schedAdminFixture()
	s.d.Now = func() time.Time { return at }
	e.snapshot = f.e.Snapshot()
	e.perf = &SchedPerfSnapshot{at: at, from: at.Add(-12 * time.Hour), complete: true, policies: e.snapshot.Policies, rows: map[schedQualityKey]*schedPerfRow{{1, "synthetic-model", 1}: {}}}
	return s, e
}

func TestReadRearBasisUsesLatestCommittedAction(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, line := range q.Lines {
		t.Run(string(line), func(t *testing.T) {
			key := q.LineKey{AccountID: 1, GroupID: 1, Model: "synthetic-model", Line: line}
			prev := q.LineState{LineKey: key, PendingAt: at.Add(-q.RegularInterval)}
			state, action := q.Step(prev, q.EventRegular, q.VerdictFail, readBasis(line, at), at)
			require.Equal(t, q.ActionEnter, action)
			for _, stage := range []string{"enter", "renew", "trial", "trial_fail", "reset"} {
				t.Run(stage, func(t *testing.T) {
					stamp := state.Entry.Add(time.Minute)
					switch stage {
					case "renew":
						stamp = state.Until
						b := readBasis(line, stamp)
						b.Kind = "review"
						state, action = q.Step(state, q.EventReview, q.VerdictFail, b, stamp)
						require.Equal(t, q.ActionRenew, action)
					case "trial":
						stamp = state.Until
						state, action = q.Step(state, q.EventReview, q.VerdictUnknown, readBasis(line, stamp), stamp)
						require.Equal(t, q.ActionTrial, action)
					case "trial_fail":
						stamp = state.Epoch.Add(time.Minute)
						state, action = q.Step(state, q.EventTrial, q.VerdictFail, readBasis(line, stamp), stamp)
						require.Equal(t, q.ActionTrialFail, action)
					case "reset":
						state = q.Relaxed(key)
					}
					s, _ := readPublishedService(t, stamp, state)
					detail, err := s.Detail(t.Context(), 1)
					require.NoError(t, err)
					panel := schedAdminJSON(t, detail.Groups[0].ByModel[0].Lines)[string(line)].(map[string]any)
					perf, err := s.AccountPerformance(t.Context(), 1)
					require.NoError(t, err)
					performance := schedAdminJSON(t, perf.Rows[0].Lines)[string(line)].(map[string]any)
					summary, err := s.Summary(t.Context(), []int64{1})
					require.NoError(t, err)
					if state.Identity != q.IdentityRear {
						require.NotContains(t, panel, "basis")
						require.NotContains(t, performance, "basis")
						require.Empty(t, summary.Accounts[0].Rear)
						return
					}
					decision := schedAdminJSON(t, SchedAdminDecision{Basis: SchedAdminDecisionBasis(*state.Basis)})
					list := schedAdminJSON(t, summary.Accounts[0].Rear[0].LineDetails[0])
					require.Equal(t, decision["basis"], panel["basis"])
					require.Equal(t, decision["basis"], performance["basis"])
					require.Equal(t, decision["basis"], list["basis"])
					require.Equal(t, schedAdminStamp(state.Until), list["until"])
					if stage == "renew" {
						require.NotEqual(t, SchedAdminDecisionBasis(readBasis(line, at)), summary.Accounts[0].Rear[0].LineDetails[0].Basis)
					}
				})
			}
		})
	}
}

func TestReadTrialLatestAssessmentAndFortyTurnBoundary(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, turns := range []int{39, 40} {
		key := q.LineKey{AccountID: 1, GroupID: 1, Model: "synthetic-model", Line: q.DimensionCache}
		initial := readBasis(q.DimensionCache, at)
		prev := q.LineState{LineKey: key, Trial: true, Epoch: at, TrialUntil: at.Add(q.TrialDuration), Basis: &initial}
		b := initial
		b.Cache = &q.CacheAssessment{Turns: turns, Ratio: .9, MinRatio: .8}
		state, action := q.Step(prev, q.EventTrial, q.VerdictPass, b, at.Add(time.Minute))
		require.Empty(t, action)
		require.True(t, state.Trial)
		s, _ := readPublishedService(t, at.Add(2*time.Minute), state)
		detail, err := s.Detail(t.Context(), 1)
		require.NoError(t, err)
		line := detail.Groups[0].ByModel[0].Lines.Cache
		require.Nil(t, line.Basis)
		require.NotNil(t, line.LastEval)
		if turns == 39 {
			require.Equal(t, q.ReasonTrialOpen, line.LastEval.Reason)
			require.Equal(t, new(float64(39)), line.LastEval.Have)
			require.Equal(t, new(40), line.LastEval.Need)
		} else {
			require.Equal(t, q.ReasonWithin, line.LastEval.Reason)
		}
		summary, err := s.Summary(t.Context(), []int64{1})
		require.NoError(t, err)
		require.Equal(t, line.LastEval, summary.Accounts[0].Trial[0].LineDetails[0].LastEval)
		perf, err := s.AccountPerformance(t.Context(), 1)
		require.NoError(t, err)
		require.Equal(t, line.LastEval, perf.Rows[0].Lines.Cache.LastEval)
	}
}

func TestReadHistoricalBasisDoesNotFillMissingFields(t *testing.T) {
	var b q.Basis
	require.NoError(t, json.Unmarshal([]byte(`{"line":"ttfo","ttft":{"n":24},"error":{"clear":50},"cache":{"turns":20}}`), &b))
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s, _ := readPublishedService(t, at, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 1, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Until: at.Add(time.Hour), Basis: &b})
	detail, err := s.Detail(t.Context(), 1)
	require.NoError(t, err)
	got := detail.Groups[0].ByModel[0].Lines.TTFO.Basis
	require.NotNil(t, got)
	require.Nil(t, got.TTFT.Tolerance)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	for _, absent := range []string{"tolerance", "window_from", "window_to"} {
		require.NotContains(t, string(raw), absent)
	}
}

func TestReadLineDetailsKeepCompositeAndIndependentDeadlines(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, trialCache := range []bool{false, true} {
		firstBasis, cacheBasis := readBasis(q.DimensionTTFO, at), readBasis(q.DimensionCache, at.Add(time.Minute))
		first := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 1, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Level: 3, Until: at.Add(20 * time.Minute), Basis: &firstBasis}
		cache := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 1, Model: "synthetic-model", Line: q.DimensionCache}, Identity: q.IdentityRear, Level: 1, Until: at.Add(40 * time.Minute), Basis: &cacheBasis}
		if trialCache {
			cache.Identity, cache.Trial = q.IdentityUnknown, true
			cache.Until, cache.Epoch, cache.TrialUntil = time.Time{}, at, at.Add(time.Hour)
			cache.LastAssessment = q.RecordAssessment(cache, at.Add(time.Minute), q.EventTrial, q.VerdictPass, "", q.Basis{Line: q.DimensionCache, Cache: &q.CacheAssessment{Turns: 40}})
			cache.EvaluatedAt = at.Add(time.Minute)
		}
		s, _ := readPublishedService(t, at.Add(2*time.Minute), first, cache)
		summary, err := s.Summary(t.Context(), []int64{1})
		require.NoError(t, err)
		account := summary.Accounts[0]
		require.Empty(t, account.Trial)
		require.Len(t, account.Rear, 1)
		rear := account.Rear[0]
		require.Len(t, rear.LineDetails, 2)
		require.Equal(t, q.DimensionTTFO, rear.LineDetails[0].Line)
		require.Equal(t, "rear", rear.LineDetails[0].Identity)
		require.Equal(t, schedAdminStamp(first.Until), rear.LineDetails[0].Until)
		require.Equal(t, SchedAdminDecisionBasis(firstBasis), rear.LineDetails[0].Basis)
		require.Equal(t, q.DimensionCache, rear.LineDetails[1].Line)
		if trialCache {
			require.Equal(t, []q.Dimension{q.DimensionTTFO}, rear.Lines)
			require.Equal(t, q.DimensionTTFO, rear.UntilLine)
			require.Equal(t, first.Level, rear.Level)
			require.Equal(t, schedAdminStamp(first.Until), rear.Until)
			require.Equal(t, "trial", rear.LineDetails[1].Identity)
			require.Equal(t, schedAdminStamp(cache.TrialUntil), rear.LineDetails[1].Until)
			require.Nil(t, rear.LineDetails[1].Basis)
			require.Equal(t, q.ReasonWithin, rear.LineDetails[1].LastEval.Reason)
		} else {
			require.Equal(t, []q.Dimension{q.DimensionTTFO, q.DimensionCache}, rear.Lines)
			require.Equal(t, q.DimensionCache, rear.UntilLine)
			require.Equal(t, cache.Level, rear.Level)
			require.Equal(t, schedAdminStamp(cache.Until), rear.Until)
			require.Equal(t, "rear", rear.LineDetails[1].Identity)
			require.Equal(t, schedAdminStamp(cache.Until), rear.LineDetails[1].Until)
			require.Equal(t, SchedAdminDecisionBasis(cacheBasis), rear.LineDetails[1].Basis)
		}
	}
}

func TestReadLineDetailsIdentifyMissingHistoricalMetadata(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	key := q.LineKey{AccountID: 1, GroupID: 1, Model: "synthetic-model", Line: q.DimensionTTFO}
	rear := q.LineState{LineKey: key, Identity: q.IdentityRear, Until: at.Add(time.Hour)}
	key.Line = q.DimensionCache
	trial := q.LineState{LineKey: key, Trial: true, TrialUntil: at.Add(2 * time.Hour)}
	s, _ := readPublishedService(t, at, rear, trial)
	summary, err := s.Summary(t.Context(), []int64{1})
	require.NoError(t, err)
	details := summary.Accounts[0].Rear[0].LineDetails
	require.Len(t, details, 2)
	for i, identity := range []string{"rear", "trial"} {
		got := schedAdminJSON(t, details[i])
		require.Equal(t, identity, got["identity"])
		require.NotContains(t, got, "basis")
		require.NotContains(t, got, "last_eval")
	}
}

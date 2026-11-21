//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestRecoveryReleaseStartsFreshEvidence(t *testing.T) {
	f := newQualityFixture(t)
	origin := f.now.Truncate(time.Minute).Add(5 * time.Second)
	at := origin.Add(15 * time.Minute)
	key := q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}
	f.store.Rows = []q.LineState{{LineKey: key, Trial: true, Epoch: origin, TrialUntil: origin.Add(120 * time.Minute), EvaluatedAt: at.Add(-time.Second), Admissions: 1, Level: 2, LastRear: origin.Add(-80 * time.Minute)}}
	f.ledger.Rows = errorRecoverySamples(52, 0, at.Add(-time.Second))
	f.now = at.Add(10 * time.Second)
	f.e.observedSince = origin.Add(-time.Hour)
	f.cycle()
	r := f.line(q.DimensionError)
	require.False(t, r.Trial)
	require.Equal(t, q.IdentityUnknown, r.Identity)
	require.Equal(t, at, r.Epoch)
	require.Equal(t, "qualified", f.store.Decisions[0].Basis.ReleaseReason)
	f.now = at.Truncate(5 * time.Minute).Add(5*time.Minute + 10*time.Second)
	f.cycle()
	r = f.line(q.DimensionError)
	require.Equal(t, q.IdentityUnknown, r.Identity)
	require.NotNil(t, r.LastAssessment)
	require.Equal(t, at, r.Epoch)
	var zeroSeen bool
	for _, group := range f.e.Snapshot().RowsForAccount(1) {
		if group.Model == key.Model {
			zeroSeen = true
			require.Equal(t, 0, group.Error.Clear)
		}
	}
	require.True(t, zeroSeen)
	// Only genuinely new request units may turn the normal label into pass.
	after := f.now.Add(10 * time.Second)
	fresh := errorRecoverySamples(52, 0, after)
	for i := range fresh {
		fresh[i].RequestID = "fresh-" + fresh[i].AttemptID
	}
	f.ledger.Rows = append(f.ledger.Rows, fresh...)
	f.now = f.now.Add(5 * time.Minute)
	f.cycle()
	require.Equal(t, q.IdentityPass, f.line(q.DimensionError).Identity)
}

func TestRecoveryHistoricalSixtyCannotUseFuture(t *testing.T) {
	for _, line := range q.Lines {
		for _, restarted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart%t", line, restarted), func(t *testing.T) {
				f := newQualityFixture(t)
				origin := f.now.Truncate(time.Minute).Add(5 * time.Second)
				check := origin.Add(time.Hour)
				r := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: line}, Trial: true, Epoch: origin, TrialUntil: origin.Add(120 * time.Minute), EvaluatedAt: check.Add(-time.Second), Admissions: 1, LastRear: origin.Add(-20 * time.Minute)}
				f.store.Rows = []q.LineState{r}
				f.now = check.Add(time.Minute + 10*time.Second)
				f.e.observedSince = origin.Add(-time.Minute)
				a := engineSamples(1, check.Add(time.Minute), true)[0]
				a.StartedAt = check.Add(time.Second)
				a.GroupID = 99
				a.Continuation = true
				a.SessionKey = "new-after-check"
				a.InputTokens = new(1)
				a.CacheReadTokens = new(99)
				a.CacheCreationTokens = new(0)
				f.ledger.Rows = []q.AttemptSample{a}
				if restarted {
					f.e = NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
				}
				f.cycle()
				got := f.line(line)
				if restarted {
					require.True(t, got.Trial)
					require.Empty(t, f.store.Decisions)
				} else {
					require.False(t, got.Trial)
					require.Equal(t, check, got.Epoch)
					require.Len(t, f.store.Decisions, 1)
					require.Equal(t, "no_samples", f.store.Decisions[0].Basis.ReleaseReason)
					require.Equal(t, check, f.store.Decisions[0].At)
				}
				raw, err := json.Marshal(f.store.Rows)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &f.store.Rows))
				prior := len(f.store.Decisions)
				f.cycle()
				require.Len(t, f.store.Decisions, prior)
			})
		}
	}
}

func TestRecoveryClearBeforeLaterFailure(t *testing.T) {
	f := newQualityFixture(t)
	f.badTTFT()
	at := f.now.Add(-10 * time.Second)
	deadline := at.Add(-time.Minute)
	for _, line := range q.Lines {
		f.store.Rows = append(f.store.Rows, q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: line}, Level: 3, Admissions: 4, LastRear: deadline.Add(-24 * time.Hour), EvaluatedAt: deadline.Add(-time.Second)})
	}
	f.cycle()
	require.Equal(t, q.IdentityRear, f.line(q.DimensionTTFO).Identity)
	require.Zero(t, f.line(q.DimensionTTFO).Level)
	var cleared int
	for _, d := range f.store.Decisions {
		if d.Key.AccountID == 1 && d.Action == q.ActionClear {
			cleared++
			require.Equal(t, deadline, d.At)
		}
	}
	require.Equal(t, 3, cleared)
}

func TestRecoveryCommitRollbackRetainsEverything(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			output := captureSchedObsLogs(t)
			f := newQualityFixture(t)
			at := f.now.Add(-10 * time.Second)
			origin := at.Add(-120 * time.Minute)
			r := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, Trial: true, Epoch: origin, TrialUntil: at, EvaluatedAt: at.Add(-time.Second), Level: 3, Admissions: 2, LastRear: origin.Add(-160 * time.Minute)}
			if missing {
				r.TrialUntil = time.Time{}
			}
			f.store.Rows = []q.LineState{r}
			f.e.processedAt = at.Add(-time.Second)
			snapshot := f.e.Snapshot()
			watermark := f.e.processedAt
			before, err := json.Marshal(f.store.Rows)
			require.NoError(t, err)
			f.store.CommitErr = context.Canceled
			require.ErrorIs(t, f.e.cycle(context.Background()), context.Canceled)
			after, err := json.Marshal(f.store.Rows)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
			require.Same(t, snapshot, f.e.Snapshot())
			require.Equal(t, watermark, f.e.processedAt)
			require.Empty(t, f.store.Decisions)
			for _, entry := range schedObsLogRecords(t, output) {
				require.NotContains(t, []string{"sched_quality.state", "sched_quality.missing_deadline", "sched_quality.history_missing"}, entry["msg"])
			}
			output.Reset()
			f.store.CommitErr = nil
			f.cycle()
			require.False(t, f.line(q.DimensionError).Trial)
			require.Len(t, f.store.Decisions, 1)
			require.Equal(t, at, f.store.Decisions[0].At)
			output.Reset()
			f.cycle()
			require.Len(t, f.store.Decisions, 1)
			for _, entry := range schedObsLogRecords(t, output) {
				require.NotEqual(t, "sched_quality.state", entry["msg"])
			}
		})
	}
}

// Group policy and global Rule B relaxation use the same clock-preserving path.
func TestRecoveryRelaxPreservesRecentFailureClock(t *testing.T) {
	for _, scope := range []string{"group", "global"} {
		for _, next := range []string{"fail_after_five", "quiet_clear", "reset"} {
			t.Run(scope+"/"+next, func(t *testing.T) {
				f := newQualityFixture(t)
				f.now = f.now.Truncate(time.Minute)
				f.badTTFT()
				at := f.now
				for i := range f.ledger.Rows {
					f.ledger.Rows[i].TTFOMs = new(100)
				}
				last := at.Add(-10 * time.Minute)
				a := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Level: 3, Admissions: 4, Entry: last, Epoch: last, LastRear: last, Until: at.Add(150 * time.Minute), EvaluatedAt: at.Add(-time.Minute)}
				b := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionCache}, Identity: q.IdentityUnknown, Level: 2, Admissions: 3, LastRear: at.Add(-30 * time.Hour), EvaluatedAt: at.Add(-time.Minute)}
				f.store.Rows = []q.LineState{a, b}
				if scope == "group" {
					policy := f.store.Policies[10].Policy
					policy.TTFTSeconds = 2
					_, err := f.e.UpdatePolicy(t.Context(), 10, 0, policy)
					require.NoError(t, err)
				} else {
					params := q.DefaultRuleBParams()
					params.Confidence = .9
					_, err := f.e.UpdateTTFTRule(t.Context(), 0, params)
					require.NoError(t, err)
				}
				relaxed := f.line(q.DimensionTTFO)
				require.Equal(t, q.IdentityPass, relaxed.Identity, "A passes its immediate reassessment")
				require.Zero(t, relaxed.Level)
				require.Zero(t, relaxed.Admissions)
				require.Equal(t, last, relaxed.LastRear)
				require.Equal(t, 2, f.line(q.DimensionCache).Level)
				require.Equal(t, 3, f.line(q.DimensionCache).Admissions)
				for _, d := range f.store.Decisions {
					require.NotEqual(t, q.ActionClear, d.Action, "relaxation cannot clear sibling B")
				}
				switch next {
				case "fail_after_five":
					f.now = at.Add(5*time.Minute + 10*time.Second)
					old := len(f.ledger.Rows)
					f.badTTFT()
					for i := old; i < len(f.ledger.Rows); i++ {
						// New sessions keep recurrence evidence eligible instead of
						// looking like a switch from the preceding peer attempts.
						f.ledger.Rows[i].SessionKey = "recurrence-" + f.ledger.Rows[i].SessionKey
						f.ledger.Rows[i].AttemptID = "recurrence-" + f.ledger.Rows[i].AttemptID
						f.ledger.Rows[i].RequestID = f.ledger.Rows[i].AttemptID
						f.ledger.Rows[i].InsertedAt = f.ledger.Rows[i].FinishedAt
						if f.ledger.Rows[i].AccountID == 1 {
							f.ledger.Rows[i].TTFOMs = new(100000)
						}
					}
					f.cycle()
					failed := f.line(q.DimensionTTFO)
					require.Equal(t, q.IdentityRear, failed.Identity)
					require.Equal(t, 3, failed.Level)
					require.Equal(t, 160*time.Minute, failed.Until.Sub(failed.Entry))
					require.Equal(t, at.Add(5*time.Minute), failed.Entry)
					require.Equal(t, 2, f.line(q.DimensionCache).Level)
				case "quiet_clear":
					deadline := last.Add(q.LevelClearQuiet)
					f.now = deadline.Add(10*time.Second - time.Nanosecond)
					f.cycle()
					require.Equal(t, 2, f.line(q.DimensionCache).Level)
					f.now = deadline.Add(10 * time.Second)
					f.cycle()
					require.Zero(t, f.line(q.DimensionCache).Level)
					require.Zero(t, f.line(q.DimensionCache).Admissions)
					var cleared int
					for _, d := range f.store.Decisions {
						if d.Action == q.ActionClear {
							cleared++
							require.Equal(t, deadline, d.At)
							require.Equal(t, q.DimensionCache, d.Key.Line)
						}
					}
					require.Equal(t, 1, cleared)
					count := len(f.store.Decisions)
					f.now = f.now.Add(time.Minute)
					f.cycle()
					require.Len(t, f.store.Decisions, count)
				case "reset":
					_, err := f.e.Reset(t.Context(), 1)
					require.NoError(t, err)
					for _, r := range f.store.Rows {
						if r.AccountID == 1 {
							require.Zero(t, r.Level)
							require.Zero(t, r.Admissions)
							require.True(t, r.LastRear.IsZero())
						}
					}
				}
			})
		}
	}
}

// A NULL evaluated_at is not a new round. Replay the persisted phase deadlines
// before letting arrivals in the recovered normal segment affect the line.
func TestRecoveryReplayNullableWatermark(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		watermark, freshFailures bool
		trialDuration            time.Duration // zero starts from rear; nonzero starts from a persisted trial
	}{
		{name: "control", watermark: true},
		{name: "null_empty"},
		{name: "null_fresh_fail", freshFailures: true},
		{name: "null_trial", trialDuration: 120 * time.Minute, freshFailures: true},
		{name: "null_legacy_trial", trialDuration: 60 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureSchedObsLogs(t)
			f := newQualityFixture(t)
			origin := f.now.Truncate(time.Minute).Add(5 * time.Second)
			review := origin.Add(240 * time.Minute)
			hard := review.Add(q.TrialDuration)
			horizon := origin.Add(500 * time.Minute)
			r := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError}, Identity: q.IdentityRear, Epoch: origin, Entry: origin, LastRear: origin, Until: review, Level: 4, Admissions: 5}
			trialStart := review
			if tc.trialDuration != 0 {
				r.Identity, r.Trial, r.Until = q.IdentityUnknown, true, time.Time{}
				trialStart = origin
				hard = origin.Add(tc.trialDuration)
				r.TrialUntil = hard
			}
			if tc.watermark {
				r.EvaluatedAt = origin
			}
			f.store.Rows = []q.LineState{r}
			f.now = horizon.Add(10 * time.Second)
			f.e = NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
			if tc.freshFailures {
				f.ledger.Rows = errorRecoverySamples(20, 20, horizon.Add(-time.Minute))
			}
			f.cycle()
			decisions := f.store.Decisions
			if tc.trialDuration == 0 {
				require.Len(t, decisions, 2)
				require.Equal(t, q.ActionTrial, decisions[0].Action)
				require.Equal(t, review, decisions[0].At)
				require.Equal(t, hard, decisions[0].Until)
				require.True(t, decisions[0].Basis.HistoryMissing)
				require.Equal(t, origin, decisions[0].Basis.Cut)
			} else {
				require.Len(t, decisions, 1)
			}
			release := decisions[len(decisions)-1]
			require.Equal(t, q.ActionTrialTimeout, release.Action)
			require.Equal(t, hard, release.At)
			require.Equal(t, trialStart, release.Basis.Cut)
			require.Equal(t, "deadline", release.Basis.ReleaseReason)
			require.Equal(t, int64(hard.Sub(trialStart)/time.Second), release.Basis.TrialDurationSeconds)
			var logged int
			for _, record := range schedObsLogRecords(t, output) {
				if record["msg"] == "sched_quality.state" {
					require.Less(t, logged, len(decisions))
					require.Equal(t, string(decisions[logged].Action), record["action"])
					require.Equal(t, decisions[logged].At.Format(time.RFC3339Nano), record["at"])
					logged++
				}
			}
			require.Equal(t, len(decisions), logged)
			got := f.line(q.DimensionError)
			require.Equal(t, q.IdentityUnknown, got.Identity)
			require.False(t, got.Trial)
			require.Equal(t, hard, got.Epoch)
			require.Equal(t, origin, got.LastRear)
			grid := horizon.Truncate(q.RegularInterval)
			require.Equal(t, grid, got.EvaluatedAt)
			if tc.freshFailures {
				require.Equal(t, grid, got.PendingAt)
			} else {
				require.True(t, got.PendingAt.IsZero())
			}
			f.cycle()
			require.Equal(t, decisions, f.store.Decisions)
			require.Equal(t, got, f.line(q.DimensionError))
			if tc.freshFailures {
				f.now = f.now.Add(q.RegularInterval)
				f.cycle()
				require.Len(t, f.store.Decisions, len(decisions)+1)
				entered := f.store.Decisions[len(decisions)]
				require.Equal(t, q.ActionEnter, entered.Action)
				require.Equal(t, grid.Add(q.RegularInterval), entered.At)
				require.Equal(t, hard, entered.Basis.Cut)
				require.Equal(t, 20, entered.Basis.Error.Failed)
			}
		})
	}
}

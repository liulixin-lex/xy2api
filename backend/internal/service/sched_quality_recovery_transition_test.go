//go:build unit

package service

import (
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func legacyRecoveryRow(f *qualityFixture, trial bool, origin, deadline time.Time) q.LineState {
	r := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError},
		Level: 4, Admissions: 2, Entry: origin, Epoch: origin, LastRear: origin, EvaluatedAt: origin,
		Basis: &q.Basis{RuleVersion: "3.1", Line: q.DimensionError}}
	if trial {
		r.Trial, r.TrialUntil = true, deadline
	} else {
		r.Identity, r.Until = q.IdentityRear, deadline
	}
	f.store.Rows = []q.LineState{r}
	return r
}

func restartRecoveryTwice(t *testing.T, f *qualityFixture) {
	t.Helper()
	rows := append([]q.LineState(nil), f.store.Rows...)
	decisions := append([]q.Decision(nil), f.store.Decisions...)
	for range 2 {
		f.e = NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
		f.cycle()
		require.Equal(t, rows, f.store.Rows)
		require.Equal(t, decisions, f.store.Decisions)
	}
}

func TestRecoveryLegacyRearKeepsStoredDeadline(t *testing.T) {
	for _, tc := range []struct {
		level    int
		duration time.Duration
	}{{0, 20 * time.Minute}, {1, 40 * time.Minute}, {3, 160 * time.Minute}, {4, 240 * time.Minute}} {
		t.Run(tc.duration.String(), func(t *testing.T) {
			f := newQualityFixture(t)
			origin := f.now.Add(-10 * time.Second)
			deadline := origin.Add(tc.duration)
			row := legacyRecoveryRow(f, false, origin, deadline)
			row.Level = tc.level
			f.store.Rows[0] = row
			f.now = deadline.Add(9 * time.Second)
			f.cycle()
			require.Equal(t, row, f.line(q.DimensionError))
			require.Empty(t, f.store.Decisions)
			restartRecoveryTwice(t, f)
			f.now = deadline.Add(10 * time.Second)
			f.cycle()
			trial := f.line(q.DimensionError)
			require.True(t, trial.Trial)
			require.Zero(t, trial.Level)
			require.Equal(t, deadline, trial.Epoch)
			require.Equal(t, deadline.Add(q.TrialDuration), trial.TrialUntil)
			require.Equal(t, deadline, f.store.Decisions[0].At)
			restartRecoveryTwice(t, f)
		})
	}
}

func TestRecoveryLegacyTrialNoSamplesUsesOriginalDeadline(t *testing.T) {
	f := newQualityFixture(t)
	origin := f.now.Add(-10 * time.Second)
	deadline := origin.Add(2 * time.Hour)
	legacyRecoveryRow(f, true, origin, deadline)
	for _, offset := range []time.Duration{time.Hour, 2*time.Hour - time.Second, 2 * time.Hour} {
		f.now = origin.Add(offset + 10*time.Second)
		f.cycle()
		row := f.line(q.DimensionError)
		if offset < 2*time.Hour {
			require.True(t, row.Trial)
			require.Equal(t, deadline, row.TrialUntil)
			require.Equal(t, 4, row.Level)
			require.Empty(t, f.store.Decisions)
		} else {
			require.False(t, row.Trial)
			require.Zero(t, row.Level)
			require.Equal(t, deadline, row.Epoch)
			require.Len(t, f.store.Decisions, 1)
			require.Equal(t, "deadline", f.store.Decisions[0].Basis.ReleaseReason)
		}
		restartRecoveryTwice(t, f)
	}
}

func TestRecoveryLegacyDeadlineEvidenceAndLateInsertion(t *testing.T) {
	for _, trial := range []bool{false, true} {
		for _, late := range []bool{false, true} {
			t.Run(map[bool]string{true: "trial", false: "rear"}[trial]+map[bool]string{true: "/late", false: "/at-deadline"}[late], func(t *testing.T) {
				f := newQualityFixture(t)
				origin := f.now.Add(-10 * time.Second)
				deadline := origin.Add(2 * time.Hour)
				legacyRecoveryRow(f, trial, origin, deadline)
				rows := errorRecoverySamples(20, 20, deadline.Add(-time.Second))
				for i := range rows {
					rows[i].InsertedAt = deadline
					if late {
						rows[i].InsertedAt = deadline.Add(time.Second)
					}
				}
				f.ledger.Rows = rows
				f.now = deadline.Add(20 * time.Second)
				f.cycle()
				row := f.line(q.DimensionError)
				first := f.store.Decisions[0]
				require.Equal(t, deadline, first.At)
				require.Zero(t, row.Level)
				if !late {
					want := q.ActionRenew
					if trial {
						want = q.ActionTrialFail
						require.Equal(t, "fail", first.Basis.ReleaseReason)
					}
					require.Equal(t, want, first.Action)
					require.Equal(t, deadline, row.Entry)
					require.Equal(t, deadline.Add(q.RearDuration), row.Until)
				} else if trial {
					require.Equal(t, q.ActionTrialTimeout, first.Action)
					require.False(t, row.Trial)
					require.Equal(t, q.IdentityUnknown, row.Identity)
					require.Equal(t, deadline, row.Epoch)
				} else {
					require.Equal(t, q.ActionTrial, first.Action)
					require.True(t, row.Trial)
					require.Equal(t, deadline, row.Epoch)
				}
				require.Len(t, f.store.Decisions, 1, "evidence from the preceding stage must not fail the next stage")
				restartRecoveryTwice(t, f)
			})
		}
	}
}

func TestRecoveryMissingDeadlineRepairedOnce(t *testing.T) {
	for _, trial := range []bool{false, true} {
		t.Run(map[bool]string{true: "trial", false: "rear"}[trial], func(t *testing.T) {
			f := newQualityFixture(t)
			now := f.now.Add(-10 * time.Second)
			legacyRecoveryRow(f, trial, now.Add(-2*time.Hour), time.Time{})
			f.cycle()
			require.Len(t, f.store.Decisions, 1)
			require.True(t, f.store.Decisions[0].Basis.MissingDeadline)
			require.Equal(t, now, f.store.Decisions[0].At)
			if !trial {
				require.Equal(t, now.Add(q.TrialDuration), f.line(q.DimensionError).TrialUntil)
			}
			restartRecoveryTwice(t, f)
			f.now = f.now.Add(q.TrialDuration)
			f.cycle()
			require.False(t, f.line(q.DimensionError).Trial)
			require.NotEqual(t, q.IdentityRear, f.line(q.DimensionError).Identity)
			count := 0
			for _, d := range f.store.Decisions {
				if d.Basis.MissingDeadline {
					count++
				}
			}
			require.Equal(t, 1, count)
			restartRecoveryTwice(t, f)
		})
	}
}

// These corrupt rows cannot occur in a valid transaction. Preserve the existing
// watermark policy: replay may use evidence visible after the old deadline.
// Deployment must reject such rows rather than treating this as historical replay.
func TestRecoveryLegacyDeadlineBehindWatermark(t *testing.T) {
	for _, trial := range []bool{false, true} {
		for _, reset := range []bool{false, true} {
			t.Run(map[bool]string{true: "trial", false: "rear"}[trial]+map[bool]string{true: "/reset", false: "/evaluated"}[reset], func(t *testing.T) {
				f := newQualityFixture(t)
				deadline := f.now.Add(-10 * time.Second)
				row := legacyRecoveryRow(f, trial, deadline.Add(-time.Hour), deadline)
				watermark := deadline.Add(time.Minute)
				if reset {
					f.store.Resets = map[int64]time.Time{1: watermark}
				} else {
					row.EvaluatedAt = watermark
					f.store.Rows[0] = row
				}
				// The request starts after both watermarks. No failure was visible at deadline.
				seen := watermark.Add(20 * time.Second)
				f.ledger.Rows = errorRecoverySamples(20, 20, seen)
				horizon := seen.Add(10 * time.Second)
				f.now = horizon.Add(10 * time.Second)
				f.cycle()
				require.Len(t, f.store.Decisions, 1)
				action := q.ActionRenew
				at := horizon
				if trial {
					action = q.ActionTrialFail
					at = schedArrivalGrid(seen)
				}
				decision := f.store.Decisions[0]
				require.Equal(t, action, decision.Action)
				require.Equal(t, at, decision.At)
				require.True(t, decision.At.After(deadline))
				require.Equal(t, 20, decision.Basis.Error.Failed)
				require.False(t, decision.Basis.MissingDeadline)
				got := f.line(q.DimensionError)
				require.Equal(t, at, got.Entry)
				require.Equal(t, at.Add(q.RearDuration), got.Until)
				restartRecoveryTwice(t, f)
			})
		}
	}
}

func TestRecoveryLegacyNormalAndPendingRemainLocal(t *testing.T) {
	for _, pending := range []bool{false, true} {
		f := newQualityFixture(t)
		at := f.now.Add(-10 * time.Second)
		row := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionError},
			Level: 4, Admissions: 5, Epoch: at.Add(-time.Hour), EvaluatedAt: at.Add(-q.RegularInterval), Basis: &q.Basis{RuleVersion: "3.1"}}
		if pending {
			row.PendingAt = at.Add(-q.RegularInterval)
			f.ledger.Rows = errorRecoverySamples(20, 20, at.Add(-time.Second))
		}
		f.store.Rows = []q.LineState{row}
		f.cycle()
		got := f.line(q.DimensionError)
		if pending {
			require.Equal(t, q.IdentityRear, got.Identity)
			require.Zero(t, got.Level)
			require.Equal(t, at.Add(q.RearDuration), got.Until)
		} else {
			require.Equal(t, q.IdentityUnknown, got.Identity)
			require.Equal(t, 4, got.Level)
			require.Equal(t, row.Epoch, got.Epoch)
			require.Empty(t, f.store.Decisions)
		}
		restartRecoveryTwice(t, f)
	}
}

func TestRecoveryMembershipRebuildAndPolicyTiming(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "group-disable", false: "member-remove"}[disabled], func(t *testing.T) {
			f := newQualityFixture(t)
			at := f.now.Add(-10 * time.Second)
			row := legacyRecoveryRow(f, true, at.Add(-time.Minute), at.Add(2*time.Hour))
			leases := &engineLeaseProbe{}
			f.e.SetTrialLeases(leases)
			f.cycle()
			if disabled {
				policy := f.store.Policies[10].Policy
				policy.Enabled = false
				_, err := f.e.UpdatePolicy(t.Context(), 10, 0, policy)
				require.NoError(t, err)
			} else {
				f.store.Members[10] = nil
				f.e.configAt = time.Time{}
				f.cycle()
			}
			require.Empty(t, f.store.Rows)
			require.Equal(t, []schedQualityKey{{1, row.Model, 10}}, leases.released)
			if disabled {
				policy := f.store.Policies[10].Policy
				policy.Enabled = true
				_, err := f.e.UpdatePolicy(t.Context(), 10, f.store.Version, policy)
				require.NoError(t, err)
			} else {
				f.store.Members[10] = []int64{1}
				f.e.configAt = time.Time{}
			}
			f.now = f.now.Add(5 * time.Minute)
			f.ledger.Rows = errorRecoverySamples(1, 0, f.now.Add(-30*time.Second))
			f.cycle()
			got := f.line(q.DimensionError)
			require.Equal(t, q.IdentityUnknown, got.Identity)
			require.False(t, got.Trial)
			require.Zero(t, got.Level)
			require.Zero(t, got.Admissions)
			require.True(t, got.Until.IsZero() && got.TrialUntil.IsZero())
		})
	}
	for _, relax := range []bool{false, true} {
		t.Run(map[bool]string{true: "relax", false: "tighten"}[relax], func(t *testing.T) {
			f := newQualityFixture(t)
			at := f.now.Add(-10 * time.Second)
			old := legacyRecoveryRow(f, false, at.Add(-time.Hour), at.Add(100*time.Minute))
			f.store.Rows[0].Line = q.DimensionCache
			old.Line = q.DimensionCache
			f.cycle()
			policy := f.store.Policies[10].Policy
			policy.MinCacheRatio = .9
			if relax {
				policy.MinCacheRatio = .7
			}
			_, err := f.e.UpdatePolicy(t.Context(), 10, 0, policy)
			require.NoError(t, err)
			got := f.line(q.DimensionCache)
			if relax {
				require.Equal(t, q.IdentityUnknown, got.Identity)
				require.Zero(t, got.Level)
				require.Zero(t, got.Admissions)
				require.True(t, got.Until.IsZero())
			} else {
				require.Equal(t, old.Until, got.Until)
				require.Equal(t, old.Epoch, got.Epoch)
				require.Equal(t, old.Entry, got.Entry)
				require.Equal(t, old.Level, got.Level)
			}
		})
	}
}

//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// shareAdaptiveEnv is a share group of three accounts with load factor 100
// in priority 1 whose recent attempts replay the readiness review: 1.1%,
// 9.0% and 20.8% transient failures.
func shareAdaptiveEnv(t *testing.T) *shareAdminEnv {
	t.Helper()
	lf := func(n int) *int { return &n }
	env := newShareAdminEnv(t, Account{ID: 11, Priority: 1, LoadFactor: lf(100)}, Account{ID: 12, Priority: 1, LoadFactor: lf(100)},
		Account{ID: 13, Priority: 1, LoadFactor: lf(100)})
	_, err := env.save(t, 0, shareUpdate)
	require.NoError(t, err)
	transient := func(id, n int64) ShareSchedulingAttemptCount {
		return ShareSchedulingAttemptCount{ShareSchedulingUpstreamAttempt: ShareSchedulingUpstreamAttempt{AccountID: id, Kind: "failover",
			StatusCode: 502, Category: string(resilience.CategoryTransient)}, Count: n}
	}
	env.policies.attempts = ShareSchedulingAccountAttempts{
		Successes: map[int64]int64{11: 4318 - 48, 12: 2070 - 187, 13: 900 - 187},
		Failures: []ShareSchedulingAttemptCount{transient(11, 48), transient(12, 187), transient(13, 187),
			// A balance failure is handled by the balance pause, not here.
			{ShareSchedulingUpstreamAttempt: ShareSchedulingUpstreamAttempt{AccountID: 13, Kind: "failover", StatusCode: 402,
				Category: string(resilience.CategoryBalance)}, Count: 500}},
	}
	return env
}

func (e *shareAdminEnv) weight(t *testing.T, id int64, now time.Time) shareWeightBreakdown {
	t.Helper()
	return e.share.shareWeight(shareTestGroupID, accountIn(e.repo.accounts, id), now)
}

// TestShareHealthDownweightsUnstableAccounts is R1 end to end: the refresh
// reads one window for every member and the placement follows the
// down-weighted shares (design §11.1).
func TestShareHealthDownweightsUnstableAccounts(t *testing.T) {
	env := shareAdaptiveEnv(t)
	now := time.Date(2026, 10, 20, 12, 7, 42, 0, time.UTC)
	env.share.refreshHealth(context.Background(), now)
	require.Equal(t, 1, env.policies.attemptCalls)
	to := time.Date(2026, 10, 20, 12, 7, 0, 0, time.UTC)
	require.Equal(t, [2]time.Time{to.Add(-30 * time.Minute), to}, env.policies.attemptWindow, "a 30-minute window aligned to the minute")

	for id, want := range map[int64]float64{11: 100, 12: 50, 13: 20} {
		w := env.weight(t, id, now)
		require.InDelta(t, want, w.Effective, 1e-9, "account %d", id)
		require.Equal(t, 100, w.LoadFactor)
		require.Equal(t, 1.0, w.Ramp.Factor)
	}
	h := env.weight(t, 13, now).Health
	require.Equal(t, shareHealthStats{Attempts: 900, SoftFailures: 187}, h.Stats, "the balance failures are not counted")

	// Candidates are weighed at the current time.
	env.share.refreshHealth(context.Background(), time.Now())
	gid := shareTestGroupID
	_, in, err := env.gateway.shareGatewayCandidates(context.Background(), nil, &gid, "", nil, 0)
	require.NoError(t, err)
	counts := map[int64]int{}
	const sessions = 34000
	for i := range sessions {
		counts[sharesched.Plan(sharesched.Request{GroupID: gid, Key: fmt.Sprintf("h-%d", i)}, in.cands).Target]++
	}
	for id, want := range map[int64]float64{11: 100.0 / 170, 12: 50.0 / 170, 13: 20.0 / 170} {
		require.InDelta(t, want, float64(counts[id])/sessions, 0.01, "account %d", id)
	}
}

func TestShareHealthMemberViewExplainAndPreview(t *testing.T) {
	env := shareAdaptiveEnv(t)
	env.share.refreshHealth(context.Background(), time.Now())

	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	m := env.member(t, view.Members, 13)
	require.InDelta(t, 0.2, *m.HealthFactor, 1e-9)
	require.Equal(t, 30, m.Health.WindowMinutes)
	require.Equal(t, int64(900), m.Health.Attempts)
	require.Equal(t, int64(187), m.Health.SoftFailures)
	require.InDelta(t, 187.0/900, *m.Health.Rate, 1e-9)
	require.InDelta(t, 48.0/4318, m.Health.TierBaselineRate, 1e-9)
	require.False(t, m.Health.Insufficient)
	require.Equal(t, 1.0, m.RampFactor)
	require.Nil(t, m.Ramp)
	require.InDelta(t, 20, m.EffectiveWeight, 1e-9)
	require.InDelta(t, 1.0/3, m.BaseShare, 1e-9, "the base share weighs load factors only")
	require.InDelta(t, 20.0/170, m.ExpectedShare, 1e-9)

	explained, err := env.share.Explain(context.Background(), shareTestGroupID, ShareSchedulingExplainInput{})
	require.NoError(t, err)
	for _, e := range explained.Entries {
		if e.AccountID == 12 {
			require.InDelta(t, 0.5, *e.HealthFactor, 1e-9)
			require.Equal(t, 1.0, e.RampFactor)
			require.InDelta(t, 50, e.EffectiveWeight, 1e-9)
			require.Equal(t, 100, e.LoadFactor)
		}
	}

	preview, err := env.share.Preview(context.Background(), shareTestGroupID, shareUpdate)
	require.NoError(t, err)
	downweighted := map[int64]float64{}
	for _, w := range preview.Warnings {
		if w.Code == ShareWarningHealthDownweighted {
			downweighted[*w.AccountID] = *w.Value
		}
	}
	require.Equal(t, map[int64]float64{12: 0.5, 13: 0.2}, downweighted)
}

func TestShareHealthInsufficientDataIsNotDownweighted(t *testing.T) {
	env := shareAdaptiveEnv(t)
	delete(env.policies.attempts.Successes, 12)
	env.policies.attempts.Failures = env.policies.attempts.Failures[:1]
	env.share.refreshHealth(context.Background(), time.Now())
	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	m := env.member(t, view.Members, 12)
	require.True(t, m.Health.Insufficient)
	require.Nil(t, m.Health.Rate)
	require.Nil(t, m.HealthFactor)
	require.InDelta(t, 100, m.EffectiveWeight, 1e-9)
}

// TestShareHealthFallsBackToLoadFactors: whenever R1 cannot be trusted the
// weight is the load factor and the views show no health.
func TestShareHealthFallsBackToLoadFactors(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	plain := func(t *testing.T, env *shareAdminEnv, at time.Time) {
		t.Helper()
		w := env.weight(t, 13, at)
		require.Nil(t, w.Health)
		require.InDelta(t, 100, w.Effective, 1e-9)
	}

	t.Run("stale", func(t *testing.T) {
		env := shareAdaptiveEnv(t)
		env.share.refreshHealth(ctx, now.Add(-6*time.Minute))
		plain(t, env, now)
		view, err := env.share.GetPolicy(ctx, shareTestGroupID)
		require.NoError(t, err)
		require.Nil(t, env.member(t, view.Members, 13).Health)
		require.Nil(t, env.member(t, view.Members, 13).HealthFactor)
	})
	t.Run("query failed", func(t *testing.T) {
		env := shareAdaptiveEnv(t)
		env.policies.attemptsErr = fmt.Errorf("statement timeout")
		env.share.refreshHealth(ctx, now)
		plain(t, env, now)
	})
	t.Run("ops monitoring off", func(t *testing.T) {
		env := shareAdaptiveEnv(t)
		env.settings.values[SettingKeyOpsMonitoringEnabled] = "false"
		env.share.refreshHealth(ctx, now)
		require.Zero(t, env.policies.attemptCalls)
		plain(t, env, now)
	})
	t.Run("ops disabled in config", func(t *testing.T) {
		env := shareAdaptiveEnv(t)
		env.share.opsEnabled = false
		env.share.refreshHealth(ctx, now)
		require.Zero(t, env.policies.attemptCalls)
		plain(t, env, now)
	})
	t.Run("resilience off", func(t *testing.T) {
		env := shareAdaptiveEnv(t)
		env.share.refreshHealth(ctx, now)
		orig := *resilience.Current()
		off := resilience.Defaults()
		off.Enabled = false
		resilience.Publish(off)
		t.Cleanup(func() { resilience.Publish(orig) })
		plain(t, env, now)
		env.share.refreshHealth(ctx, now)
		require.Nil(t, env.share.health.Load())
	})
	t.Run("no share group", func(t *testing.T) {
		env := shareAdaptiveEnv(t)
		env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{}, groups: env.share.state.Load().groups})
		env.share.refreshHealth(ctx, now)
		require.Zero(t, env.policies.attemptCalls, "nothing is read while no group runs share scheduling")
		require.Nil(t, env.share.health.Load())
	})
}

// TestShareHealthNeverMovesBoundSessions: the factors only shape new
// placements; a warm session stays on its down-weighted account.
func TestShareHealthNeverMovesBoundSessions(t *testing.T) {
	env := shareAdaptiveEnv(t)
	env.share.refreshHealth(context.Background(), time.Now())
	env.cache.bind("bound", 13, time.Hour-30*time.Second)
	result := env.selectAccount(t, cacheableContext(), "bound", nil)
	require.Equal(t, int64(13), result.Account.ID)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonAffinityHit)))
}

// TestShareHealthInstancesAgree: two instances reading the same minute
// compute the same weights.
func TestShareHealthInstancesAgree(t *testing.T) {
	a, b := shareAdaptiveEnv(t), shareAdaptiveEnv(t)
	now := time.Date(2026, 10, 20, 12, 7, 42, 0, time.UTC)
	a.share.refreshHealth(context.Background(), now)
	b.share.refreshHealth(context.Background(), now.Add(15*time.Second))
	for _, id := range []int64{11, 12, 13} {
		require.Equal(t, a.weight(t, id, now.Add(20*time.Second)).Effective, b.weight(t, id, now.Add(20*time.Second)).Effective)
	}
}

// TestShareRampAfterObservedOutage is R2 for outages the account row does
// not show (disabled, error, scheduling off): the refresh observes them.
func TestShareRampAfterObservedOutage(t *testing.T) {
	env := shareAdaptiveEnv(t)
	env.policies.attempts = ShareSchedulingAccountAttempts{}
	ctx := context.Background()
	t0 := time.Now().Add(-10 * time.Minute)
	accountIn(env.repo.accounts, 12).Schedulable = false
	env.share.refreshHealth(ctx, t0)
	env.share.refreshHealth(ctx, t0.Add(3*time.Minute))
	accountIn(env.repo.accounts, 12).Schedulable = true
	recovered := t0.Add(6 * time.Minute)
	env.share.refreshHealth(ctx, recovered)

	w := env.weight(t, 12, recovered.Add(90*time.Second))
	require.Equal(t, ShareRampReenabled, w.Ramp.Cause)
	require.True(t, w.Ramp.Since.Equal(recovered))
	require.InDelta(t, 0.25, w.Ramp.Factor, 1e-9)
	require.InDelta(t, 25, w.Effective, 1e-9)
	require.InDelta(t, 100, env.weight(t, 12, recovered.Add(shareRampDuration)).Effective, 1e-9, "full weight after ten minutes")

	view, err := env.share.GetPolicy(ctx, shareTestGroupID)
	require.NoError(t, err)
	m := env.member(t, view.Members, 12)
	require.NotNil(t, m.Ramp)
	require.Equal(t, ShareRampReenabled, m.Ramp.Cause)
	require.True(t, m.Ramp.FullAt.Equal(recovered.Add(shareRampDuration)))
	preview, err := env.share.Preview(ctx, shareTestGroupID, shareUpdate)
	require.NoError(t, err)
	var ramping []int64
	for _, warning := range preview.Warnings {
		if warning.Code == ShareWarningRamping {
			ramping = append(ramping, *warning.AccountID)
		}
	}
	require.Equal(t, []int64{12}, ramping)

	// A short outage does not ramp.
	accountIn(env.repo.accounts, 11).Schedulable = false
	env.share.refreshHealth(ctx, recovered.Add(time.Minute))
	accountIn(env.repo.accounts, 11).Schedulable = true
	env.share.refreshHealth(ctx, recovered.Add(3*time.Minute))
	require.Empty(t, env.weight(t, 11, recovered.Add(3*time.Minute)).Ramp.Cause)

	// A new process has no memory of the outage and does not ramp.
	fresh := shareAdaptiveEnv(t)
	fresh.share.refreshHealth(ctx, recovered)
	require.Empty(t, fresh.weight(t, 12, recovered.Add(time.Minute)).Ramp.Cause)
}

// TestShareRampAfterBalancePause: an ended balance pause ramps on every
// instance from the account row; with resilience off nothing ramps.
func TestShareRampAfterBalancePause(t *testing.T) {
	env := shareAdaptiveEnv(t)
	now := time.Now()
	ended := now.Add(-3 * time.Minute)
	acc := accountIn(env.repo.accounts, 11)
	acc.TempUnschedulableUntil, acc.TempUnschedulableReason = &ended, resilience.ReasonPrefixBalanceExhausted+": HTTP 402"
	w := env.weight(t, 11, now)
	require.Equal(t, ShareRampBalancePause, w.Ramp.Cause)
	require.InDelta(t, 35, w.Effective, 1e-9)

	orig := *resilience.Current()
	off := resilience.Defaults()
	off.Enabled = false
	resilience.Publish(off)
	t.Cleanup(func() { resilience.Publish(orig) })
	require.InDelta(t, 100, env.weight(t, 11, now).Effective, 1e-9)
}

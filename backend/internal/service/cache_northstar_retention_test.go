//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type northstarRetentionRepo struct {
	northstarTestRepo
	cutoffs [][2]time.Time
}

func (r *northstarRetentionRepo) CleanupCacheNorthstar(_ context.Context, rollup, trace time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "cleanup")
	r.cutoffs = append(r.cutoffs, [2]time.Time{rollup, trace})
	return r.fail
}
func (r *northstarRetentionRepo) snapshot() ([]string, [][2]time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.calls...), append([][2]time.Time{}, r.cutoffs...)
}
func (r *northstarRetentionRepo) setFail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}

func northstarRetentionPolicies(t *testing.T, kill bool, writes ...[2]string) *CachePolicyService {
	t.Helper()
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = kill
	s := NewCachePolicyService(newPolicyTestStore(), cfg, WithCacheNorthstar())
	for _, w := range writes {
		_, _, err := s.Update(context.Background(), w[0], policyWrite(t, w[1]), nil, CachePolicyActor{})
		require.NoError(t, err)
	}
	return s
}

func TestCacheNorthstarRetentionDecoupledFromSwitches(t *testing.T) {
	ctx := context.Background()
	cases := map[string]*CachePolicyService{
		"northstar_disabled": northstarRetentionPolicies(t, false,
			[2]string{"northstar", `{"expected_rev":0,"spec":{"rollup_enabled":true}}`},
			[2]string{"northstar", `{"expected_rev":1,"spec":{"rollup_enabled":false}}`}),
		"observability_disabled": northstarRetentionPolicies(t, false,
			[2]string{"observability", `{"expected_rev":0,"spec":{"enabled":true}}`},
			[2]string{"observability", `{"expected_rev":1,"spec":{"enabled":false}}`}),
		"persist_disabled": northstarRetentionPolicies(t, false,
			[2]string{"observability", `{"expected_rev":0,"spec":{"enabled":true,"persist_trace_hourly":false}}`}),
		"kill_switch": northstarRetentionPolicies(t, true,
			[2]string{"northstar", `{"expected_rev":0,"spec":{"rollup_enabled":true}}`}),
		"enabled": northstarRetentionPolicies(t, false,
			[2]string{"northstar", `{"expected_rev":0,"spec":{"rollup_enabled":true}}`},
			[2]string{"observability", `{"expected_rev":0,"spec":{"enabled":true}}`}),
	}
	require.False(t, cases["northstar_disabled"].Snapshot().NorthstarEnabled())
	require.False(t, cases["observability_disabled"].Snapshot().PersistTraceHourlyEnabled())
	require.False(t, cases["kill_switch"].Snapshot().NorthstarEnabled())
	require.True(t, cases["enabled"].Snapshot().NorthstarEnabled() && cases["enabled"].Snapshot().PersistTraceHourlyEnabled())
	for name, policies := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &northstarRetentionRepo{}
			s := NewCacheNorthstarAggregator(repo, policies, NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
			now := time.Date(2026, 10, 25, 4, 30, 0, 0, time.UTC)
			require.NoError(t, s.Cleanup(ctx, now))
			calls, cutoffs := repo.snapshot()
			require.Equal(t, []string{"cleanup"}, calls)
			// Strictly-before cutoffs: rollups, hour markers and events 180 days, trace hours 90 days.
			require.Equal(t, [][2]time.Time{{time.Date(2026, 4, 28, 4, 30, 0, 0, time.UTC), time.Date(2026, 7, 27, 4, 30, 0, 0, time.UTC)}}, cutoffs)

			// At most one attempt per hour, whatever its outcome.
			require.NoError(t, s.Cleanup(ctx, now.Add(59*time.Minute)))
			calls, _ = repo.snapshot()
			require.Len(t, calls, 1)
			failure := errors.New("synthetic retention failure")
			repo.setFail(failure)
			require.ErrorIs(t, s.Cleanup(ctx, now.Add(time.Hour)), failure)
			repo.setFail(nil)
			require.NoError(t, s.Cleanup(ctx, now.Add(time.Hour+time.Minute)))
			calls, _ = repo.snapshot()
			require.Len(t, calls, 2)
			require.NoError(t, s.Cleanup(ctx, now.Add(2*time.Hour)))
			calls, _ = repo.snapshot()
			require.Len(t, calls, 3)
		})
	}
}

func TestCacheNorthstarRetentionNeverConfiguredDoesNoIO(t *testing.T) {
	repo := &northstarRetentionRepo{}
	repo.fail = errors.New("storage must not run")
	s := NewCacheNorthstarAggregator(repo, northstarTestPolicies(t, false), NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
	require.NoError(t, s.Cleanup(context.Background(), time.Now().UTC()))
	locks := &dashboardAggregationLeaderLockRecordingCache{delegate: &fakeLeaderLockCache{}}
	dashboard := &DashboardAggregationService{repo: &dashboardAggregationRepoTestStub{}, cacheNorthstar: s, lockCache: locks, instanceID: "self"}
	dashboard.runStandaloneCacheNorthstar()
	dashboard.runScheduledCacheNorthstar()
	calls, _ := repo.snapshot()
	require.Empty(t, calls)
	require.Empty(t, locks.acquireKeys, "default-off schedulers return before leader election")
	require.Zero(t, s.retentionAt.Load())

	var missing *CacheNorthstarAggregator
	require.NoError(t, missing.Cleanup(context.Background(), time.Now().UTC()))
	(&DashboardAggregationService{}).runStandaloneCacheNorthstar()
	(&DashboardAggregationService{}).runScheduledCacheNorthstar()
}

func TestCacheNorthstarRetentionCanceledContextDoesNotConsumeHour(t *testing.T) {
	repo := &northstarRetentionRepo{}
	s := NewCacheNorthstarAggregator(repo, northstarRetentionPolicies(t, false, [2]string{"northstar", `{"expected_rev":0,"spec":{"rollup_enabled":true}}`}), NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Now().UTC()
	require.NoError(t, s.Cleanup(ctx, now))
	calls, _ := repo.snapshot()
	require.Empty(t, calls)
	require.NoError(t, s.Cleanup(context.Background(), now))
	calls, _ = repo.snapshot()
	require.Equal(t, []string{"cleanup"}, calls)
}

func TestCacheNorthstarRetentionLeaderOnly(t *testing.T) {
	policies := northstarRetentionPolicies(t, false,
		[2]string{"northstar", `{"expected_rev":0,"spec":{"rollup_enabled":true}}`},
		[2]string{"northstar", `{"expected_rev":1,"spec":{"rollup_enabled":false}}`})
	for _, standalone := range []bool{true, false} {
		repo := &northstarRetentionRepo{}
		s := NewCacheNorthstarAggregator(repo, policies, NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
		peer := &fakeLeaderLockCache{}
		ok, err := peer.TryAcquireLeaderLock(context.Background(), dashboardAggregationLeaderLockKey, "peer", time.Hour)
		require.NoError(t, err)
		require.True(t, ok)
		dashboard := &DashboardAggregationService{
			repo:           &dashboardAggregationRepoTestStub{},
			cacheNorthstar: s,
			lockCache:      peer,
			instanceID:     "self",
			cfg:            config.DashboardAggregationConfig{Enabled: !standalone, IntervalSeconds: 60},
		}
		run := dashboard.runStandaloneCacheNorthstar
		if !standalone {
			run = dashboard.runScheduledAggregation
		}
		run()
		calls, _ := repo.snapshot()
		require.Empty(t, calls, "a peer holds the leader lease")

		require.NoError(t, peer.ReleaseLeaderLock(context.Background(), dashboardAggregationLeaderLockKey, "peer"))
		run()
		calls, cutoffs := repo.snapshot()
		require.Equal(t, []string{"cleanup"}, calls)
		require.Len(t, cutoffs, 1)
		run()
		calls, _ = repo.snapshot()
		require.Len(t, calls, 1, "hourly")
	}
}

func TestCacheNorthstarRetentionLeavesDashboardRetention(t *testing.T) {
	repo := &northstarRetentionRepo{}
	s := NewCacheNorthstarAggregator(repo, northstarRetentionPolicies(t, false, [2]string{"northstar", `{"expected_rev":0,"spec":{"rollup_enabled":true}}`}), NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
	dashboardRepo := &dashboardAggregationRepoTestStub{}
	dashboard := &DashboardAggregationService{
		repo:           dashboardRepo,
		cacheNorthstar: s,
		cfg: config.DashboardAggregationConfig{
			Enabled:   true,
			Retention: config.DashboardAggregationRetentionConfig{UsageLogsDays: 1, HourlyDays: 1, DailyDays: 1},
		},
	}
	dashboardRepo.cleanupAggregatesErr = errors.New("dashboard cleanup failed")
	for i := 0; i < 3; i++ {
		// Dashboard retention retries every cycle after failures; northstar
		// retention no longer follows it.
		dashboard.maybeCleanupRetention(context.Background(), time.Now().UTC())
	}
	require.Equal(t, 3, dashboardRepo.cleanupUsageCalls)
	calls, _ := repo.snapshot()
	require.Empty(t, calls)
}

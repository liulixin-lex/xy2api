package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// CacheNorthstarAggregator is called under the existing dashboard leader lease.
// It has no scheduler of its own and never writes dashboard or usage tables.
type CacheNorthstarAggregator struct {
	repo            CacheNorthstarRepository
	policies        *CachePolicyService
	counters        *CacheTraceCounters
	build           CachePanelBuildInfo
	mu              sync.Mutex
	startupRecorded bool
	traceHour       time.Time
}

func NewCacheNorthstarAggregator(repo CacheNorthstarRepository, policies *CachePolicyService, counters *CacheTraceCounters, build CachePanelBuildInfo) *CacheNorthstarAggregator {
	return &CacheNorthstarAggregator{repo: repo, policies: policies, counters: counters, build: build}
}
func (s *CacheNorthstarAggregator) Enabled() bool {
	if s == nil {
		return false
	}
	p := s.policies.Snapshot()
	return p.NorthstarRollupEnabled() || p.PersistTraceHourlyEnabled()
}
func (s *CacheNorthstarAggregator) Tick(ctx context.Context, now time.Time) error {
	if !s.Enabled() {
		return nil
	}
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	p := s.policies.Snapshot()
	if !p.NorthstarRollupEnabled() && !p.PersistTraceHourlyEnabled() {
		return nil
	}
	if !s.startupRecorded {
		if err := s.repo.RecordCachePanelStart(ctx, s.build.Version, s.counters.started, s.counters.processID, p.kill); err != nil {
			return err
		}
		s.startupRecorded = true
	}
	end := now.UTC().Truncate(time.Hour)
	if p.PersistTraceHourlyEnabled() && !s.traceHour.Equal(end) {
		if err := s.repo.SaveCacheTraceHours(ctx, s.counters.PreviousHour(now)); err != nil {
			return err
		}
		s.traceHour = end
	}
	if !p.NorthstarRollupEnabled() {
		return nil
	}
	policy := p.NorthstarPolicy()
	state, err := s.repo.GetCacheNorthstarState(ctx)
	if err != nil {
		return err
	}
	if state == nil || state.PolicyVersion != policy.PolicyVersion {
		if err = s.repo.AggregateCacheNorthstar(ctx, end.Add(-time.Hour), end, policy, now, false); err != nil {
			return err
		}
		state, err = s.repo.GetCacheNorthstarState(ctx)
		if err != nil {
			return err
		}
	}
	if state == nil {
		return nil
	}
	// At most three hours in an ordinary refresh, including one lookback hour.
	start := state.AggregatedUntil.Add(-time.Hour)
	if start.After(end.Add(-time.Hour)) {
		start = end.Add(-time.Hour)
	}
	catchupEnd := start.Add(3 * time.Hour)
	if catchupEnd.After(end) {
		catchupEnd = end
	}
	if start.Before(catchupEnd) {
		if err = s.repo.AggregateCacheNorthstar(ctx, start, catchupEnd, policy, now, false); err != nil {
			return err
		}
		state, err = s.repo.GetCacheNorthstarState(ctx)
		if err != nil {
			return err
		}
	}
	if state == nil {
		return nil
	}
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	previousDay := day.Add(-24 * time.Hour)
	if !now.Before(day.Add(30*time.Minute)) && !state.BackfilledFrom.After(day) && !state.AggregatedUntil.Before(day) && (state.ReconciledDay == nil || state.ReconciledDay.Before(previousDay)) {
		// One transaction for all 24 hour replacements, including predecessor replay.
		if err = s.repo.AggregateCacheNorthstar(ctx, previousDay, day, policy, now, true); err != nil {
			return err
		}
		state, err = s.repo.GetCacheNorthstarState(ctx)
		if err != nil {
			return err
		}
	}
	if state == nil {
		return nil
	}
	target := end.Add(-time.Duration(policy.BackfillDays) * 24 * time.Hour)
	// Small backward blocks preserve a usable recent hour during initial backfill.
	for i := 0; i < 2 && policy.BackfillDays > 0 && state.BackfilledFrom.After(target); i++ {
		if !s.policies.Snapshot().NorthstarRollupEnabled() {
			return nil
		}
		stop := state.BackfilledFrom
		start := stop.Add(-time.Hour)
		if err = s.repo.AggregateCacheNorthstar(ctx, start, stop, policy, now, false); err != nil {
			return err
		}
		state.BackfilledFrom = start
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
func (s *CacheNorthstarAggregator) Cleanup(ctx context.Context, now time.Time) error {
	if !s.Enabled() {
		return nil
	}
	p := s.policies.Snapshot()
	var rollup, trace time.Time
	if p.NorthstarRollupEnabled() {
		rollup = now.AddDate(0, 0, -180)
	}
	if p.PersistTraceHourlyEnabled() {
		trace = now.AddDate(0, 0, -90)
	}
	return s.repo.CleanupCacheNorthstar(ctx, rollup, trace)
}

func (s *DashboardAggregationService) runScheduledCacheNorthstar() {
	if s.cacheNorthstar == nil || !s.cacheNorthstar.Enabled() {
		return
	}
	// Independent from the dashboard's deadline and outcome, within the same
	// existing five-minute leader lease (40s + the two existing two-minute stages).
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := s.cacheNorthstar.Tick(ctx, time.Now().UTC()); err != nil {
		slog.Warn("cache northstar aggregation failed", "error", err)
	}
}
func (s *DashboardAggregationService) runStandaloneCacheNorthstar() {
	if s.cacheNorthstar == nil || !s.cacheNorthstar.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, dashboardAggregationLeaderLockKey, s.instanceID, dashboardAggregationLeaderLockTTL)
	if !ok {
		return
	}
	defer release()
	if err := s.cacheNorthstar.Tick(ctx, time.Now().UTC()); err != nil {
		slog.Warn("cache northstar aggregation failed", "error", err)
	}
}

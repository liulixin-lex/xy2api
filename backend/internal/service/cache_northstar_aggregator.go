package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	cacheNorthstarRetentionDays   = 180
	cacheTraceHourlyRetentionDays = 90
	cacheNorthstarRetentionEvery  = time.Hour
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
	retentionAt     atomic.Int64 // Unix seconds of the last retention attempt.
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

// retentionDue is lock-free so schedulers return before any I/O. Retention does
// not depend on rollup, persistence, observability or the kill switch; nothing
// can be stored before observability or northstar is first saved (rev > 0).
func (s *CacheNorthstarAggregator) retentionDue(now time.Time) bool {
	if s == nil {
		return false
	}
	p := s.policies.Snapshot()
	if p.ObservabilityRevision() == 0 && p.NorthstarRevision() == 0 {
		return false
	}
	last := s.retentionAt.Load()
	return last == 0 || now.Unix()-last >= int64(cacheNorthstarRetentionEvery/time.Second)
}

// Cleanup is attempted at most once per hour by the leader, whatever its
// outcome: rollups, hour markers and events keep 180 days, trace hours 90 days.
func (s *CacheNorthstarAggregator) Cleanup(ctx context.Context, now time.Time) error {
	if !s.retentionDue(now) || ctx.Err() != nil {
		return nil
	}
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	if !s.retentionDue(now) {
		return nil
	}
	s.retentionAt.Store(now.Unix())
	return s.repo.CleanupCacheNorthstar(ctx, now.AddDate(0, 0, -cacheNorthstarRetentionDays), now.AddDate(0, 0, -cacheTraceHourlyRetentionDays))
}

// due reports whether a leader cycle has any work, without I/O.
func (s *CacheNorthstarAggregator) due(now time.Time) (enabled, ok bool) {
	if s == nil {
		return false, false
	}
	enabled = s.Enabled()
	return enabled, enabled || s.retentionDue(now)
}
func (s *CacheNorthstarAggregator) runLeaderCycle(ctx context.Context, now time.Time, enabled bool) {
	if enabled {
		if err := s.Tick(ctx, now); err != nil {
			slog.Warn("cache northstar aggregation failed", "error", err)
		}
	}
	if err := s.Cleanup(ctx, now); err != nil {
		slog.Warn("cache northstar retention failed", "error", err)
	}
}

func (s *DashboardAggregationService) runScheduledCacheNorthstar() {
	now := time.Now().UTC()
	enabled, ok := s.cacheNorthstar.due(now)
	if !ok {
		return
	}
	// Independent from the dashboard's deadline and outcome, within the same
	// existing five-minute leader lease (40s + the two existing two-minute stages).
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s.cacheNorthstar.runLeaderCycle(ctx, now, enabled)
}
func (s *DashboardAggregationService) runStandaloneCacheNorthstar() {
	now := time.Now().UTC()
	enabled, ok := s.cacheNorthstar.due(now)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, dashboardAggregationLeaderLockKey, s.instanceID, dashboardAggregationLeaderLockTTL)
	if !ok {
		return
	}
	defer release()
	s.cacheNorthstar.runLeaderCycle(ctx, now, enabled)
}

//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/config"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type northstarTestRepo struct {
	CacheNorthstarRepository
	mu        sync.Mutex
	state     *CacheNorthstarState
	data      CacheNorthstarData
	calls     []string
	ranges    []CachePanelWindow
	reconcile []bool
	fail      error
}

func (r *northstarTestRepo) GetCacheNorthstarState(context.Context) (*CacheNorthstarState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "state")
	if r.fail != nil {
		return nil, r.fail
	}
	if r.state == nil {
		return nil, nil
	}
	copy := *r.state
	return &copy, nil
}
func (r *northstarTestRepo) AggregateCacheNorthstar(_ context.Context, start, end time.Time, p CacheNorthstarPolicy, _ time.Time, reconcile bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "aggregate")
	r.ranges = append(r.ranges, CachePanelWindow{Start: start, End: end})
	r.reconcile = append(r.reconcile, reconcile)
	if r.fail != nil {
		return r.fail
	}
	if r.state == nil || r.state.PolicyVersion != p.PolicyVersion {
		r.state = &CacheNorthstarState{PolicyVersion: p.PolicyVersion, Policy: p, PolicyChangedAt: time.Unix(0, 0), AggregatedUntil: end, BackfilledFrom: start}
	}
	if end.After(r.state.AggregatedUntil) {
		r.state.AggregatedUntil = end
	}
	if !end.Before(r.state.BackfilledFrom) && start.Before(r.state.BackfilledFrom) {
		r.state.BackfilledFrom = start
	}
	if reconcile {
		r.state.ReconciledDay = &start
	}
	return nil
}
func (r *northstarTestRepo) ReadCacheNorthstar(context.Context, string, time.Time, time.Time, string) (CacheNorthstarData, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "read")
	return r.data, r.fail
}
func (r *northstarTestRepo) CleanupCacheNorthstar(context.Context, time.Time, time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "cleanup")
	return r.fail
}
func (r *northstarTestRepo) SaveCacheTraceHours(context.Context, []CacheTraceHour) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "trace")
	return r.fail
}
func (r *northstarTestRepo) RecordCachePanelStart(context.Context, string, time.Time, string, bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "startup")
	return r.fail
}
func (r *northstarTestRepo) ListCachePanelEvents(context.Context, time.Time, time.Time) ([]CachePanelEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "events")
	return []CachePanelEvent{}, r.fail
}

func northstarTestPolicies(t *testing.T, enabled bool) *CachePolicyService {
	t.Helper()
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCacheNorthstar())
	if enabled {
		_, _, err := s.Update(context.Background(), "northstar", policyWrite(t, `{"expected_rev":0,"spec":{"rollup_enabled":true,"backfill_days":1}}`), nil, CachePolicyActor{})
		require.NoError(t, err)
	}
	return s
}

func TestCacheNorthstarDisabledDoesNoStorage(t *testing.T) {
	repo := &northstarTestRepo{fail: errors.New("storage must not run")}
	s := NewCacheNorthstarAggregator(repo, northstarTestPolicies(t, false), NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
	require.NoError(t, s.Tick(context.Background(), time.Now()))
	require.NoError(t, s.Cleanup(context.Background(), time.Now()))
	require.Empty(t, repo.calls)
}

func TestCacheNorthstarBackfillResumesAndDailyRangeIsAtomic(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 30, 0, 0, time.UTC)
	repo := &northstarTestRepo{}
	policies := northstarTestPolicies(t, true)
	first := NewCacheNorthstarAggregator(repo, policies, NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
	require.NoError(t, first.Tick(context.Background(), now))
	cursor := repo.state.BackfilledFrom
	require.Equal(t, now.Truncate(time.Hour), repo.state.AggregatedUntil)
	require.True(t, cursor.Before(now.Add(-time.Hour)))
	resumed := NewCacheNorthstarAggregator(repo, policies, NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
	require.NoError(t, resumed.Tick(context.Background(), now))
	require.Equal(t, cursor.Add(-2*time.Hour), repo.state.BackfilledFrom)
	day := time.Date(2026, 10, 24, 0, 30, 0, 0, time.UTC)
	repo.state.AggregatedUntil = day.Truncate(time.Hour)
	repo.state.BackfilledFrom = day.Add(-7 * 24 * time.Hour)
	require.NoError(t, resumed.Tick(context.Background(), day))
	count := 0
	for i, reconcile := range repo.reconcile {
		if reconcile {
			count++
			require.Equal(t, 24*time.Hour, repo.ranges[i].End.Sub(repo.ranges[i].Start))
			require.Equal(t, time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), repo.ranges[i].Start)
		}
	}
	require.Equal(t, 1, count)
	require.NoError(t, resumed.Tick(context.Background(), day))
	count = 0
	for _, reconcile := range repo.reconcile {
		if reconcile {
			count++
		}
	}
	require.Equal(t, 1, count, "do not repeat D-1 after its committed marker")
}

func TestCacheNorthstarQueriesOnlyReadAndCacheByPolicyRevision(t *testing.T) {
	policies := northstarTestPolicies(t, false)
	now := time.Date(2026, 10, 23, 12, 30, 0, 0, time.UTC)
	end := now.Truncate(time.Hour)
	repo := &northstarTestRepo{state: &CacheNorthstarState{PolicyVersion: "2026-10-22-v1", PolicyChangedAt: time.Unix(0, 0), AggregatedUntil: end, BackfilledFrom: end.Add(-48 * time.Hour)}}
	for hour := end.Add(-48 * time.Hour); hour.Before(end); hour = hour.Add(time.Hour) {
		repo.data.Hours = append(repo.data.Hours, CacheNorthstarHour{BucketStart: hour})
	}
	repo.data.Rows = []CacheNorthstarRow{
		{BucketStart: end.Add(-time.Hour), Platform: "openai", Model: "gpt-5.4", GroupID: 1, GroupLabel: "group-one", ThresholdBand: 2048, CacheNorthstarTotals: CacheNorthstarTotals{Requests: 12, RequestsHit: 6, PromptTokens: 3000, ReadTokens: 2000, InputTokens: 1000, InputCostMicros: 10000, ReadCostMicros: 2000, OpportunityTokens: 500}},
		{BucketStart: end.Add(-time.Hour), Platform: "grok", Model: "unknown", GroupID: 2, GroupLabel: "group-two", ThresholdBand: 2048, CacheNorthstarTotals: CacheNorthstarTotals{Requests: 2, PromptTokens: 3000, InputTokens: 3000, OpportunityTokens: 200}},
	}
	s := NewCacheObservabilityService(repo, policies, NewCacheTraceCounters(), CachePanelBuildInfo{Version: "test"})
	s.now = func() time.Time { return now }
	q := CachePanelQuery{Window: "24h", Granularity: "1h", Platform: "all", CostView: "list"}
	value, err := s.Query(context.Background(), "northstar", q)
	require.NoError(t, err)
	got := value.(CacheNorthstarResponse)
	require.EqualValues(t, 12, got.Summary.Requests)
	require.EqualValues(t, 3000, got.Summary.PromptTokens)
	require.InDelta(t, 0.5, *got.Summary.BusinessCoverage, 1e-12)
	require.InDelta(t, 2.0/3, *got.Summary.HitUpper, 1e-12)
	require.EqualValues(t, 2, got.Scope.Excluded["unknown_threshold_requests"])
	require.NotNil(t, got.Summary.Previous)
	require.Len(t, got.Series, 24)
	_, err = s.Query(context.Background(), "northstar", q)
	require.NoError(t, err)
	require.Equal(t, []string{"state", "read", "events"}, repo.calls)
	_, _, err = policies.Update(context.Background(), "northstar", policyWrite(t, `{"expected_rev":0,"spec":{"backfill_days":2}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	_, err = s.Query(context.Background(), "northstar", q)
	require.NoError(t, err)
	require.Len(t, repo.calls, 6)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	for _, forbidden := range []string{"session_digest", "request_id_hmac", "api_key_id", "user_id", "user_agent"} {
		require.NotContains(t, string(raw), forbidden)
	}
	q.Dimension = "group"
	q.Sort = "opportunity"
	q.Limit = 10
	value, err = s.Query(context.Background(), "breakdown", q)
	require.NoError(t, err)
	breakdown := value.(CacheBreakdownResponse)
	require.Len(t, breakdown.Rows, 1)
	require.Equal(t, "1", breakdown.Rows[0].Key)
	require.NotNil(t, breakdown.Other)
	require.EqualValues(t, 2, breakdown.Other.Requests)
	require.EqualValues(t, 6000, breakdown.Total.PromptTokens)
	require.InDelta(t, 200.0/700, *breakdown.Other.OpportunityShare, 1e-12)
	for _, call := range repo.calls {
		require.NotEqual(t, "aggregate", call)
	}
}

func TestCacheNorthstarNotReadyAndVersionBoundary(t *testing.T) {
	repo := &northstarTestRepo{}
	policies := northstarTestPolicies(t, false)
	s := NewCacheObservabilityService(repo, policies, NewCacheTraceCounters(), CachePanelBuildInfo{})
	_, err := s.Query(context.Background(), "northstar", CachePanelQuery{Window: "24h"})
	var failure *CachePolicyError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, 503, failure.Status)
}

func TestCacheNorthstarDashboardSequenceIndependent(t *testing.T) {
	watermark := time.Now().UTC().Add(-time.Hour)
	var sequences [][]string
	for _, mode := range []string{"baseline", "off", "on_failure"} {
		events := []string{}
		dash := &dashboardAggregationRollupRepoTestStub{dashboardAggregationRepoTestStub: &dashboardAggregationRepoTestStub{watermark: watermark, events: &events}}
		service := &DashboardAggregationService{repo: dash, cfg: config.DashboardAggregationConfig{Enabled: true, IntervalSeconds: 60, LookbackSeconds: 120}}
		northstar := &northstarTestRepo{fail: errors.New("independent synthetic failure")}
		if mode != "baseline" {
			service.cacheNorthstar = NewCacheNorthstarAggregator(northstar, northstarTestPolicies(t, mode == "on_failure"), NewCacheTraceCounters(), CachePanelBuildInfo{})
		}
		service.runScheduledAggregation()
		sequences = append(sequences, events)
		require.Equal(t, watermark.Add(-120*time.Second), dash.lastStart)
		require.Equal(t, 1, dash.aggregateCalls)
		require.Equal(t, 1, dash.groupRollupCalls)
		if mode == "off" {
			require.Empty(t, northstar.calls)
		}
	}
	require.Equal(t, sequences[0], sequences[1])
	require.Equal(t, sequences[0], sequences[2])
}
func TestCacheNorthstarMetricsNeverDriveSelection(t *testing.T) {
	for _, name := range []string{"gateway_scheduling.go", "openai_account_scheduler.go", "openai_gateway_scheduling.go", "gemini_messages_compat_service.go"} {
		raw, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		for _, forbidden := range []string{"CacheTraceCounters", "CacheObservabilityService", "NorthstarRollup", "host_read_ewma", "token_hit_cost_weighted"} {
			require.NotContains(t, string(raw), forbidden, name)
		}
	}
}

func TestCacheNorthstarRequiresPersistedCompleteHour(t *testing.T) {
	policies := northstarTestPolicies(t, false)
	now := time.Now().UTC().Truncate(time.Hour)
	for _, version := range []string{"old-policy", "2026-10-22-v1"} {
		repo := &northstarTestRepo{state: &CacheNorthstarState{PolicyVersion: version, AggregatedUntil: now}}
		panel := NewCacheObservabilityService(repo, policies, NewCacheTraceCounters(), CachePanelBuildInfo{})
		_, err := panel.Query(context.Background(), "northstar", CachePanelQuery{Window: "24h", Granularity: "1h", Platform: "all", CostView: "list"})
		var failure *CachePolicyError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, 503, failure.Status)
	}
}

func TestCacheNorthstarMixedTimezoneBuckets(t *testing.T) {
	at := time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)
	local := at.In(time.FixedZone("Asia/Shanghai", 8*3600))
	row := CacheNorthstarRow{BucketStart: local, Platform: "openai", Model: "gpt-5.4", ThresholdBand: 2048, CacheNorthstarTotals: CacheNorthstarTotals{
		Requests: 12, RequestsHit: 6, PromptTokens: 24000, ReadTokens: 12000, InputTokens: 12000, InputCostMicros: 24000, ReadCostMicros: 1200}}
	second := row
	second.BucketStart = at
	second.InputCostMicros = 48000
	rows := []CacheNorthstarRow{row, second}
	prices := northstarPrices(rows)
	cost := northstarCost(rows, rows, prices, "list")
	require.False(t, cost.PricingChangedInWindow, "same hour in two locations is one price bucket")
	policy := parseNorthstarPolicy(northstarPolicyDefaults())
	meta := CachePanelMeta{PolicyVersion: policy.PolicyVersion, AsOf: at.Add(time.Hour), GeneratedAt: at.Add(time.Hour), Window: CachePanelWindow{Start: at, End: at.Add(time.Hour)}}
	cold := local.Add(time.Hour)
	got := buildCacheNorthstar(meta, CachePanelQuery{Window: "24h", Granularity: "1h", Platform: "all", CostView: "list"}, policy,
		&CacheNorthstarState{PolicyChangedAt: at}, CacheNorthstarData{Rows: rows, Hours: []CacheNorthstarHour{{BucketStart: local}}},
		[]CachePanelEvent{{At: local, Kind: "release", Label: "test", ColdUntil: &cold}})
	require.Len(t, got.Series, 1)
	require.EqualValues(t, 24, got.Series[0].Requests)
	require.EqualValues(t, 48000, got.Series[0].PromptTokens)
	require.InDelta(t, 0.5, *got.Series[0].HitUpper, 1e-12)
	require.Greater(t, *got.Series[0].SavedAmount, 0.0)
	require.False(t, got.Series[0].Partial)
	require.Equal(t, at, got.Events[0].At)
	require.Equal(t, at.Add(time.Hour), *got.Events[0].ColdUntil)
}

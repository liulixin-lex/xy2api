package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type cachePanelCacheKey struct {
	kind     string
	query    CachePanelQuery
	revision int64
}
type cachePanelCached struct {
	value   any
	expires time.Time
}
type CacheObservabilityService struct {
	repo     CacheNorthstarRepository
	policies *CachePolicyService
	counters *CacheTraceCounters
	build    CachePanelBuildInfo
	now      func() time.Time
	mu       sync.Mutex
	cached   map[cachePanelCacheKey]cachePanelCached
}

func NewCacheObservabilityService(repo CacheNorthstarRepository, policies *CachePolicyService, counters *CacheTraceCounters, build CachePanelBuildInfo) *CacheObservabilityService {
	if counters == nil {
		counters = NewCacheTraceCounters()
	}
	return &CacheObservabilityService{repo: repo, policies: policies, counters: counters, build: build, now: time.Now, cached: map[cachePanelCacheKey]cachePanelCached{}}
}
func cachePanelInvalid() error {
	return &CachePolicyError{Status: 400, Reason: "CACHE_PANEL_INVALID_PARAM", Message: "invalid cache panel parameter"}
}
func cacheRollupNotReady() error {
	return &CachePolicyError{Status: 503, Reason: "CACHE_ROLLUP_NOT_READY", Message: "cache rollup has no complete hour"}
}
func cachePanelOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func ParseCachePanelQuery(kind string, values url.Values) (CachePanelQuery, error) {
	q := CachePanelQuery{Window: "7d", Platform: "all", CostView: "list", Sort: "opportunity", Limit: 10}
	allowed := map[string]bool{"window": true, "platform": true}
	switch kind {
	case "northstar":
		allowed["granularity"] = true
		allowed["cost_view"] = true
	case "breakdown":
		allowed["dimension"] = true
		allowed["sort"] = true
		allowed["limit"] = true
		allowed["cost_view"] = true
	case "stats":
		q.Window = "24h"
		q.Platform = "openai"
	case "experiments":
		allowed = map[string]bool{}
	default:
		return q, cachePanelInvalid()
	}
	for name, items := range values {
		if !allowed[name] || len(items) != 1 || items[0] == "" {
			return q, cachePanelInvalid()
		}
	}
	if v := values.Get("window"); v != "" {
		q.Window = v
	}
	if v := values.Get("platform"); v != "" {
		q.Platform = v
	}
	if v := values.Get("cost_view"); v != "" {
		q.CostView = v
	}
	if !cachePanelOneOf(q.Platform, "all", "openai", "anthropic", "gemini", "grok") || !cachePanelOneOf(q.CostView, "list", "charged", "upstream") {
		return q, cachePanelInvalid()
	}
	if kind == "stats" {
		if !cachePanelOneOf(q.Window, "1h", "24h") {
			return q, cachePanelInvalid()
		}
	} else if !cachePanelOneOf(q.Window, "24h", "7d", "30d") {
		return q, cachePanelInvalid()
	}
	q.Granularity = values.Get("granularity")
	if q.Granularity == "" {
		q.Granularity = "1h"
		if q.Window == "30d" {
			q.Granularity = "1d"
		}
	}
	if !cachePanelOneOf(q.Granularity, "1h", "1d") {
		return q, cachePanelInvalid()
	}
	if kind == "breakdown" {
		q.Dimension = values.Get("dimension")
		if !cachePanelOneOf(q.Dimension, "group", "account", "upstream_host", "client_family", "protocol", "model") {
			return q, cachePanelInvalid()
		}
		if v := values.Get("sort"); v != "" {
			q.Sort = v
		}
		if !cachePanelOneOf(q.Sort, "opportunity", "opportunity_amount", "prompt", "token_hit") {
			return q, cachePanelInvalid()
		}
		if v := values.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 50 {
				return q, cachePanelInvalid()
			}
			q.Limit = n
		}
	}
	return q, nil
}
func cachePanelDuration(window string) time.Duration {
	switch window {
	case "1h":
		return time.Hour
	case "24h":
		return 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 7 * 24 * time.Hour
	}
}

func (s *CacheObservabilityService) Query(ctx context.Context, kind string, q CachePanelQuery) (any, error) {
	now := s.now().UTC()
	snapshot := s.policies.Snapshot()
	if kind == "stats" {
		return s.counters.Snapshot(now, q, snapshot), nil
	}
	if kind == "experiments" {
		return CacheExperimentsResponse{CachePanelMeta: CachePanelMeta{PolicyVersion: snapshot.NorthstarPolicy().PolicyVersion, AsOf: now, GeneratedAt: now, Window: CachePanelWindow{Start: now, End: now}}, Experiments: []any{}}, nil
	}
	key := cachePanelCacheKey{kind: kind, query: q, revision: snapshot.NorthstarRevision()}
	s.mu.Lock()
	cached, ok := s.cached[key]
	s.mu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached.value, nil
	}
	state, err := s.repo.GetCacheNorthstarState(ctx)
	if err != nil {
		return nil, err
	}
	policy := snapshot.NorthstarPolicy()
	if state == nil || state.PolicyVersion != policy.PolicyVersion {
		return nil, cacheRollupNotReady()
	}
	end := state.AggregatedUntil.UTC().Truncate(time.Hour)
	if end.After(now.Truncate(time.Hour)) {
		end = now.Truncate(time.Hour)
	}
	if !end.After(time.Unix(0, 0)) {
		return nil, cacheRollupNotReady()
	}
	start := end.Add(-cachePanelDuration(q.Window))
	from := start
	if kind == "northstar" {
		from = start.Add(-cachePanelDuration(q.Window))
	}
	data, err := s.repo.ReadCacheNorthstar(ctx, policy.PolicyVersion, from, end, q.Platform)
	if err != nil {
		return nil, err
	}
	ready := false
	for _, hour := range data.Hours {
		if !hour.BucketStart.Before(start) && hour.BucketStart.Before(end) {
			ready = true
			break
		}
	}
	if !ready {
		return nil, cacheRollupNotReady()
	}
	meta := CachePanelMeta{PolicyVersion: policy.PolicyVersion, AsOf: end, GeneratedAt: now, Window: CachePanelWindow{Start: start, End: end}}
	var value any
	switch kind {
	case "northstar":
		var events []CachePanelEvent
		events, err = s.repo.ListCachePanelEvents(ctx, start, end)
		if err != nil {
			return nil, err
		}
		value = buildCacheNorthstar(meta, q, policy, state, data, events)
	case "breakdown":
		value = buildCacheBreakdown(meta, q, policy, data.Rows)
	default:
		return nil, fmt.Errorf("unknown cache panel view")
	}
	s.mu.Lock()
	if len(s.cached) >= 64 {
		s.cached = map[cachePanelCacheKey]cachePanelCached{}
	}
	s.cached[key] = cachePanelCached{value: value, expires: now.Add(time.Minute)}
	s.mu.Unlock()
	return value, nil
}

func buildCacheNorthstar(meta CachePanelMeta, q CachePanelQuery, policy CacheNorthstarPolicy, state *CacheNorthstarState, data CacheNorthstarData, events []CachePanelEvent) CacheNorthstarResponse {
	// Keep response timestamps canonical even for alternate repository readers.
	events = append([]CachePanelEvent{}, events...)
	for i := range events {
		events[i].At = events[i].At.UTC()
		if events[i].ColdUntil != nil {
			at := events[i].ColdUntil.UTC()
			events[i].ColdUntil = &at
		}
	}
	result := CacheNorthstarResponse{CachePanelMeta: meta, Granularity: q.Granularity, Platform: q.Platform,
		Scope: CacheNorthstarScope{Name: "strict_known_minimum", Excluded: map[string]int64{"media_requests": 0, "zero_prompt_requests": 0, "probe_group_requests": 0, "below_min_requests": 0, "unknown_threshold_requests": 0}}, Series: []CacheNorthstarPoint{}, Events: events}
	var business, strict, previousBusiness, previousStrict []CacheNorthstarRow
	for _, row := range data.Rows {
		if !policy.Business(row) {
			continue
		}
		if row.BucketStart.Before(meta.Window.Start) {
			previousBusiness = append(previousBusiness, row)
			if policy.Strict(row) {
				previousStrict = append(previousStrict, row)
			}
			continue
		}
		business = append(business, row)
		result.Scope.BusinessPromptTokens += row.PromptTokens
		if policy.Strict(row) {
			strict = append(strict, row)
		} else if _, known := policy.Threshold(row.Model); known {
			result.Scope.Excluded["below_min_requests"] += row.Requests
		} else {
			result.Scope.Excluded["unknown_threshold_requests"] += row.Requests
		}
	}
	for _, hour := range data.Hours {
		if hour.BucketStart.Before(meta.Window.Start) {
			continue
		}
		if policy.ExcludeMedia {
			result.Scope.Excluded["media_requests"] += hour.MediaRequests
		}
		result.Scope.Excluded["zero_prompt_requests"] += hour.ZeroPromptRequests
		result.Scope.Excluded["probe_group_requests"] += hour.ProbeGroupRequests
	}
	prices := northstarPrices(business)
	result.Summary = CacheNorthstarSummary{CachePanelMetrics: cachePanelMetrics(strict), Cost: northstarCost(strict, business, prices, q.CostView)}
	result.Summary.BusinessCoverage = northstarRatio(float64(result.Summary.PromptTokens), float64(result.Scope.BusinessPromptTokens))
	previousStart := meta.Window.Start.Add(-cachePanelDuration(q.Window))
	if !previousStart.Before(state.PolicyChangedAt) && cacheNorthstarHoursComplete(data.Hours, previousStart, meta.Window.Start) {
		previous := cachePanelMetrics(previousStrict)
		result.Summary.Previous = &CacheNorthstarPrevious{HitUpper: previous.HitUpper, HitLower: previous.HitLower, RequestHit: previous.RequestHit}
		previousCost := northstarCost(previousStrict, previousBusiness, northstarPrices(previousBusiness), q.CostView)
		result.Summary.Cost.Previous = &CacheNorthstarCostPrevious{SavedAmount: previousCost.SavedAmount, HitUpper: previousCost.HitUpper}
	}
	if len(business) == 0 {
		return result
	}
	step := time.Hour
	if q.Granularity == "1d" {
		step = 24 * time.Hour
	}
	byBucket := make(map[time.Time][]CacheNorthstarRow)
	for _, row := range strict {
		bucket := row.BucketStart.UTC().Truncate(step)
		byBucket[bucket] = append(byBucket[bucket], row)
	}
	for bucket := meta.Window.Start.Truncate(step); bucket.Before(meta.Window.End); bucket = bucket.Add(step) {
		selected := byBucket[bucket]
		metrics := cachePanelMetrics(selected)
		values := northstarCostSums(selected, prices, q.CostView)
		cost := CacheNorthstarCost{}
		if len(values.models) > 0 {
			cost.SavedAmount = &values.saved
			cost.OpportunityAmount = &values.opportunity
			cost.HitUpper = northstarRatio(values.weightedRead, values.weightedPrompt)
		}
		partial := bucket.Before(meta.Window.Start) || bucket.Add(step).After(meta.Window.End) || !cacheNorthstarHoursComplete(data.Hours, bucket, bucket.Add(step))
		result.Series = append(result.Series, CacheNorthstarPoint{BucketStart: bucket, Requests: metrics.Requests, PromptTokens: metrics.PromptTokens, HitUpper: metrics.HitUpper, HitLower: metrics.HitLower, RequestHit: metrics.RequestHit, OpportunityTokens: metrics.OpportunityTokens, Partial: partial, SavedAmount: cost.SavedAmount, OpportunityAmount: cost.OpportunityAmount, HitCostUpper: cost.HitUpper})
	}
	return result
}

func cacheNorthstarHoursComplete(hours []CacheNorthstarHour, start, end time.Time) bool {
	covered := make(map[time.Time]bool, len(hours))
	for _, hour := range hours {
		covered[hour.BucketStart.UTC()] = true
	}
	for cursor := start; cursor.Before(end); cursor = cursor.Add(time.Hour) {
		if !covered[cursor.UTC()] {
			return false
		}
	}
	return true
}

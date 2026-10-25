package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
)

const CachePolicyRefreshInterval = 60 * time.Second

// Shared by all service instances in this process, including delayed construction.
var cachePolicyProcessStartedAt = time.Now().UTC()

var cachePolicyKeys = [...]string{"master", "observability", "northstar", "upstream_identity", "anthropic_identity", "routing"}
var cachePolicySettings = [...]string{"cache_feature_master", "cache_observability_policy", "cache_northstar_policy", "cache_upstream_identity_policy", "cache_anthropic_identity_policy", "cache_routing_policy"}

// CachePolicyStore serializes read/validate/write in one database transaction,
// including the first write when the settings row does not yet exist.
type CachePolicyStore interface {
	UpdateCachePolicy(context.Context, string, func(string) (string, error)) error
}

type CachePolicyActor struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
}
type CachePolicyEnvelope struct {
	Rev       int64             `json:"rev"`
	UpdatedAt *time.Time        `json:"updated_at"`
	UpdatedBy *CachePolicyActor `json:"updated_by"`
	Reason    *string           `json:"reason"`
	Spec      map[string]any    `json:"spec"`
}
type CachePolicyCaps struct {
	Workers          int `json:"workers"`
	MaxByteBudgetMiB int `json:"max_byte_budget_mib"`
	MaxRateMiBPerS   int `json:"max_rate_mib_per_s"`
	MaxSessions      int `json:"max_sessions"`
	ChainsPerSession int `json:"chains_per_session"`
	MaxCheckpoints   int `json:"max_checkpoints"`
}
type cachePolicyEntry struct {
	envelope   CachePolicyEnvelope
	parseError bool
	loadedAt   time.Time
}

// CachePolicySnapshot contains immutable policy data. Collection fields stay
// private and are built once at publication. Requests hold one pointer across
// retries and streaming; readers never consult settings.
type CachePolicySnapshot struct {
	kill                bool
	stale               bool
	master              bool
	trace               bool
	northstar           bool
	persistTrace        bool
	northstarSpec       CacheNorthstarPolicy
	samplePercent       int
	chainPercent        int
	byteBudget          int64
	rate                int64
	revs                [6]int64
	revision            string
	entries             [6]cachePolicyEntry
	upstream            CacheUpstreamIdentityPolicy
	cohortViews         [2]cachePolicyCohortView
	platforms           uint8
	ingressChainPercent int
}

func (p *CachePolicySnapshot) TraceEnabled() bool  { return p != nil && p.trace }
func (p *CachePolicySnapshot) MasterEnabled() bool { return p != nil && !p.kill && p.master }

// behaviorStale marks a snapshot republished after settings refreshes failed for
// longer than gateway.cache_policy.max_stale_seconds. Behavior accessors AND it
// with master and build capability at publication; master, observability and
// northstar ignore it.
func (p *CachePolicySnapshot) behaviorStale() bool { return p != nil && p.stale }
func (p *CachePolicySnapshot) SamplePercent() int {
	if p == nil {
		return 0
	}
	return p.samplePercent
}
func (p *CachePolicySnapshot) ChainPercent() int {
	if p == nil {
		return 0
	}
	return p.chainPercent
}
func (p *CachePolicySnapshot) ByteBudget() int64 {
	if p == nil {
		return 0
	}
	return p.byteBudget
}
func (p *CachePolicySnapshot) RateBytesPerSecond() int64 {
	if p == nil {
		return 0
	}
	return p.rate
}
func (p *CachePolicySnapshot) Revision() string {
	if p == nil {
		return "m0.o0.u0.a0.r0"
	}
	return p.revision
}
func (p *CachePolicySnapshot) ObservabilityRevision() int64 {
	if p == nil {
		return 0
	}
	return p.revs[1]
}

// CachePolicyOption configures immutable service dependencies at construction.
type CachePolicyOption func(*CachePolicyService)

// WithCacheNorthstar enables the M2 build capability, never a runtime setting.
func WithCacheNorthstar() CachePolicyOption {
	return func(s *CachePolicyService) { s.northstarAvailable = true }
}
func (p *CachePolicySnapshot) NorthstarEnabled() bool { return p != nil && p.northstar }
func (p *CachePolicySnapshot) NorthstarPolicy() CacheNorthstarPolicy {
	if p == nil {
		return CacheNorthstarPolicy{}
	}
	copy := p.northstarSpec
	copy.ExcludedGroupIDs = append([]int64{}, copy.ExcludedGroupIDs...)
	copy.ModelThresholds = append([]CacheNorthstarThresholdRule{}, copy.ModelThresholds...)
	return copy
}
func (p *CachePolicySnapshot) NorthstarRevision() int64 {
	if p == nil {
		return 0
	}
	return p.revs[2]
}

// WithCachePolicyClock makes policy and audit timestamps deterministic for
// callers that supply a clock. Request snapshot reads never call this function.
func WithCachePolicyClock(now func() time.Time) CachePolicyOption {
	return func(s *CachePolicyService) {
		if now != nil {
			s.now = now
		}
	}
}

type CachePolicyService struct {
	now                             func() time.Time
	repo                            SettingRepository
	store                           CachePolicyStore
	accounts                        CachePolicyAccountLookup
	groups                          CachePolicyGroupLookup
	m4Available                     bool
	m4Behaviors                     CacheM4BuildCapabilities
	anthropicObservabilityAvailable bool
	caps                            CachePolicyCaps
	kill, diagnostics, aware        bool
	rollout                         int
	started                         time.Time
	snapshot                        atomic.Pointer[CachePolicySnapshot]
	mu                              sync.Mutex
	cancel                          context.CancelFunc
	wg                              sync.WaitGroup
	listeners                       []func(*CachePolicySnapshot)
	northstarAvailable              bool
	providerTraceAvailable          bool

	// Staleness bound, guarded by mu. refreshedAt is the last successful
	// settings read; administrator writes neither refresh nor clear it.
	maxStale    time.Duration
	refreshedAt time.Time
	stale       bool
	staleTotal  int64
}

func NewCachePolicyService(repo SettingRepository, cfg *config.Config, options ...CachePolicyOption) *CachePolicyService {
	s := &CachePolicyService{now: time.Now, repo: repo, started: cachePolicyProcessStartedAt, caps: CachePolicyCaps{1, 64, 32, 1024, 3, 512}}
	for _, option := range options {
		option(s)
	}
	s.store, _ = repo.(CachePolicyStore)
	if cfg != nil {
		c := cfg.Gateway.CacheTrace
		s.kill = cfg.Gateway.CacheFeatures.KillSwitch
		s.diagnostics = cfg.Gateway.OpenAICache.DiagnosticsEnabled
		s.aware = cfg.Gateway.OpenAICache.AwareRoutingEnabled
		s.rollout = cfg.Gateway.OpenAICache.RolloutPercent
		s.maxStale = time.Duration(cfg.Gateway.CachePolicy.MaxStaleSeconds) * time.Second
		if c.Workers > 0 {
			s.caps.Workers = c.Workers
		}
		if c.MaxByteBudgetMiB > 0 {
			s.caps.MaxByteBudgetMiB = c.MaxByteBudgetMiB
		}
		if c.MaxRateMiBPerS > 0 {
			s.caps.MaxRateMiBPerS = c.MaxRateMiBPerS
		}
		if c.MaxSessions > 0 {
			s.caps.MaxSessions = c.MaxSessions
		}
		if c.ChainsPerSession > 0 {
			s.caps.ChainsPerSession = c.ChainsPerSession
		}
		if c.MaxCheckpoints > 0 {
			s.caps.MaxCheckpoints = c.MaxCheckpoints
		}
	}
	s.refreshedAt = s.now().UTC()
	s.publish(map[string]string{})
	return s
}

// Snapshot is a single atomic load and allocates nothing.
func (s *CachePolicyService) Snapshot() *CachePolicySnapshot {
	if s == nil {
		return nil
	}
	return s.snapshot.Load()
}
func (s *CachePolicyService) Start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	if err := s.Refresh(ctx); err != nil {
		slog.Warn("cache policy refresh failed")
	}
	go func() {
		defer s.wg.Done()
		// Leave headroom for the bounded database read within the 60s contract.
		ticker := time.NewTicker(CachePolicyRefreshInterval / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.Refresh(ctx); err != nil {
					slog.Warn("cache policy refresh failed")
				}
			}
		}
	}()
}
func (s *CachePolicyService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *CachePolicyService) Refresh(ctx context.Context) error {
	before := s.snapshot.Load()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.repo.GetMultiple(ctx, cachePolicySettings[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		// Keep the last valid snapshot on read failure, up to max_stale_seconds.
		s.boundStaleLocked()
		return err
	}
	now := s.now().UTC()
	age := now.Sub(s.refreshedAt)
	s.refreshedAt = now
	// A successful write or another refresh published while storage was being
	// read. Discard this possibly stale read; the next poll will reconcile.
	if s.snapshot.Load() == before {
		if s.stale {
			s.stale = false
			slog.Info("cache policy snapshot refreshed", "cache_policy_snapshot_age_seconds", int64(age/time.Second))
		}
		s.publish(raw)
	}
	return nil
}

// boundStaleLocked republishes the loaded entries once, with behavior policies
// off, after refreshes have failed for longer than max_stale_seconds. It never
// writes settings or revisions; the next successful refresh restores them.
func (s *CachePolicyService) boundStaleLocked() {
	if s.maxStale <= 0 || s.stale {
		return
	}
	age := s.now().UTC().Sub(s.refreshedAt)
	if age <= s.maxStale {
		return
	}
	s.stale = true
	s.staleTotal++
	slog.Warn("cache policy snapshot stale", "cache_policy_snapshot_age_seconds", int64(age/time.Second), "cache_policy_stale_total", s.staleTotal, "max_stale_seconds", int64(s.maxStale/time.Second))
	s.publishEntries(s.snapshot.Load().entries)
}
func (s *CachePolicyService) publish(raw map[string]string) {
	entries := [6]cachePolicyEntry{}
	for i := range entries {
		if s.cachePolicySupported(i) {
			entries[i] = s.parse(i, raw[cachePolicySettings[i]])
		}
	}
	s.publishEntries(entries)
}
func (s *CachePolicyService) publishEntries(entries [6]cachePolicyEntry) {
	p := &CachePolicySnapshot{kill: s.kill, stale: s.stale, entries: entries}
	for i := range entries {
		p.revs[i] = entries[i].envelope.Rev
	}
	p.master, _ = entries[0].envelope.Spec["enabled"].(bool)
	if s.northstarAvailable {
		p.northstarSpec = parseNorthstarPolicy(entries[2].envelope.Spec)
		p.northstar = !s.kill && !entries[2].parseError && p.northstarSpec.RollupEnabled
	}

	o := entries[1].envelope.Spec
	enabled, _ := o["enabled"].(bool)
	p.trace = !s.kill && enabled && s.diagnostics && !entries[1].parseError
	persist, _ := o["persist_trace_hourly"].(bool)
	p.persistTrace = p.trace && persist
	p.samplePercent = policyInt(o["sample_percent"])
	p.chainPercent = policyInt(o["chain_percent"])
	p.platforms = cacheTracePlatformOpenAI
	if s.providerTraceAvailable {
		p.platforms = cacheTracePlatformsMask(o["platforms"])
		p.ingressChainPercent = policyInt(o["ingress_chain_percent"])
	}
	p.byteBudget = int64(min(policyInt(o["byte_budget_mib"]), s.caps.MaxByteBudgetMiB)) << 20
	p.rate = int64(min(policyInt(o["rate_mib_per_s"]), s.caps.MaxRateMiBPerS)) << 20
	p.upstream = upstreamIdentityFromEntry(entries[3], p.MasterEnabled() && !p.stale)
	p.revision = fmt.Sprintf("m%d.o%d.u%d.a%d.r%d", p.revs[0], p.revs[1], p.revs[3], p.revs[4], p.revs[5])
	p.initCohortViews()
	for _, notify := range s.listeners {
		notify(p)
	}
	s.snapshot.Store(p)
}
func (s *CachePolicyService) parse(index int, raw string) cachePolicyEntry {
	e := cachePolicyEntry{envelope: CachePolicyEnvelope{Spec: s.defaults(index)}, loadedAt: s.now().UTC()}
	if raw == "" {
		return e
	}
	var stored struct {
		Rev       *int64            `json:"rev"`
		UpdatedAt *time.Time        `json:"updated_at"`
		UpdatedBy *CachePolicyActor `json:"updated_by"`
		Reason    *string           `json:"reason"`
		Spec      json.RawMessage   `json:"spec"`
	}
	err := json.Unmarshal([]byte(raw), &stored)
	if stored.Rev != nil && *stored.Rev >= 0 {
		e.envelope.Rev = *stored.Rev
		e.envelope.UpdatedAt = stored.UpdatedAt
		e.envelope.UpdatedBy = stored.UpdatedBy
		e.envelope.Reason = stored.Reason
	}
	spec, _, errs := s.merge(index, s.defaults(index), stored.Spec, false)
	if err != nil || stored.Rev == nil || *stored.Rev < 0 || len(errs) != 0 {
		e.parseError = true
		slog.Warn("cache policy parse failed", "policy_key", cachePolicyKeys[index])
	} else {
		e.envelope.Spec = spec
	}
	return e
}
func policyIndex(key string) int {
	for i, k := range cachePolicyKeys {
		if key == k {
			return i
		}
	}
	return -1
}
func policyDefaults(index int) map[string]any {
	if index == 4 || index == 5 {
		return m4PolicyDefaults(index)
	}
	if index == 2 {
		return northstarPolicyDefaults()
	}
	if index == 0 {
		return map[string]any{"enabled": false}
	}
	if index == 3 {
		return map[string]any{"mode": "off", "account_ids": []int64{}, "rollout_percent": 0, "chat_compat_uuid": false, "header_only_uuid": false, "forward_session_headers": "whitelist", "max_inflight_per_key": 2}
	}
	return map[string]any{"enabled": false, "sample_percent": 100, "chain_percent": 100, "byte_budget_mib": 64, "rate_mib_per_s": 32}
}
func policyInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func ProvideCachePolicyService(repo SettingRepository, cfg *config.Config, accounts AccountRepository, groups GroupRepository) *CachePolicyService {
	s := NewCachePolicyService(repo, cfg, WithCacheNorthstar(), WithCachePolicyAccounts(accounts), WithCacheM4Policies(groups), WithProviderCacheTrace())
	s.Start()
	return s
}

// subscribe is used only by lazily constructed resource pools. Publication and
// subscription share the writer lock, preventing a pool from missing an update.
func (s *CachePolicyService) subscribe(notify func(*CachePolicySnapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, notify)
	notify(s.Snapshot())
}

func (s *CachePolicyService) defaults(index int) map[string]any {
	out := policyDefaults(index)
	if index == 1 && s.providerTraceAvailable {
		out["platforms"] = []string{"openai"}
		out["ingress_chain_percent"] = 10
	}
	if s.northstarAvailable && index == 1 {
		out["persist_trace_hourly"] = true
	}
	return out
}
func (p *CachePolicySnapshot) NorthstarRollupEnabled() bool    { return p.NorthstarEnabled() }
func (p *CachePolicySnapshot) PersistTraceHourlyEnabled() bool { return p != nil && p.persistTrace }

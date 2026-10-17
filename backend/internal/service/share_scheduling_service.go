package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

const (
	shareSchedulingSettingsKey     = "share_scheduling_settings"
	shareSchedulingRefreshInterval = 30 * time.Second
	shareSchedulingFlushInterval   = time.Second
	shareSchedulingLoadTimeout     = 5 * time.Second
	maxShareAttributionSampleRate  = 64
)

// shareSchedulingSettingsChange is the broadcast id of a settings change;
// group ids are never negative.
const shareSchedulingSettingsChange int64 = -1

// ErrShareSchedulingVersionConflict reports a lost compare-and-swap on a
// group policy: another administrator saved first.
var ErrShareSchedulingVersionConflict = infraerrors.Conflict("SHARE_SCHEDULING_VERSION_CONFLICT", "share scheduling policy was changed by someone else; reload and retry")

// ShareSchedulingSettings are the global share-scheduling switches.
type ShareSchedulingSettings struct {
	// Enabled is the master switch. While off every group uses the original
	// Sub2API scheduler whatever its policy says.
	Enabled bool `json:"enabled"`
	// CacheAttribution classifies prompt-cache outcomes for every group,
	// including groups on the original scheduler, so both have a baseline.
	CacheAttribution bool `json:"cache_attribution"`
	// AttributionSampleRate samples one session in N for switch attribution.
	AttributionSampleRate int `json:"attribution_sample_rate"`
	// DecisionSamples keeps the most recent decisions of each share group.
	DecisionSamples bool `json:"decision_samples"`
}

// DefaultShareSchedulingSettings keeps routing on Sub2API and starts building
// the cache baseline.
func DefaultShareSchedulingSettings() ShareSchedulingSettings {
	return ShareSchedulingSettings{CacheAttribution: true, AttributionSampleRate: 4, DecisionSamples: true}
}

// Validate checks the settings ranges.
func (s ShareSchedulingSettings) Validate() error {
	if s.AttributionSampleRate < 1 || s.AttributionSampleRate > maxShareAttributionSampleRate {
		return infraerrors.BadRequest("SHARE_SCHEDULING_SETTINGS_INVALID", fmt.Sprintf("attribution_sample_rate must be between 1 and %d", maxShareAttributionSampleRate))
	}
	return nil
}

// ShareSchedulingPolicyRow is the stored form of one group's policy.
type ShareSchedulingPolicyRow struct {
	GroupID      int64
	Version      int64
	Scheduler    string
	Policy       []byte
	ImportedFrom string
	UpdatedBy    *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ShareSchedulingUsage is one account's traffic in a group over a window.
type ShareSchedulingUsage struct {
	Requests     int64
	Tokens       int64
	StandardCost float64
}

// ShareSchedulingRepository persists group policies.
type ShareSchedulingRepository interface {
	ListPolicies(ctx context.Context) ([]ShareSchedulingPolicyRow, error)
	// GetPolicy returns nil without error when the group has no policy.
	GetPolicy(ctx context.Context, groupID int64) (*ShareSchedulingPolicyRow, error)
	// SavePolicy stores a policy when the current version equals
	// expectedVersion (0 creates). It returns ErrShareSchedulingVersionConflict
	// otherwise.
	SavePolicy(ctx context.Context, groupID, expectedVersion int64, scheduler string, policy []byte, updatedBy int64) (*ShareSchedulingPolicyRow, error)
	// AccountUsage aggregates usage_logs of a group per account since a time.
	AccountUsage(ctx context.Context, groupID int64, since time.Time) (map[int64]ShareSchedulingUsage, error)
}

// ShareSchedulingCounter addresses one counter field in one minute bucket.
type ShareSchedulingCounter struct {
	Kind    string
	GroupID int64
	Minute  int64
	Field   string
}

// ShareSchedulingStatsBatch is one asynchronous flush.
type ShareSchedulingStatsBatch struct {
	Counters map[ShareSchedulingCounter]int64
	// Decisions holds JSON samples per group, oldest first.
	Decisions map[int64][][]byte
}

// ShareSchedulingBinding is one session binding seen by a rebalance scan.
type ShareSchedulingBinding struct {
	AccountID int64
	Remaining time.Duration
}

// ShareSchedulingStore is the Redis side: change broadcast, statistics and
// session memory. Every method may fail; callers drop statistics on error.
type ShareSchedulingStore interface {
	PublishChange(ctx context.Context, groupID int64) error
	SubscribeChanges(ctx context.Context, handler func(groupID int64))
	WriteStats(ctx context.Context, batch ShareSchedulingStatsBatch) error
	ReadCounters(ctx context.Context, kind string, groupID int64, from, to time.Time) (map[string]int64, error)
	RecentDecisions(ctx context.Context, groupID int64, limit int) ([][]byte, error)
	// SwapCacheSession stores the latest request of a sampled session and
	// returns the previous one (nil when none).
	SwapCacheSession(ctx context.Context, groupID int64, fingerprint string, value []byte, ttl time.Duration) ([]byte, error)
	// ClearBindings deletes session bindings of a group for which clear
	// returns true and reports how many were deleted.
	ClearBindings(ctx context.Context, groupID int64, clear func(ShareSchedulingBinding) bool) (int, error)
}

// shareGroupPolicy is the immutable runtime form of one group's policy.
type shareGroupPolicy struct {
	groupID int64
	version int64
	policy  sharesched.Policy
}

type shareSchedulingState struct {
	settings ShareSchedulingSettings
	groups   map[int64]*shareGroupPolicy
}

// ShareSchedulingService owns share-scheduling policies and observability.
// The request path only reads atomically published snapshots; storage
// failures keep the last known state, and a cold start without storage keeps
// every group on the original scheduler.
type ShareSchedulingService struct {
	repo     ShareSchedulingRepository
	store    ShareSchedulingStore
	settings SettingRepository
	node     string

	state       atomic.Pointer[shareSchedulingState]
	corrections atomic.Pointer[map[int64]map[int64]float64]
	// correctionRuns records, per group, the last successful correction
	// query; only the refresh loop touches it.
	correctionRuns map[int64]shareCorrectionRun
	// ineligible holds, per account, when it was last seen failing a health
	// gate. Tier return waits until an account has been healthy for
	// tiers.stable_seconds.
	ineligible sync.Map
	stats      *shareStatsRecorder
	traces     *shareTraceTable
	// attribution bounds the concurrent session-history lookups of cache
	// attribution; observations beyond it are dropped, never queued.
	attribution chan struct{}
	// shadows bounds the concurrent shadow comparisons the same way: under
	// load the agreement rate is computed from fewer requests instead of
	// piling up goroutines.
	shadows chan struct{}
	// gateway and openai give the admin operations the live candidate gates.
	gateway *GatewayService
	openai  *OpenAIGatewayService
	// meterIO is set when the repository timing hooks are installed
	// (server.enable_server_timing). Without them a selection's Redis and
	// database calls are invisible, so only its duration is recorded.
	meterIO bool

	mu sync.Mutex
	// settingsEpoch counts settings writes; a reload that read settings
	// before a write must not publish them over it. settingsWriteMu keeps
	// stored and published settings in the same order.
	settingsEpoch   uint64
	settingsWriteMu sync.Mutex
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	startOnce       sync.Once
	stopOnce        sync.Once
}

// NewShareSchedulingService creates the service. Start loads state and
// launches the background loops.
func NewShareSchedulingService(repo ShareSchedulingRepository, store ShareSchedulingStore, settings SettingRepository) *ShareSchedulingService {
	node, _ := os.Hostname()
	s := &ShareSchedulingService{repo: repo, store: store, settings: settings, node: node,
		stats: newShareStatsRecorder(), traces: newShareTraceTable(), attribution: make(chan struct{}, shareAttributionConcurrency),
		shadows: make(chan struct{}, shareShadowConcurrency)}
	s.state.Store(&shareSchedulingState{settings: DefaultShareSchedulingSettings(), groups: map[int64]*shareGroupPolicy{}})
	empty := map[int64]map[int64]float64{}
	s.corrections.Store(&empty)
	return s
}

// Start loads policies and settings and launches refresh, broadcast,
// correction and statistics loops.
func (s *ShareSchedulingService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		loadCtx, loadCancel := context.WithTimeout(ctx, shareSchedulingLoadTimeout)
		if err := s.reloadAll(loadCtx); err != nil {
			slog.Warn("share_scheduling.initial_load_failed", "error", err)
		}
		loadCancel()
		if s.store != nil {
			s.store.SubscribeChanges(ctx, func(groupID int64) {
				reloadCtx, reloadCancel := context.WithTimeout(ctx, shareSchedulingLoadTimeout)
				defer reloadCancel()
				var err error
				if groupID == shareSchedulingSettingsChange {
					err = s.reloadSettings(reloadCtx)
				} else {
					err = s.reloadGroup(reloadCtx, groupID)
				}
				if err != nil {
					slog.Warn("share_scheduling.broadcast_reload_failed", "group_id", groupID, "error", err)
				}
			})
		}
		s.wg.Add(2)
		go s.refreshLoop(ctx)
		go s.flushLoop(ctx)
	})
}

// Stop ends the background loops and flushes pending statistics.
func (s *ShareSchedulingService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
		flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.flushStats(flushCtx)
	})
}

func (s *ShareSchedulingService) refreshLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(shareSchedulingRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			loadCtx, cancel := context.WithTimeout(ctx, shareSchedulingLoadTimeout)
			if err := s.reloadAll(loadCtx); err != nil {
				slog.Warn("share_scheduling.refresh_failed", "error", err)
			}
			cancel()
			s.refreshCorrections(ctx, time.Now())
		}
	}
}

func (s *ShareSchedulingService) flushLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(shareSchedulingFlushInterval)
	defer ticker.Stop()
	lastSweep := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			flushCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			s.flushStats(flushCtx)
			cancel()
			if now.Sub(lastSweep) >= time.Minute {
				s.traces.sweep(now)
				lastSweep = now
			}
		}
	}
}

// flushStats writes the recorder's batch. Statistics are best effort: a
// failed write is dropped and counted, so the report shows the gap.
func (s *ShareSchedulingService) flushStats(ctx context.Context) {
	batch := s.stats.drain()
	if len(batch.Counters) == 0 && len(batch.Decisions) == 0 {
		return
	}
	if s.store == nil {
		return
	}
	if err := s.store.WriteStats(ctx, batch); err != nil {
		s.stats.addDropped(int64(len(batch.Counters)))
		slog.Debug("share_scheduling.stats_flush_failed", "error", err)
	}
}

// reloadAll replaces the whole snapshot. A failure keeps the previous one.
func (s *ShareSchedulingService) reloadAll(ctx context.Context) error {
	s.mu.Lock()
	epoch, before := s.settingsEpoch, s.state.Load()
	s.mu.Unlock()
	versions := map[int64]int64{}
	settings, settingsErr := s.loadSettings(ctx)
	var groups map[int64]*shareGroupPolicy
	var groupsErr error
	if s.repo != nil {
		var rows []ShareSchedulingPolicyRow
		rows, groupsErr = s.repo.ListPolicies(ctx)
		if groupsErr == nil {
			groups = make(map[int64]*shareGroupPolicy, len(rows))
			for i := range rows {
				versions[rows[i].GroupID] = rows[i].Version
				if gp := decodeShareGroupPolicy(&rows[i]); gp != nil {
					groups[gp.groupID] = gp
				}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Load()
	next := &shareSchedulingState{settings: current.settings, groups: current.groups}
	if settingsErr == nil && epoch == s.settingsEpoch {
		next.settings = settings
	}
	if groups != nil {
		// Keep what a save published while this reload read the table: a
		// newer version, or a group whose row the listing did not include.
		for id, previous := range current.groups {
			version, exists := versions[id]
			if (exists && previous.version > version) || (!exists && before.groups[id] != previous) {
				groups[id] = previous
			}
		}
		next.groups = groups
	}
	s.state.Store(next)
	return errors.Join(settingsErr, groupsErr)
}

func (s *ShareSchedulingService) reloadGroup(ctx context.Context, groupID int64) error {
	if s.repo == nil {
		return nil
	}
	row, err := s.repo.GetPolicy(ctx, groupID)
	if err != nil {
		return err
	}
	s.installGroup(row)
	return nil
}

// installGroup publishes the committed snapshot without another DB read;
// delayed notifications must never replace a newer policy version.
func (s *ShareSchedulingService) installGroup(row *ShareSchedulingPolicyRow) {
	if row == nil {
		return
	}
	gp := decodeShareGroupPolicy(row)
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Load()
	if previous := current.groups[row.GroupID]; previous != nil && previous.version > row.Version {
		return
	}
	groups := make(map[int64]*shareGroupPolicy, len(current.groups)+1)
	for id, old := range current.groups {
		groups[id] = old
	}
	delete(groups, row.GroupID)
	if gp != nil {
		groups[row.GroupID] = gp
	}
	s.state.Store(&shareSchedulingState{settings: current.settings, groups: groups})
}

func (s *ShareSchedulingService) reloadSettings(ctx context.Context) error {
	s.mu.Lock()
	epoch := s.settingsEpoch
	s.mu.Unlock()
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Load()
	if epoch == s.settingsEpoch {
		s.state.Store(&shareSchedulingState{settings: settings, groups: current.groups})
	}
	return nil
}

func (s *ShareSchedulingService) loadSettings(ctx context.Context) (ShareSchedulingSettings, error) {
	settings := DefaultShareSchedulingSettings()
	if s.settings == nil {
		return settings, nil
	}
	raw, err := s.settings.GetValue(ctx, shareSchedulingSettingsKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return settings, nil
		}
		return settings, err
	}
	if raw == "" {
		return settings, nil
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return DefaultShareSchedulingSettings(), fmt.Errorf("decode %s: %w", shareSchedulingSettingsKey, err)
	}
	if settings.Validate() != nil {
		settings.AttributionSampleRate = DefaultShareSchedulingSettings().AttributionSampleRate
	}
	return settings, nil
}

// decodeShareGroupPolicy turns a stored row into runtime form. Rows that no
// longer decode stay on the original scheduler instead of failing the load.
func decodeShareGroupPolicy(row *ShareSchedulingPolicyRow) *shareGroupPolicy {
	policy, err := sharesched.DecodePolicy(row.Policy)
	if err != nil {
		slog.Warn("share_scheduling.policy_decode_failed", "group_id", row.GroupID, "version", row.Version, "error", err)
		return nil
	}
	policy.Freeze()
	return &shareGroupPolicy{groupID: row.GroupID, version: row.Version, policy: policy}
}

// Settings returns the current global settings.
func (s *ShareSchedulingService) Settings() ShareSchedulingSettings {
	if s == nil {
		return DefaultShareSchedulingSettings()
	}
	return s.state.Load().settings
}

// groupPolicy returns the group's policy when share or shadow scheduling is
// in effect, nil when the group uses the original scheduler.
func (s *ShareSchedulingService) groupPolicy(groupID int64) *shareGroupPolicy {
	if s == nil {
		return nil
	}
	state := s.state.Load()
	if !state.settings.Enabled {
		return nil
	}
	gp := state.groups[groupID]
	if gp == nil || gp.policy.Scheduler == sharesched.SchedulerSub2API {
		return nil
	}
	return gp
}

// shareSchedulingActive reports whether the group routes by share scheduling.
func (s *ShareSchedulingService) shareSchedulingActive(groupID int64) bool {
	gp := s.groupPolicy(groupID)
	return gp != nil && gp.policy.Scheduler == sharesched.SchedulerShare
}

// shareHealthReason reports whether an exclusion reflects the account's own
// health. Only those delay tier return; request-specific gates such as model
// support or channel restrictions say nothing about stability.
func shareHealthReason(reason sharesched.Reason) bool {
	switch reason {
	case sharesched.ReasonRateLimited, sharesched.ReasonOverloaded, sharesched.ReasonTempUnschedulable,
		sharesched.ReasonNotSchedulable, sharesched.ReasonInactive, sharesched.ReasonDisabled,
		sharesched.ReasonExpired, sharesched.ReasonQuotaExceeded, sharesched.ReasonIQDegraded,
		sharesched.ReasonSchedulingThreshold, sharesched.ReasonRuntimeBlocked, sharesched.ReasonProxyQuarantined,
		sharesched.ReasonBalanceExhausted, sharesched.ReasonQuotaAutoPause:
		return true
	}
	return false
}

// observeEligibility records health observations and returns when an
// eligible account last failed a health gate (zero when never seen).
func (s *ShareSchedulingService) observeEligibility(accountID int64, eligible bool, reason sharesched.Reason, now time.Time) time.Time {
	if !eligible {
		if shareHealthReason(reason) {
			s.ineligible.Store(accountID, now.UnixNano())
		}
		return time.Time{}
	}
	if v, ok := s.ineligible.Load(accountID); ok {
		if nanos, ok := v.(int64); ok {
			return time.Unix(0, nanos)
		}
	}
	return time.Time{}
}

// correctionsFor returns the bounded correction factors of a group.
func (s *ShareSchedulingService) correctionsFor(groupID int64) map[int64]float64 {
	if s == nil {
		return nil
	}
	return (*s.corrections.Load())[groupID]
}

// shareCorrectionRun is the last successful correction query of a group.
type shareCorrectionRun struct {
	at      time.Time
	version int64
}

const (
	shareCorrectionMinInterval  = 30 * time.Second
	shareCorrectionMaxInterval  = 15 * time.Minute
	shareCorrectionQueryTimeout = 3 * time.Second
	shareCorrectionBudget       = 10 * time.Second
	shareCorrectionMinStale     = 10 * time.Minute
)

// shareCorrectionInterval spaces the usage aggregation of one group: the
// factors follow a window-long average, so re-reading the whole window every
// 30 seconds would only add database load.
func shareCorrectionInterval(windowMinutes int) time.Duration {
	interval := time.Duration(windowMinutes) * time.Minute / 12
	switch {
	case interval < shareCorrectionMinInterval:
		return shareCorrectionMinInterval
	case interval > shareCorrectionMaxInterval:
		return shareCorrectionMaxInterval
	}
	return interval
}

// refreshCorrections recomputes the optional share correction of active
// groups whose interval has passed or whose policy changed, each query with
// its own timeout and the whole pass within shareCorrectionBudget so a slow
// database cannot hold up the policy refresh. A failed or unreached query
// keeps the previous factors until they are a window (at least ten minutes)
// old; after that the group runs uncorrected.
func (s *ShareSchedulingService) refreshCorrections(ctx context.Context, now time.Time) {
	if s.repo == nil {
		return
	}
	ctx, cancelAll := context.WithTimeout(ctx, shareCorrectionBudget)
	defer cancelAll()
	state := s.state.Load()
	next := make(map[int64]map[int64]float64)
	previous := *s.corrections.Load()
	runs := make(map[int64]shareCorrectionRun, len(s.correctionRuns))
	keep := func(groupID int64, run shareCorrectionRun) {
		runs[groupID] = run
		if factors, ok := previous[groupID]; ok {
			next[groupID] = factors
		}
	}
	for groupID, gp := range state.groups {
		balance := gp.policy.Balance
		if !state.settings.Enabled || gp.policy.Scheduler == sharesched.SchedulerSub2API || balance.Correction == sharesched.CorrectionOff {
			continue
		}
		window := time.Duration(balance.WindowMinutes) * time.Minute
		stale := window
		if stale < shareCorrectionMinStale {
			stale = shareCorrectionMinStale
		}
		run, ran := s.correctionRuns[groupID]
		ran = ran && run.version == gp.version
		if ran && now.Sub(run.at) < shareCorrectionInterval(balance.WindowMinutes) {
			keep(groupID, run)
			continue
		}
		queryCtx, cancel := context.WithTimeout(ctx, shareCorrectionQueryTimeout)
		usage, err := s.repo.AccountUsage(queryCtx, groupID, now.Add(-window))
		cancel()
		if err != nil {
			slog.Debug("share_scheduling.correction_usage_failed", "group_id", groupID, "error", err)
			if ran && now.Sub(run.at) <= stale {
				keep(groupID, run)
			}
			continue
		}
		runs[groupID] = shareCorrectionRun{at: now, version: gp.version}
		if factors := shareCorrectionFactors(&gp.policy, usage); len(factors) > 0 {
			next[groupID] = factors
		}
	}
	s.correctionRuns = runs
	s.corrections.Store(&next)
}

// shareCorrectionFactors compares each configured account's share of the
// window volume with its target share inside its tier. Accounts without
// configured rules and windows below min_volume are left uncorrected.
func shareCorrectionFactors(p *sharesched.Policy, usage map[int64]ShareSchedulingUsage) map[int64]float64 {
	volume := func(u ShareSchedulingUsage) float64 {
		switch p.Balance.Unit {
		case sharesched.UnitTokens:
			return float64(u.Tokens)
		case sharesched.UnitStandardCost:
			return u.StandardCost
		default:
			return float64(u.Requests)
		}
	}
	type tierTotals struct{ weight, volume float64 }
	tiers := map[int]*tierTotals{}
	var total float64
	for _, rule := range p.Accounts {
		if rule.Drain {
			continue
		}
		t := tiers[rule.Tier]
		if t == nil {
			t = &tierTotals{}
			tiers[rule.Tier] = t
		}
		v := volume(usage[rule.AccountID])
		t.weight += float64(rule.Weight)
		t.volume += v
		total += v
	}
	if total < float64(p.Balance.MinVolume) || total <= 0 {
		return nil
	}
	factors := make(map[int64]float64, len(p.Accounts))
	for _, rule := range p.Accounts {
		t := tiers[rule.Tier]
		if rule.Drain || t == nil || t.weight <= 0 || t.volume <= 0 {
			continue
		}
		target := float64(rule.Weight) / t.weight
		actual := volume(usage[rule.AccountID]) / t.volume
		if f := sharesched.CorrectionFactor(p.Balance.Correction, target, actual); f != 1 {
			factors[rule.AccountID] = f
		}
	}
	return factors
}

// publishChange broadcasts a change; other instances also converge through
// the periodic refresh when the broadcast is lost.
func (s *ShareSchedulingService) publishChange(ctx context.Context, groupID int64) {
	if s.store == nil {
		return
	}
	if err := s.store.PublishChange(ctx, groupID); err != nil {
		slog.Warn("share_scheduling.broadcast_failed", "group_id", groupID, "error", err)
	}
}

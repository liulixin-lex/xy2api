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
	// shareAttributionSampleRate samples one session in four for cache
	// attribution (the 0.3.0–0.4.3 default, now fixed).
	shareAttributionSampleRate = 4
)

// Retention of the hourly counter buckets. Cache attribution runs for every
// group and is the baseline of the report, so it is kept for eight days plus
// two hours (design §8); share decision counters only exist for share groups.
const (
	ShareCacheCounterRetention = 194 * time.Hour
	ShareShareCounterRetention = 26 * time.Hour
	// ShareSwitchAnchorTTL keeps the time of the last scheduler switch as
	// long as the cache counters it is compared with.
	ShareSwitchAnchorTTL = ShareCacheCounterRetention
)

// shareSchedulingSettingsChange is the broadcast id of a settings change;
// group ids are never negative.
const shareSchedulingSettingsChange int64 = -1

// ErrShareSchedulingVersionConflict reports a lost compare-and-swap on a
// group policy: another administrator saved first.
var ErrShareSchedulingVersionConflict = infraerrors.Conflict("SHARE_SCHEDULING_VERSION_CONFLICT", "share scheduling policy was changed by someone else; reload and retry")

// ShareSchedulingSettings are the global share-scheduling settings. The
// stored document may still hold the switches of 0.3.0–0.4.3
// (cache_attribution, attribution_sample_rate, decision_samples); they are
// ignored and kept on save, so a rollback reads them unchanged.
type ShareSchedulingSettings struct {
	// Enabled is the master switch. While off every group uses the original
	// Sub2API scheduler whatever its policy says.
	Enabled bool `json:"enabled"`
}

// DefaultShareSchedulingSettings keeps routing on Sub2API.
func DefaultShareSchedulingSettings() ShareSchedulingSettings {
	return ShareSchedulingSettings{}
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

// ShareSchedulingGroupSummary is one group's successful traffic over a
// window, from usage_logs.
type ShareSchedulingGroupSummary struct {
	Requests int64
	// CacheReadTokens over PromptTokens (input + cache read + cache
	// creation) is the cache hit ratio.
	CacheReadTokens int64
	PromptTokens    int64
}

// ShareSchedulingGroupMetrics are the database metrics of one group over a
// window: its successful requests (usage_logs) and its client-visible
// errors (ops_error_logs, the condition of the account recent health).
type ShareSchedulingGroupMetrics struct {
	ShareSchedulingGroupSummary
	// TTFTP50Ms and TTFTP95Ms are first-token percentiles of the requests
	// that have one (streaming); nil without samples.
	TTFTP50Ms   *int
	TTFTP95Ms   *int
	TTFTSamples int64
	Errors      int64
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
	// AccountUsage aggregates usage_logs of a group per account over
	// [from, to).
	AccountUsage(ctx context.Context, groupID int64, from, to time.Time) (map[int64]ShareSchedulingUsage, error)
	// GroupSummaries aggregates usage_logs per group since a time, for every
	// group in one query.
	GroupSummaries(ctx context.Context, since time.Time) (map[int64]ShareSchedulingGroupSummary, error)
	// GroupMetrics reads the database metrics of a group over [from, to).
	GroupMetrics(ctx context.Context, groupID int64, from, to time.Time) (ShareSchedulingGroupMetrics, error)
	// AccountAttempts reads the successful requests (usage_logs) and the
	// failed upstream attempts (ops_error_logs.upstream_errors) of the
	// accounts over [from, to), for the health factor (design §11.1).
	AccountAttempts(ctx context.Context, accountIDs []int64, from, to time.Time) (ShareSchedulingAccountAttempts, error)
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
}

// ShareSchedulingStore is the Redis side: change broadcast, statistics,
// session memory and the switch anchor. Every method may fail; callers drop
// statistics on error.
type ShareSchedulingStore interface {
	PublishChange(ctx context.Context, groupID int64) error
	SubscribeChanges(ctx context.Context, handler func(groupID int64))
	WriteStats(ctx context.Context, batch ShareSchedulingStatsBatch) error
	// ReadCounters sums a window that ends now: minute buckets up to two
	// hours, whole hours from the nearest one beyond. An exact read counts
	// nothing before from: beyond two hours, the partial hour at from is
	// read minute by minute while those buckets are kept, and left out
	// otherwise.
	ReadCounters(ctx context.Context, kind string, groupID int64, from, to time.Time, exact bool) (map[string]int64, error)
	// ReadHourCounters sums the hourly buckets [fromHour, toHour), in Unix
	// hours, of a window in the past.
	ReadHourCounters(ctx context.Context, kind string, groupID int64, fromHour, toHour int64) (map[string]int64, error)
	// SwapCacheSession stores the latest request of a sampled session and
	// returns the previous one (nil when none).
	SwapCacheSession(ctx context.Context, groupID int64, fingerprint string, value []byte, ttl time.Duration) ([]byte, error)
	// RecordSwitch stores when the group last changed its scheduler.
	RecordSwitch(ctx context.Context, groupID int64, at time.Time) error
	// SwitchedAt returns the stored switch time; zero when none is stored.
	SwitchedAt(ctx context.Context, groupID int64) (time.Time, error)
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

	state atomic.Pointer[shareSchedulingState]
	// ineligible holds, per account, when it was last seen failing a health
	// gate. Tier return waits until an account has been healthy for
	// sharesched.StableFor.
	ineligible sync.Map
	stats      *shareStatsRecorder
	traces     *shareTraceTable
	// attribution bounds the concurrent session-history lookups of cache
	// attribution; observations beyond it are dropped, never queued.
	attribution chan struct{}
	// admin caches the one-hour summaries of the admin views (design §7).
	admin shareAdminCache
	// attributionRate samples one session in attributionRate for cache
	// attribution (shareAttributionSampleRate; tests sample every one).
	attributionRate int
	// health is the latest adaptive-weight refresh (design §11); outages
	// and healthMu belong to the refresh.
	health     atomic.Pointer[shareHealthSnapshot]
	healthKick chan struct{}
	outages    map[int64]shareOutageObservation
	healthMu   sync.Mutex
	opsEnabled bool
	// gateway and openai give the admin operations the live candidate gates.
	gateway *GatewayService
	openai  *OpenAIGatewayService

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
		attributionRate: shareAttributionSampleRate, opsEnabled: true, healthKick: make(chan struct{}, 1)}
	s.state.Store(&shareSchedulingState{settings: DefaultShareSchedulingSettings(), groups: map[int64]*shareGroupPolicy{}})
	return s
}

// Start loads policies and settings and launches the refresh, broadcast and
// statistics loops.
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
			s.store.SubscribeChanges(ctx, func(groupID int64) { s.onChange(ctx, groupID) })
		}
		s.wg.Add(3)
		go s.refreshLoop(ctx)
		go s.flushLoop(ctx)
		go s.healthLoop(ctx)
	})
}

// onChange applies a broadcast change of a group policy or of the
// settings.
func (s *ShareSchedulingService) onChange(ctx context.Context, groupID int64) {
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
		return
	}
	// A group that just started running share scheduling gets its health
	// now, not at the next refresh.
	s.kickHealthIfNeeded(time.Now())
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
	if len(batch.Counters) == 0 {
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
	// Fields of earlier versions are ignored.
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return DefaultShareSchedulingSettings(), fmt.Errorf("decode %s: %w", shareSchedulingSettingsKey, err)
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
	return &shareGroupPolicy{groupID: row.GroupID, version: row.Version, policy: policy}
}

// Settings returns the current global settings.
func (s *ShareSchedulingService) Settings() ShareSchedulingSettings {
	if s == nil {
		return DefaultShareSchedulingSettings()
	}
	return s.state.Load().settings
}

// groupPolicy returns the group's policy when share scheduling is in effect,
// nil when the group uses the original scheduler. A stored shadow policy runs
// the original scheduler (design §3.3).
func (s *ShareSchedulingService) groupPolicy(groupID int64) *shareGroupPolicy {
	if s == nil {
		return nil
	}
	state := s.state.Load()
	if !state.settings.Enabled {
		return nil
	}
	gp := state.groups[groupID]
	if gp == nil || gp.policy.Effective() != sharesched.SchedulerShare {
		return nil
	}
	return gp
}

// shareSchedulingActive reports whether the group routes by share scheduling.
func (s *ShareSchedulingService) shareSchedulingActive(groupID int64) bool {
	return s.groupPolicy(groupID) != nil
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

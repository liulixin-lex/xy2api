package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const (
	resilienceSettingsRefreshInterval = 10 * time.Second
	// resilienceSessionMarkSweep removes expired session marks of sessions
	// that never came back (lookups only drop the marks they touch).
	resilienceSessionMarkSweep = time.Minute
	resilienceLoadTimeout      = 5 * time.Second
)

// ResilienceStore is the Redis side of the resilience layer. Every method may
// fail; callers fall back to the 0.3.0 behaviour on error.
type ResilienceStore interface {
	// IncrSessionFailure increments the failure count of an account within a
	// session and returns the new count. The hash expires after window.
	IncrSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64, window time.Duration) (int64, error)
	// ClearSessionFailure resets the count of an account within a session.
	ClearSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64) error
	// SessionFailures returns all counts of a session (Explain).
	SessionFailures(ctx context.Context, groupID int64, sessionHash string) (map[int64]int64, error)
	// AddSessionAvoid puts an account into the session avoid set for ttl.
	AddSessionAvoid(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error
	// SessionAvoid returns the accounts a session currently avoids.
	SessionAvoid(ctx context.Context, groupID int64, sessionHash string) (map[int64]struct{}, error)

	// MarkGroupStable records, if absent, the moment a group became healthy
	// and returns the stored moment.
	MarkGroupStable(ctx context.Context, groupID int64, now time.Time) (time.Time, error)
	ClearGroupStable(ctx context.Context, groupID int64) error

	// IncrUndelivered counts undelivered requests of an API key in one hour.
	IncrUndelivered(ctx context.Context, apiKeyID int64, hour string) (int64, error)

	// MarkFallbackKey remembers for ttl that an API key was served by the
	// availability fallback group of groupID; FallbackKey reads and
	// ClearFallbackKey forgets it (design §5).
	MarkFallbackKey(ctx context.Context, groupID, apiKeyID int64, ttl time.Duration) error
	FallbackKey(ctx context.Context, groupID, apiKeyID int64) (bool, error)
	ClearFallbackKey(ctx context.Context, groupID, apiKeyID int64) error

	// SetSessionCacheRatio stores the latest cache-read share of a session.
	SetSessionCacheRatio(ctx context.Context, key string, ratio float64, ttl time.Duration) error
	// SessionCacheRatio returns the stored share, ok=false when absent.
	SessionCacheRatio(ctx context.Context, key string) (float64, bool, error)
}

// ResilienceService owns the gateway_resilience_settings lifecycle and the
// shared runtime state of the resilience layer. The request path only reads
// the atomically published settings snapshot (resilience.Current); a failed
// reload keeps the last known value and a cold start without storage uses
// the defaults.
type ResilienceService struct {
	settingRepo SettingRepository
	store       ResilienceStore

	negCache *resilience.NegativeCache
	fastFail *resilience.FastFail

	settingsWriteMu sync.Mutex
	settingsEpoch   atomic.Uint64

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once

	// background holds optional loops registered before Start.
	background []func(ctx context.Context)

	rateLimit        *RateLimitService
	modelBreakerOnce sync.Once
	modelBreaker     *openAIAccountModelTransientState

	lastResortGroups lastResortGroupCache
	// lastResortUsed records accounts recently served as last resort
	// (id -> time) so a success lifts their transient cooldown early.
	lastResortUsed sync.Map
	// failingSessions marks sessions with failure counts on this instance
	// (key -> expiry) so successes only reset counts when needed.
	failingSessions sync.Map
	// avoidingSessions marks sessions with avoid entries (key -> expiry).
	avoidingSessions sync.Map
	// postOutput counts recent failures after output per account for the
	// threshold cooldown (design §7.6).
	postOutput postOutputTracker

	// outputP99 is the per-model output-token P99 of recent usage
	// (lower-cased model -> tokens) for reservation estimates.
	outputP99 atomic.Pointer[map[string]int]

	// undelivered stores requests abandoned before any output.
	undelivered UndeliveredRepository

	// availability fallback groups (design §5)
	fallbackAccounts   AccountRepository
	fallbackGroups     GroupRepository
	fallbackGroupCache fallbackGroupCache
	// fallbackMoves records when this instance last moved a key of a group
	// to its fallback group (group id -> time).
	fallbackMoves sync.Map

	// pricingRestricted reports a model the group's channel pricing rejects
	// before any account is chosen (Explain only).
	pricingRestricted func(ctx context.Context, groupID int64, model string) bool
}

// NewResilienceService creates the service; Start loads the settings and
// launches the refresh loops.
func NewResilienceService(settingRepo SettingRepository, store ResilienceStore) *ResilienceService {
	return &ResilienceService{
		settingRepo: settingRepo,
		store:       store,
		negCache:    resilience.ProcessNegativeCache(),
		fastFail:    resilience.ProcessFastFail(),
	}
}

// Settings returns the published snapshot.
func (s *ResilienceService) Settings() *resilience.Settings { return resilience.Current() }

// Store returns the Redis store (may be nil).
func (s *ResilienceService) Store() ResilienceStore {
	if s == nil {
		return nil
	}
	return s.store
}

// NegativeCache returns the process-wide no-account cache.
func (s *ResilienceService) NegativeCache() *resilience.NegativeCache {
	if s == nil {
		return nil
	}
	return s.negCache
}

// FastFail returns the process-wide per-key fast-fail tracker.
func (s *ResilienceService) FastFail() *resilience.FastFail {
	if s == nil {
		return nil
	}
	return s.fastFail
}

// AddBackgroundLoop registers a loop started by Start (and stopped by Stop).
// It must be called before Start.
func (s *ResilienceService) AddBackgroundLoop(loop func(ctx context.Context)) {
	if s == nil || loop == nil {
		return
	}
	s.background = append(s.background, loop)
}

// Start loads the settings and starts the refresh loops.
func (s *ResilienceService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		loadCtx, loadCancel := context.WithTimeout(ctx, resilienceLoadTimeout)
		if err := s.reloadSettings(loadCtx); err != nil {
			slog.Warn("resilience.initial_settings_load_failed", "error", err)
		}
		loadCancel()
		s.wg.Add(2)
		go s.settingsLoop(ctx)
		go s.sessionMarkLoop(ctx)
		for _, loop := range s.background {
			s.wg.Add(1)
			go func(loop func(context.Context)) {
				defer s.wg.Done()
				loop(ctx)
			}(loop)
		}
	})
}

// Stop ends the background loops.
func (s *ResilienceService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}

func (s *ResilienceService) settingsLoop(ctx context.Context) {
	defer s.wg.Done()
	t := time.NewTicker(resilienceSettingsRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			loadCtx, cancel := context.WithTimeout(ctx, resilienceLoadTimeout)
			if err := s.reloadSettings(loadCtx); err != nil {
				slog.Warn("resilience.settings_refresh_failed", "error", err)
			}
			cancel()
		}
	}
}

// reloadSettings reads the stored document. Invalid or unreadable documents
// keep the last published value.
func (s *ResilienceService) reloadSettings(ctx context.Context) error {
	if s.settingRepo == nil {
		return nil
	}
	epoch := s.settingsEpoch.Load()
	raw, err := s.settingRepo.GetValue(ctx, resilience.SettingKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			raw = ""
		} else {
			return err
		}
	}
	parsed, err := resilience.Parse(raw)
	if err != nil {
		return err
	}
	s.settingsWriteMu.Lock()
	defer s.settingsWriteMu.Unlock()
	// A save that happened after this read already published newer values.
	if s.settingsEpoch.Load() != epoch {
		return nil
	}
	resilience.Publish(parsed)
	return nil
}

// GetSettings returns the effective settings.
func (s *ResilienceService) GetSettings() resilience.Settings {
	return resilience.Current().Clone()
}

// UpdateSettings validates, stores and publishes a new document.
func (s *ResilienceService) UpdateSettings(ctx context.Context, next resilience.Settings) (resilience.Settings, error) {
	next = next.Normalized()
	if err := next.Validate(); err != nil {
		return resilience.Settings{}, infraerrors.BadRequest("GATEWAY_RESILIENCE_SETTINGS_INVALID", err.Error())
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return resilience.Settings{}, err
	}
	s.settingsWriteMu.Lock()
	defer s.settingsWriteMu.Unlock()
	if s.settingRepo != nil {
		if err := s.settingRepo.Set(ctx, resilience.SettingKey, string(raw)); err != nil {
			return resilience.Settings{}, err
		}
	}
	s.settingsEpoch.Add(1)
	resilience.Publish(next)
	// Settings changes may change who is available.
	s.negCache.InvalidateGroup(0)
	return resilience.Current().Clone(), nil
}

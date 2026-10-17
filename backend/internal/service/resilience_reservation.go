package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Cache-aware reservation estimate (design §9.5).
const (
	// sessionCacheRatioTTL keeps the latest cache-read share of a session.
	sessionCacheRatioTTL = 30 * time.Minute
	// sessionCacheRatioTimeout bounds the Redis read on the request path.
	sessionCacheRatioTimeout = 50 * time.Millisecond

	// outputP99Window is the usage history the output cap is computed from.
	outputP99Window = 7 * 24 * time.Hour
	// outputP99MinSamples is the minimum history for a model to be capped.
	outputP99MinSamples = 50
	// outputP99Floor is the lowest output cap.
	outputP99Floor = 1024
	// outputP99Refresh is how often the caps are recomputed.
	outputP99Refresh = time.Hour
	// outputP99QueryTimeout bounds the aggregation query.
	outputP99QueryTimeout = 30 * time.Second
)

// SetCacheSession attaches the client session identifier of the request so
// the billing task can record the session's cache-read share.
func (r *InflightReservation) SetCacheSession(sessionKey string) {
	if r == nil {
		return
	}
	r.cacheSession = sessionCacheRatioKey(r.userID, sessionKey)
}

// sessionCacheRatioKey scopes a client session identifier to its user; it
// returns "" when there is no identifier.
func sessionCacheRatioKey(userID int64, sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if userID <= 0 || sessionKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionKey))
	return strconv.FormatInt(userID, 10) + ":" + hex.EncodeToString(sum[:12])
}

// sessionCacheRatio returns the cache-read share of the session's latest
// request; ok=false when unknown or the feature is off.
func (r *ResilienceService) sessionCacheRatio(ctx context.Context, userID int64, sessionKey string) (float64, bool) {
	if r == nil || r.store == nil || !resilience.Current().CacheAwareEstimateOn() {
		return 0, false
	}
	key := sessionCacheRatioKey(userID, sessionKey)
	if key == "" {
		return 0, false
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCacheRatioTimeout)
	defer cancel()
	ratio, ok, err := r.store.SessionCacheRatio(readCtx, key)
	if err != nil || !ok || ratio <= 0 {
		return 0, false
	}
	return min(ratio, 1), true
}

// NoteSessionCacheUsage records the cache-read share of a billed request
// whose reservation carries a client session (design §9.5). total is the
// whole prompt: uncached input, cache reads and cache writes.
func (r *ResilienceService) NoteSessionCacheUsage(ctx context.Context, cacheRead, total int) {
	if r == nil || r.store == nil || total <= 0 || !resilience.Current().CacheAwareEstimateOn() {
		return
	}
	res := InflightReservationFromContext(ctx)
	if res == nil || res.cacheSession == "" {
		return
	}
	ratio := float64(max(cacheRead, 0)) / float64(total)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := r.store.SetSessionCacheRatio(writeCtx, res.cacheSession, min(ratio, 1), sessionCacheRatioTTL); err != nil {
		slog.Debug("resilience.cache_ratio_store_failed", "error", err)
	}
}

// outputP99Cap returns the output-token cap of a model: its recent P99 with
// a floor, or 0 when unknown or the feature is off.
func (r *ResilienceService) outputP99Cap(model string) int {
	if r == nil || !resilience.Current().OutputP99CapOn() {
		return 0
	}
	caps := r.outputP99.Load()
	if caps == nil {
		return 0
	}
	p99, ok := (*caps)[strings.ToLower(strings.TrimSpace(model))]
	if !ok || p99 <= 0 {
		return 0
	}
	return max(p99, outputP99Floor)
}

// ResilienceRepository is the SQL side of the resilience layer.
type ResilienceRepository interface {
	// OutputTokenP99ByModel returns the P99 output tokens per requested
	// model since the given time, for models with at least minSamples
	// requests.
	OutputTokenP99ByModel(ctx context.Context, since time.Time, minSamples int) (map[string]int, error)
}

// ConfigureOutputP99 registers the loop that recomputes the per-model
// output caps from usage history.
func (r *ResilienceService) ConfigureOutputP99(repo ResilienceRepository) {
	if r == nil || repo == nil {
		return
	}
	r.AddBackgroundLoop(func(ctx context.Context) {
		// Spread the first query away from start-up work.
		first := time.NewTimer(30*time.Second + rand.N(30*time.Second))
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		}
		r.refreshOutputP99(ctx, repo)
		t := time.NewTicker(outputP99Refresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.refreshOutputP99(ctx, repo)
			}
		}
	})
}

func (r *ResilienceService) refreshOutputP99(ctx context.Context, repo ResilienceRepository) {
	if !resilience.Current().OutputP99CapOn() {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, outputP99QueryTimeout)
	defer cancel()
	caps, err := repo.OutputTokenP99ByModel(qctx, time.Now().Add(-outputP99Window), outputP99MinSamples)
	if err != nil {
		// Keep the last known caps.
		slog.Warn("resilience.output_p99_refresh_failed", "error", err)
		return
	}
	normalized := make(map[string]int, len(caps))
	for model, p99 := range caps {
		normalized[strings.ToLower(strings.TrimSpace(model))] = p99
	}
	r.outputP99.Store(&normalized)
}

// ResilienceStats is the payload of GET /admin/resilience/stats (design §12).
type ResilienceStats struct {
	WindowSeconds int64                  `json:"window_seconds"`
	Scope         string                 `json:"scope"`
	Counters      []resilience.MetricRow `json:"counters"`
}

// Stats sums this instance's resilience counters over window.
func (r *ResilienceService) Stats(window time.Duration) ResilienceStats {
	if window <= 0 || window > resilience.MetricsRetention {
		window = resilience.MetricsRetention
	}
	return ResilienceStats{
		WindowSeconds: int64(window / time.Second),
		Scope:         "instance",
		Counters:      resilience.Snapshot(window),
	}
}

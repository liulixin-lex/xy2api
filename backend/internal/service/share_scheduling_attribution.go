package service

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

const (
	shareAttributionConcurrency = 64
	shareAttributionTimeout     = 2 * time.Second
	// shareCacheSessionRetention keeps a session's last placement long
	// enough to tell an expired cache from a new conversation.
	shareCacheSessionRetention = 6 * time.Hour

	shareStatsClassPrefix        = "class:"
	shareStatsClassTokensPrefix  = "class_tokens:"
	shareStatsPromptTokensField  = "prompt_tokens"
	shareStatsCacheReadField     = "cache_read_tokens"
	shareStatsCacheCreationField = "cache_creation_tokens"
	// shareStatsTTFTPrefix + reason + ":" + bucket upper bound in ms counts
	// first-token latencies; "inf" is the overflow bucket.
	shareStatsTTFTPrefix = "ttft:"
	// shareStatsAccountTTFTPrefix + account + ":" + bucket is the same
	// histogram per serving account.
	shareStatsAccountTTFTPrefix = "acct_ttft:"
	shareTTFTOriginal           = "original"

	// shareStatsSelectionPrefix + scheduler + ":" + field sums the cost of
	// sampled selections.
	shareStatsSelectionPrefix = "sel:"
	shareSelectionSamples     = "samples"
	shareSelectionMicros      = "us"
	shareSelectionIOSamples   = "io_samples"
	shareSelectionRedisTrips  = "redis_rtt"
	shareSelectionRedisMicros = "redis_us"
	shareSelectionDBQueries   = "db_queries"
)

// shareTTFTBucketsMs are the first-token histogram bounds.
var shareTTFTBucketsMs = []int{100, 200, 300, 500, 750, 1000, 1500, 2000, 3000, 5000, 8000, 13000, 20000, 30000}

// Cache attribution classes. A miss is attributed to the first cause that
// explains it: an expired cache before a switch, a switch before drift.
const (
	shareCacheHit                = "cache_hit"
	shareCacheBelowMin           = "below_min_cacheable"
	shareCacheMissSessionless    = "miss_sessionless"
	shareCacheMissNewSession     = "miss_new_session"
	shareCacheMissTTLExpired     = "miss_ttl_expired"
	shareCacheMissSwitchPolicy   = "miss_switch_policy"
	shareCacheMissSwitchSaturate = "miss_switch_saturated"
	shareCacheMissSwitchDown     = "miss_switch_unavailable"
	shareCacheMissSwitchFailover = "miss_switch_failover"
	shareCacheMissSwitchOther    = "miss_switch_other"
	shareCacheMissPrefixDrift    = "miss_prefix_drift"
)

// shareAttributionSampled samples whole sessions so a sampled session's
// history is complete; sessionless requests are sampled independently.
func shareAttributionSampled(sessionKey string, rate int) bool {
	if rate <= 1 {
		return true
	}
	if sessionKey == "" {
		return rand.IntN(rate) == 0
	}
	return xxhash.Sum64String(sessionKey)%uint64(rate) == 0
}

// shareCachePlacement is the last request of a sampled session: where it
// ran, when, the cache lifetime it established and its prompt size.
type shareCachePlacement struct {
	accountID int64
	at        time.Time
	ttl       time.Duration
	prompt    int
}

func (p shareCachePlacement) encode() []byte {
	return []byte(strconv.FormatInt(p.accountID, 10) + ":" + strconv.FormatInt(p.at.UnixMilli(), 10) + ":" +
		strconv.FormatInt(int64(p.ttl/time.Second), 10) + ":" + strconv.Itoa(p.prompt))
}

func decodeShareCachePlacement(raw []byte) (shareCachePlacement, bool) {
	parts := strings.Split(string(raw), ":")
	if len(parts) != 4 {
		return shareCachePlacement{}, false
	}
	accountID, err1 := strconv.ParseInt(parts[0], 10, 64)
	at, err2 := strconv.ParseInt(parts[1], 10, 64)
	ttl, err3 := strconv.ParseInt(parts[2], 10, 64)
	prompt, err4 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return shareCachePlacement{}, false
	}
	return shareCachePlacement{accountID: accountID, at: time.UnixMilli(at), ttl: time.Duration(ttl) * time.Second, prompt: prompt}, true
}

// shareCacheObservation is one sampled usage record.
type shareCacheObservation struct {
	trace         shareTrace
	accountID     int64
	model         string
	prompt        int
	cacheRead     int
	cacheCreation int
	oneHour       bool
	firstTokenMs  *int
	at            time.Time
}

// observeUsage attributes the cache outcome of a finished request to the
// placement that served it. It runs in the usage recorder and never blocks
// it: the session lookup runs asynchronously within a bounded budget.
func (s *ShareSchedulingService) observeUsage(ctx context.Context, log *UsageLog) {
	if s == nil || log == nil {
		return
	}
	requestID, _ := ctx.Value(ctxkey.RequestID).(string)
	trace, ok := s.traces.take(requestID)
	if !ok || trace.accountID != log.AccountID || !s.Settings().CacheAttribution {
		return
	}
	obs := shareCacheObservation{trace: trace, accountID: log.AccountID, model: log.Model,
		prompt:    log.InputTokens + log.CacheReadTokens + log.CacheCreationTokens,
		cacheRead: log.CacheReadTokens, cacheCreation: log.CacheCreationTokens,
		oneHour: log.CacheCreation1hTokens > 0, firstTokenMs: log.FirstTokenMs, at: time.Now()}
	s.recordTTFT(obs)
	if obs.trace.sessionKey == "" || s.store == nil {
		s.recordCacheClass(obs, s.classifyCache(obs, nil))
		return
	}
	select {
	case s.attribution <- struct{}{}:
	default:
		s.stats.add(shareStatsKindCache, trace.groupID, obs.at, shareStatsDroppedField, 1)
		return
	}
	go func() {
		defer func() { <-s.attribution }()
		lookupCtx, cancel := context.WithTimeout(context.Background(), shareAttributionTimeout)
		defer cancel()
		current := shareCachePlacement{accountID: obs.accountID, at: obs.at, ttl: s.cacheTTLFor(obs), prompt: obs.prompt}
		raw, err := s.store.SwapCacheSession(lookupCtx, trace.groupID, shareSessionFingerprint(trace.sessionKey),
			current.encode(), shareCacheSessionRetention)
		if err != nil {
			slog.Debug("share_scheduling.cache_session_swap_failed", "group_id", trace.groupID, "error", err)
			s.stats.add(shareStatsKindCache, trace.groupID, obs.at, shareStatsDroppedField, 1)
			return
		}
		var previous *shareCachePlacement
		if p, ok := decodeShareCachePlacement(raw); ok {
			previous = &p
		}
		s.recordCacheClass(obs, s.classifyCache(obs, previous))
	}()
}

// cacheTTLFor is the cache lifetime the request established upstream.
func (s *ShareSchedulingService) cacheTTLFor(obs shareCacheObservation) time.Duration {
	override := 0
	if gp := s.groupPolicy(obs.trace.groupID); gp != nil {
		override = gp.policy.Cache.TTLSeconds
	}
	return sharesched.CacheTTL(sharesched.CacheFamily(shareCacheFamily(obs.trace.platform)), obs.oneHour, override)
}

// classifyCache names why a request did or did not read the cache. On the
// same warm account a read below half of the previous prompt is prefix
// drift: the client or a rewrite changed the prefix.
func (s *ShareSchedulingService) classifyCache(obs shareCacheObservation, previous *shareCachePlacement) string {
	minimum := sharesched.MinCacheableTokens(sharesched.CacheFamily(shareCacheFamily(obs.trace.platform)), obs.model)
	warmSameAccount := previous != nil && previous.accountID == obs.accountID && obs.at.Sub(previous.at) <= previous.ttl
	switch {
	case obs.cacheRead == 0 && obs.prompt < minimum:
		return shareCacheBelowMin
	case warmSameAccount && previous.prompt >= minimum && obs.cacheRead*2 < previous.prompt:
		return shareCacheMissPrefixDrift
	case obs.cacheRead > 0:
		return shareCacheHit
	case obs.trace.sessionKey == "":
		return shareCacheMissSessionless
	case previous == nil || previous.prompt < minimum:
		return shareCacheMissNewSession
	case obs.at.Sub(previous.at) > previous.ttl:
		return shareCacheMissTTLExpired
	case previous.accountID != obs.accountID:
		return shareCacheSwitchClass(obs.trace)
	default:
		return shareCacheMissPrefixDrift
	}
}

// shareCacheSwitchClass attributes an account switch to the placement
// reason that caused it.
func shareCacheSwitchClass(trace shareTrace) string {
	switch trace.reason {
	case sharesched.ReasonFailoverNext:
		return shareCacheMissSwitchFailover
	case sharesched.ReasonSpillRebind, sharesched.ReasonSpillKeep, sharesched.ReasonTierOverflowSaturated:
		return shareCacheMissSwitchSaturate
	case sharesched.ReasonRebindUnavailable, sharesched.ReasonSoftSpill, sharesched.ReasonTierOverflowUnavailable:
		return shareCacheMissSwitchDown
	case sharesched.ReasonRealignCold, sharesched.ReasonReturnCold, sharesched.ReasonReturnImmediate,
		sharesched.ReasonReturnDeadline, sharesched.ReasonDrainCold, sharesched.ReasonDrainDeadline,
		sharesched.ReasonAdminRebalance, sharesched.ReasonModelRouting, sharesched.ReasonModelRoutingFallback,
		sharesched.ReasonNewSession:
		return shareCacheMissSwitchPolicy
	}
	if trace.switched {
		return shareCacheMissSwitchFailover
	}
	return shareCacheMissSwitchOther
}

func (s *ShareSchedulingService) recordCacheClass(obs shareCacheObservation, class string) {
	gid, at := obs.trace.groupID, obs.at
	s.stats.add(shareStatsKindCache, gid, at, shareStatsRequestsField, 1)
	s.stats.add(shareStatsKindCache, gid, at, shareStatsClassPrefix+class, 1)
	s.stats.add(shareStatsKindCache, gid, at, shareStatsClassTokensPrefix+class, int64(obs.prompt))
	s.stats.add(shareStatsKindCache, gid, at, shareStatsPromptTokensField, int64(obs.prompt))
	s.stats.add(shareStatsKindCache, gid, at, shareStatsCacheReadField, int64(obs.cacheRead))
	s.stats.add(shareStatsKindCache, gid, at, shareStatsCacheCreationField, int64(obs.cacheCreation))
}

// recordTTFT adds the first-token latency to the histogram of the decision
// that placed the request (original groups share one histogram) and to the
// serving account's histogram.
func (s *ShareSchedulingService) recordTTFT(obs shareCacheObservation) {
	if obs.firstTokenMs == nil || *obs.firstTokenMs < 0 {
		return
	}
	reason := string(obs.trace.reason)
	if reason == "" {
		reason = shareTTFTOriginal
	}
	bucket := shareTTFTBucket(*obs.firstTokenMs)
	s.stats.add(shareStatsKindCache, obs.trace.groupID, obs.at, shareStatsTTFTPrefix+reason+":"+bucket, 1)
	s.stats.add(shareStatsKindCache, obs.trace.groupID, obs.at, shareStatsAccountTTFTPrefix+strconv.FormatInt(obs.accountID, 10)+":"+bucket, 1)
}

func shareTTFTBucket(ms int) string {
	for _, bound := range shareTTFTBucketsMs {
		if ms <= bound {
			return strconv.Itoa(bound)
		}
	}
	return "inf"
}

//go:build unit

package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// shareMemoryStore keeps the cache-session history in memory.
type shareMemoryStore struct {
	ShareSchedulingStore
	mu       sync.Mutex
	sessions map[string][]byte
}

func (m *shareMemoryStore) SwapCacheSession(_ context.Context, groupID int64, fingerprint string, value []byte, _ time.Duration) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string][]byte{}
	}
	key := fmt.Sprintf("%d:%s", groupID, fingerprint)
	previous := m.sessions[key]
	m.sessions[key] = value
	return previous, nil
}

func TestShareAttributionSamplesWholeSessions(t *testing.T) {
	require.True(t, shareAttributionSampled("any", 1))
	sampled := 0
	for i := range 4000 {
		key := fmt.Sprintf("session-%d", i)
		first := shareAttributionSampled(key, 4)
		require.Equal(t, first, shareAttributionSampled(key, 4), "a session is either always or never sampled")
		if first {
			sampled++
		}
	}
	require.InDelta(t, 1000, sampled, 120)
}

func TestShareCacheClassification(t *testing.T) {
	svc := NewShareSchedulingService(nil, nil, nil)
	now := time.Now()
	base := shareCacheObservation{trace: shareTrace{groupID: 1, sessionKey: "s", platform: PlatformAnthropic},
		accountID: 1, model: "claude-sonnet-4-5", prompt: 20_000, at: now}
	same := &shareCachePlacement{accountID: 1, at: now.Add(-time.Minute), ttl: 5 * time.Minute, prompt: 19_000}
	moved := &shareCachePlacement{accountID: 2, at: now.Add(-time.Minute), ttl: 5 * time.Minute, prompt: 19_000}
	expired := &shareCachePlacement{accountID: 2, at: now.Add(-10 * time.Minute), ttl: 5 * time.Minute, prompt: 19_000}
	short := &shareCachePlacement{accountID: 1, at: now.Add(-time.Minute), ttl: 5 * time.Minute, prompt: 300}

	with := func(mutate func(*shareCacheObservation)) shareCacheObservation {
		obs := base
		mutate(&obs)
		return obs
	}
	cases := []struct {
		name     string
		obs      shareCacheObservation
		previous *shareCachePlacement
		want     string
	}{
		{"hit", with(func(o *shareCacheObservation) { o.cacheRead = 18_000 }), same, shareCacheHit},
		{"partial read is drift", with(func(o *shareCacheObservation) { o.cacheRead = 4_000 }), same, shareCacheMissPrefixDrift},
		{"first cacheable request", base, short, shareCacheMissNewSession},
		{"short prompt", with(func(o *shareCacheObservation) { o.prompt = 500 }), same, shareCacheBelowMin},
		{"sessionless", with(func(o *shareCacheObservation) { o.trace.sessionKey = "" }), nil, shareCacheMissSessionless},
		{"new session", base, nil, shareCacheMissNewSession},
		{"expired before switch", base, expired, shareCacheMissTTLExpired},
		{"drift", base, same, shareCacheMissPrefixDrift},
		{"spill", with(func(o *shareCacheObservation) { o.trace.reason = sharesched.ReasonSpillRebind }), moved, shareCacheMissSwitchSaturate},
		{"outage", with(func(o *shareCacheObservation) { o.trace.reason = sharesched.ReasonRebindUnavailable }), moved, shareCacheMissSwitchDown},
		{"policy", with(func(o *shareCacheObservation) { o.trace.reason = sharesched.ReasonReturnCold }), moved, shareCacheMissSwitchPolicy},
		{"failover", with(func(o *shareCacheObservation) { o.trace.switched = true }), moved, shareCacheMissSwitchFailover},
		{"original scheduler", base, moved, shareCacheMissSwitchOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, svc.classifyCache(tc.obs, tc.previous))
		})
	}
}

func TestShareCachePlacementRoundTrip(t *testing.T) {
	at := time.UnixMilli(1_790_000_000_123)
	p, ok := decodeShareCachePlacement(shareCachePlacement{accountID: 42, at: at, ttl: time.Hour, prompt: 9000}.encode())
	require.True(t, ok)
	require.Equal(t, int64(42), p.accountID)
	require.True(t, at.Equal(p.at))
	require.Equal(t, time.Hour, p.ttl)
	require.Equal(t, 9000, p.prompt)
	require.Equal(t, "750", shareTTFTBucket(600))
	require.Equal(t, "inf", shareTTFTBucket(45_000))
	_, ok = decodeShareCachePlacement(nil)
	require.False(t, ok)
}

func TestShareAttributionFollowsServedPlacements(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	env.share.store = &shareMemoryStore{}
	ttft := 420
	serve := func(requestID string, cacheRead int) *AccountSelectionResult {
		ctx := context.WithValue(cacheableContext(), ctxkey.RequestID, requestID)
		result := env.selectAccount(t, ctx, "attributed", nil)
		env.share.observeUsage(ctx, &UsageLog{AccountID: result.Account.ID, Model: "claude-sonnet-4-5",
			InputTokens: 12_000 - cacheRead, CacheReadTokens: cacheRead, FirstTokenMs: &ttft})
		return result
	}
	cacheCounter := func(field string) int64 {
		env.share.stats.mu.Lock()
		defer env.share.stats.mu.Unlock()
		var n int64
		for key, v := range env.share.stats.counters {
			if key.Kind == shareStatsKindCache && key.Field == field {
				n += v
			}
		}
		return n
	}

	serve("r1", 0)
	require.Eventually(t, func() bool { return cacheCounter(shareStatsClassPrefix+shareCacheMissNewSession) == 1 }, time.Second, 5*time.Millisecond)
	serve("r2", 11_000)
	require.Eventually(t, func() bool { return cacheCounter(shareStatsClassPrefix+shareCacheHit) == 1 }, time.Second, 5*time.Millisecond)
	serve("r3", 0)
	require.Eventually(t, func() bool { return cacheCounter(shareStatsClassPrefix+shareCacheMissPrefixDrift) == 1 }, time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return cacheCounter(shareStatsCacheReadField) == 11_000 }, time.Second, 5*time.Millisecond)
	require.Equal(t, int64(1), cacheCounter(shareStatsTTFTPrefix+string(sharesched.ReasonNewSession)+":500"))
	require.Equal(t, int64(2), cacheCounter(shareStatsTTFTPrefix+string(sharesched.ReasonAffinityHit)+":500"))

	// A usage record for another account than the traced one is ignored.
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "r4")
	result := env.selectAccount(t, ctx, "attributed", nil)
	env.share.observeUsage(ctx, &UsageLog{AccountID: result.Account.ID + 100, InputTokens: 12_000})
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int64(3), cacheCounter(shareStatsRequestsField))
}

func TestShareAttributionCoversOriginalGroups(t *testing.T) {
	env := newShareTestEnv(t, twoAccountPolicy(), Account{ID: 1}, Account{ID: 2})
	state := env.share.state.Load()
	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: false, CacheAttribution: true, AttributionSampleRate: 1}, groups: state.groups})
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "orig-1")
	result := env.selectAccount(t, ctx, "original-session", nil)
	env.share.observeUsage(ctx, &UsageLog{AccountID: result.Account.ID, Model: "claude-sonnet-4-5", InputTokens: 100})
	require.Equal(t, int64(1), env.counter(shareStatsClassPrefix+shareCacheBelowMin))
	require.Zero(t, env.counter(shareStatsReasonPrefix+string(sharesched.ReasonNewSession)), "no share decision was made")
}

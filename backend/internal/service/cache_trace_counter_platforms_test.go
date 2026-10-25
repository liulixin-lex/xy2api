//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cacheProviderCounterPolicy(t *testing.T, platforms []string) *CachePolicySnapshot {
	t.Helper()
	store := newPolicyTestStore()
	svc := NewCachePolicyService(store, policyTestConfig(), WithCacheNorthstar(), WithProviderCacheTrace())
	encoded, err := json.Marshal(platforms)
	require.NoError(t, err)
	raw := fmt.Sprintf(`{"expected_rev":0,"spec":{"enabled":true,"platforms":%s}}`, string(encoded))
	_, _, err = svc.Update(context.Background(), "observability", policyWrite(t, raw), nil, CachePolicyActor{})
	require.NoError(t, err)
	return svc.Snapshot()
}

func cacheProviderCounterSample() CacheTraceCounterSample {
	return CacheTraceCounterSample{
		PromptTokens: 2048, ReadTokens: 1024, PreviousPrompt: 2048,
		LossClass: "under_read", Transport: "http", ModelGen: "gpt56", ForkKind: "append",
		PCKClassIn: "none", PCKClassOut: "injected", SessOutRelation: "uuid_of_pck",
		RouteReason: "sticky_hit", RebindCause: "none", SessHdrIn: 1, SessHdrOut: 1, PCKOutIsUUID: true,
	}
}

func TestCacheProviderCountersDefaultMain16Parity(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC)
	p := cacheProviderCounterPolicy(t, []string{"openai"})
	c := NewCacheTraceCounters()
	c.started = now.Add(-2 * time.Hour)
	for i := 0; i < 24; i++ {
		at := now.Add(-time.Duration(i) * time.Minute)
		sample := cacheProviderCounterSample()
		if i%3 == 0 {
			sample.LossClass, sample.ReadTokens = "full", 2048
		}
		if i%5 == 0 {
			sample.LossClass = "n/a_usage"
		}
		c.RecordBusiness(at, 2048, int64(sample.ReadTokens), "relay.example", fmt.Sprintf("s-%d", i%2), "gpt-5.4", int64(i%3))
		c.RecordPlatformTrace(at, "openai", sample)
	}
	for _, window := range []string{"1h", "24h"} {
		for _, platform := range []string{"openai", "all"} {
			query := CachePanelQuery{Window: window, Platform: platform}
			require.Equal(t, c.main16CounterSnapshot(now, query, p), c.Snapshot(now, query, p), window+"/"+platform)
			off := *p
			off.trace = false
			require.Equal(t, c.main16CounterSnapshot(now, query, &off), c.Snapshot(now, query, &off))
		}
	}
	require.ElementsMatch(t, c.main16CounterPreviousHour(now.Add(time.Hour)), c.PreviousHour(now.Add(time.Hour)))
	require.Nil(t, c.providers[0])
	require.Nil(t, c.providers[1])
}

func TestCacheProviderCountersDoNotPolluteOpenAI(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC)
	p := cacheProviderCounterPolicy(t, []string{"openai", "anthropic", "gemini", "messages_bridge"})
	c := NewCacheTraceCounters()
	for i := 0; i < 20; i++ {
		at := now.Add(-time.Duration(30-i) * time.Second)
		c.RecordBusiness(at, 2048, 1024, "shared.example", "same-session", "same-model", 1)
		c.RecordTrace(at, cacheTraceData{
			LossClass: "under_read", PromptTokens: 2048, ReadTokens: 1024,
			PCKClassIn: "none", PCKClassOut: "none", RouteReason: "sticky_hit",
			Prefix: cachePrefixObservation{PreviousPrompt: 2048},
		})
	}
	query := CachePanelQuery{Window: "1h", Platform: "openai"}
	before := c.Snapshot(now, query, p)
	for _, platform := range []string{"anthropic", "gemini"} {
		for i := 0; i < 2200; i++ {
			at := now.Add(-time.Duration(2200-i) * time.Millisecond)
			sample := cacheProviderCounterSample()
			sample.LossClass = fmt.Sprintf("synthetic-capacity-%d", i%150)
			sample.ModelGen = fmt.Sprintf("synthetic-model-%d", i)
			c.RecordPlatformBusiness(at, platform, 2048, 2048, fmt.Sprintf("host-%d.example", i), fmt.Sprintf("session-%d", i), "same-model", 2)
			c.RecordPlatformTrace(at, platform, sample)
		}
		// The same raw session/model in another platform cannot become the
		// predecessor of the OpenAI request.
		c.RecordPlatformBusiness(now, platform, 2048, 2048, "shared.example", "same-session", "same-model", 2)
		partition := c.platformPartition(platform, false)
		require.NotNil(t, partition)
		require.LessOrEqual(t, len(partition.hosts), 1025, "existing host bound plus its other bucket")
		require.LessOrEqual(t, len(partition.sessions), 2048)
	}
	require.Equal(t, before, c.Snapshot(now, query, p))
	require.Len(t, c.hosts, 1)
	require.Len(t, c.sessions, 1)
	rows := c.PreviousHour(now.Add(time.Hour))
	platforms := map[string]bool{}
	for _, row := range rows {
		platforms[row.Platform] = true
		require.Equal(t, c.processID, row.ProcessID)
	}
	require.Equal(t, map[string]bool{"openai": true, "anthropic": true, "gemini": true}, platforms)
}

func TestCacheProviderCountersSumBeforeFoldingAndIsolateOpenAIMetrics(t *testing.T) {
	now := time.Now().UTC()
	p := cacheProviderCounterPolicy(t, []string{"openai", "anthropic", "gemini"})
	c := NewCacheTraceCounters()
	for _, platform := range cacheCounterPlatforms {
		for i := 0; i < 6; i++ {
			sample := cacheProviderCounterSample()
			sample.LossClass = "cold"
			c.RecordPlatformBusiness(now, platform, 2048, 1024, "", "", "", 1)
			c.RecordPlatformTrace(now, platform, sample)
		}
	}
	all := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "all"}, p)
	oa := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "openai"}, p)
	require.EqualValues(t, 18, all["coverage"].(map[string]any)["traced_requests"])
	loss := all["loss_classes"].([]any)
	require.Len(t, loss, 1)
	require.Equal(t, "cold", loss[0].(map[string]any)["class"], "6+6+6 is folded only after summation")
	require.EqualValues(t, 18, loss[0].(map[string]any)["requests"])
	for _, field := range []string{"pck_in", "pck_out", "pck_out_is_uuid_ratio", "session_headers", "sess_out_relation"} {
		require.Equal(t, oa[field], all[field], field)
	}
	require.Equal(t, oa["coverage"].(map[string]any)["category_known_ratio"], all["coverage"].(map[string]any)["category_known_ratio"])
	for _, platform := range []string{"anthropic", "gemini", "grok"} {
		view := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: platform}, p)
		require.Empty(t, view["pck_in"])
		require.Empty(t, view["session_headers"])
		require.Nil(t, view["pck_out_is_uuid_ratio"].(*float64))
		require.Nil(t, view["coverage"].(map[string]any)["category_known_ratio"].(*float64))
	}
}

func TestCacheProviderCountersHostEWMAAndHistoricalScope(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC)
	c := NewCacheTraceCounters()
	p := cacheProviderCounterPolicy(t, []string{"openai", "anthropic"})
	for i := 0; i < 10; i++ {
		c.RecordPlatformBusiness(now.Add(-30*time.Minute), "openai", 100, 0, "same.example", "", "", 1)
		c.RecordPlatformBusiness(now, "anthropic", 100, 100, "same.example", "", "", 2)
		c.RecordPlatformTrace(now, "openai", cacheProviderCounterSample())
		c.RecordPlatformTrace(now, "anthropic", cacheProviderCounterSample())
	}
	all := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "all"}, p)
	hosts := all["host_read_ewma"].([]any)
	require.Len(t, hosts, 1)
	host := hosts[0].(map[string]any)
	require.EqualValues(t, 20, host["samples"])
	require.InDelta(t, 2.0/3, *host["read_share"].(*float64), 1e-12)
	onlyProvider := cacheProviderCounterPolicy(t, []string{"anthropic"})
	old := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "openai"}, onlyProvider)
	require.Equal(t, []string{"platform_not_covered"}, old["scope_notes"])
	require.EqualValues(t, 10, old["coverage"].(map[string]any)["traced_requests"])
	selected := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "all"}, onlyProvider)
	require.Equal(t, []string{"anthropic"}, selected["platforms_covered"])
	require.EqualValues(t, 10, selected["coverage"].(map[string]any)["traced_requests"])
	require.Empty(t, selected["pck_out"])
	require.Nil(t, selected["pck_out_is_uuid_ratio"].(*float64))
	bridge := cacheProviderCounterPolicy(t, []string{"messages_bridge"})
	view := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "openai"}, bridge)
	require.Equal(t, []string{"openai"}, view["platforms_covered"])
	require.Equal(t, []string{"openai_native_excluded"}, view["scope_notes"])
}

func TestCacheProviderCountersConcurrentPartitions(t *testing.T) {
	now := time.Now().UTC()
	p := cacheProviderCounterPolicy(t, []string{"openai", "anthropic", "gemini"})
	c := NewCacheTraceCounters()
	var wg sync.WaitGroup
	for _, platform := range cacheCounterPlatforms {
		wg.Add(1)
		go func(platform string) {
			defer wg.Done()
			for i := 0; i < 80; i++ {
				c.RecordPlatformBusiness(now, platform, 2048, 1024, "relay.example", "session", "model", 1)
				c.RecordPlatformTrace(now, platform, cacheProviderCounterSample())
				_ = c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "all"}, p)
			}
		}(platform)
	}
	wg.Wait()
	all := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: "all"}, p)
	require.EqualValues(t, 240, all["coverage"].(map[string]any)["traced_requests"])
	require.False(t, math.IsNaN(*all["coverage"].(map[string]any)["coverage_ratio"].(*float64)))
	for _, platform := range cacheCounterPlatforms {
		part := c.Snapshot(now, CachePanelQuery{Window: "1h", Platform: platform}, p)
		require.EqualValues(t, 80, part["coverage"].(map[string]any)["traced_requests"])
	}
}

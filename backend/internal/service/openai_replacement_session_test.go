//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReplacementSessionStableLoadBand(t *testing.T) {
	svc := &OpenAIGatewayService{}
	group := int64(71)
	newer := time.Now()
	older := newer.Add(-time.Hour)
	a := &Account{ID: 1, Priority: 0, LastUsedAt: &older}
	b := &Account{ID: 2, Priority: 0, LastUsedAt: &newer}
	outside := &Account{ID: 3, Priority: 0}
	lower := &Account{ID: 4, Priority: 1}
	makeItems := func() []accountWithLoad {
		return []accountWithLoad{
			{account: a, loadInfo: &AccountLoadInfo{LoadRate: 10}},
			{account: b, loadInfo: &AccountLoadInfo{LoadRate: 30}},
			{account: outside, loadInfo: &AccountLoadInfo{LoadRate: 31}},
			{account: lower, loadInfo: &AccountLoadInfo{LoadRate: 0}},
		}
	}
	winners := map[int64]bool{}
	for i := 0; i < 40; i++ {
		session := fmt.Sprintf("session-%d", i)
		items := makeItems()
		svc.preferOpenAIReplacementSession(context.Background(), items, &group, session, "gpt-test", openAILegacyUpstreamRateOrder{}, false)
		winners[items[0].account.ID] = true
		want := []int64{items[0].account.ID, items[1].account.ID}
		a.LastUsedAt, b.LastUsedAt = b.LastUsedAt, a.LastUsedAt
		again := makeItems()
		again[0], again[1] = again[1], again[0]
		svc.preferOpenAIReplacementSession(context.Background(), again, &group, session, "gpt-test", openAILegacyUpstreamRateOrder{}, false)
		require.Equal(t, want, []int64{again[0].account.ID, again[1].account.ID})
		require.Equal(t, int64(3), items[2].account.ID)
		require.Equal(t, int64(4), items[3].account.ID)
	}
	require.Len(t, winners, 2, "independent sessions spread over both eligible candidates")
	require.NotEqual(t, openAIReplacementSessionHash(group, "session", 1), openAIReplacementSessionHash(group+1, "session", 1))
}

func TestOpenAIReplacementSessionHonorsCacheRateCompactAndHold(t *testing.T) {
	group := int64(72)
	cfg := &config.Config{}
	cfg.Gateway.OpenAICache.AwareRoutingEnabled = true
	cfg.Gateway.OpenAICache.RolloutPercent = 100
	svc := &OpenAIGatewayService{cfg: cfg}
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_compact_supported": true}}
	b := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_compact_supported": true}}
	var session string
	for i := 0; i < 100; i++ {
		candidate := fmt.Sprintf("cache-session-%d", i)
		if openAIReplacementSessionHash(group, candidate, a.ID) > openAIReplacementSessionHash(group, candidate, b.ID) {
			session = candidate
			break
		}
	}
	require.NotEmpty(t, session, "the cache-preferred account must lose the hash comparison")
	diagnostic := &OpenAICacheDiagnostic{Session: session, Prefix: "prefix", GroupID: group}
	ctx := context.WithValue(context.Background(), openAICacheDiagnosticKey{}, diagnostic)
	now := time.Now()
	for i, acc := range []*Account{a, b} {
		sample := openAICacheSample{count: openAICacheMinSamples, input: 100, read: float64(i) * 90, at: now}
		for j := range sample.recent {
			sample.recent[j] = now
		}
		svc.cacheTelemetry().scores[cacheSampleKey(diagnostic, acc, "gpt-test")] = sample
	}
	items := []accountWithLoad{{account: a, loadInfo: &AccountLoadInfo{}}, {account: b, loadInfo: &AccountLoadInfo{LoadRate: 20}}}
	svc.preferOpenAIReplacementSession(ctx, items, &group, session, "gpt-test", openAILegacyUpstreamRateOrder{}, true)
	require.Equal(t, b.ID, items[0].account.ID, "complete cache evidence outranks HRW")
	items = []accountWithLoad{{account: a, loadInfo: &AccountLoadInfo{}}, {account: b, loadInfo: &AccountLoadInfo{}}}
	order := openAILegacyUpstreamRateOrder{enabled: true, rates: map[int64]float64{1: 0.1, 2: 1}}
	svc.preferOpenAIReplacementSession(ctx, items, &group, session, "gpt-test", order, true)
	require.Equal(t, a.ID, items[0].account.ID, "different rates cannot exchange positions")
	b.Extra = nil
	svc.preferOpenAIReplacementSession(ctx, items, &group, session, "gpt-test", openAILegacyUpstreamRateOrder{}, true)
	require.Equal(t, a.ID, items[0].account.ID, "different compact tiers cannot exchange positions")
	held := map[int64]openAIAccountLatencyHoldSnapshot{a.ID: {Held: true}}
	items = partitionLatencyHeld(items, func(item accountWithLoad) *Account { return item.account }, held, false)
	require.Equal(t, b.ID, items[0].account.ID, "the final hold partition wins over affinity")
}

func TestOpenAILegacyReplacementSessionTriesNextSlot(t *testing.T) {
	group := int64(73)
	accounts := []Account{}
	for id := int64(1); id <= 3; id++ {
		accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group}})
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	session := "replacement"
	ranked := []accountWithLoad{{account: &accounts[1], loadInfo: &AccountLoadInfo{}}, {account: &accounts[2], loadInfo: &AccountLoadInfo{LoadRate: 20}}}
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
	svc.preferOpenAIReplacementSession(context.Background(), ranked, &group, session, "gpt-test", openAILegacyUpstreamRateOrder{}, false)
	first, second := ranked[0].account.ID, ranked[1].account.ID
	acquired := []int64{}
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		loadMap:        map[int64]*AccountLoadInfo{2: {LoadRate: 0}, 3: {LoadRate: 20}},
		acquireResults: map[int64]bool{first: false, second: true}, acquiredIDs: &acquired,
	})
	svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + session: 1}}
	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), &group, session, "gpt-test", map[int64]struct{}{1: {}})
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Equal(t, second, result.Account.ID)
	require.Equal(t, []int64{first, second}, acquired)
	result.ReleaseFunc()
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{2: false, 3: false},
	})
	result, err = svc.SelectAccountWithLoadAwareness(context.Background(), &group, session, "gpt-test", map[int64]struct{}{1: {}})
	require.NoError(t, err)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan, "all full candidates fall back to the existing queue")
	// Initial selection and requests without a session retain minimum-load order.
	for _, key := range []string{"", "new-session"} {
		acquired = nil
		svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{1: {LoadRate: 100}, 2: {LoadRate: 0}, 3: {LoadRate: 20}}, acquiredIDs: &acquired})
		result, err = svc.SelectAccountWithLoadAwareness(context.Background(), &group, key, "gpt-test", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), result.Account.ID)
		result.ReleaseFunc()
	}
}

func TestOpenAIShareCompactReplacementKeepsLowestLoad(t *testing.T) {
	env := newOpenAIShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShare), 1, 2, 3)
	env.svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true
	for _, id := range []int64{2, 3} {
		env.account(id).Extra = map[string]any{"openai_compact_supported": true}
	}
	env.svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{2: {LoadRate: 0}, 3: {LoadRate: 20}},
	})
	var session string
	for i := 0; i < 100; i++ {
		candidate := fmt.Sprintf("share-compact-%d", i)
		if openAIReplacementSessionHash(shareTestGroupID, candidate, 3) > openAIReplacementSessionHash(shareTestGroupID, candidate, 2) {
			session = candidate
			break
		}
	}
	require.NotEmpty(t, session, "use a session whose HRW winner has higher load")
	env.cache.bind("openai:"+session, 1, time.Hour)
	group := shareTestGroupID
	require.True(t, env.share.shareSchedulingActive(group))
	result, decision, err := env.svc.SelectAccountWithScheduler(context.Background(), &group, "", session, "gpt-5.1",
		map[int64]struct{}{1: {}}, OpenAIUpstreamTransportAny, true)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	defer result.ReleaseFunc()
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, int64(2), result.Account.ID, "share compact bypass keeps the original minimum-load order")
}

//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

type openAIShareTestEnv struct {
	svc   *OpenAIGatewayService
	share *ShareSchedulingService
	cache *shareTestCache
	conc  *shareTestConcurrency
	repo  schedulerTestOpenAIAccountRepo
}

func newOpenAIShareTestEnv(t *testing.T, policy sharesched.Policy, ids ...int64) *openAIShareTestEnv {
	t.Helper()
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	require.NoError(t, policy.Validate())
	accounts := make([]Account, 0, len(ids))
	for _, id := range ids {
		accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
			Schedulable: true, Concurrency: 5, AccountGroups: []AccountGroup{{GroupID: shareTestGroupID}}})
	}
	repo := schedulerTestOpenAIAccountRepo{accounts: accounts}
	cache := newShareTestCache()
	conc := newShareTestConcurrency()
	share := NewShareSchedulingService(nil, nil, nil)
	policy.Freeze() // as decodeShareGroupPolicy does
	share.state.Store(&shareSchedulingState{
		settings: ShareSchedulingSettings{Enabled: true, CacheAttribution: true, AttributionSampleRate: 1, DecisionSamples: true},
		groups:   map[int64]*shareGroupPolicy{shareTestGroupID: {groupID: shareTestGroupID, version: 1, policy: policy}},
	})
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, cfg: cfg,
		concurrencyService: NewConcurrencyService(conc), shareScheduling: share}
	return &openAIShareTestEnv{svc: svc, share: share, cache: cache, conc: conc, repo: repo}
}

func (e *openAIShareTestEnv) selectAccount(t *testing.T, ctx context.Context, previousResponseID, sessionHash string, excluded map[int64]struct{}) (*AccountSelectionResult, OpenAIAccountScheduleDecision) {
	t.Helper()
	gid := shareTestGroupID
	result, decision, err := e.svc.SelectAccountWithScheduler(ctx, &gid, previousResponseID, sessionHash, "gpt-5.1", excluded, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	return result, decision
}

func (e *openAIShareTestEnv) counter(field string) int64 {
	return (&shareTestEnv{share: e.share}).counter(field)
}

func (e *openAIShareTestEnv) account(id int64) *Account {
	for i := range e.repo.accounts {
		if e.repo.accounts[i].ID == id {
			return &e.repo.accounts[i]
		}
	}
	return nil
}

// openAIShareTarget places a fresh session in a separate environment and
// returns its HRW first choice.
func openAIShareTarget(t *testing.T, session string) (target, other int64) {
	t.Helper()
	probe := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	result, _ := probe.selectAccount(t, context.Background(), "", session, nil)
	return result.Account.ID, 3 - result.Account.ID
}

func TestOpenAIShareSchedulingPlacesAndRefreshesSessions(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)

	result, decision := env.selectAccount(t, context.Background(), "", "s1", nil)
	require.True(t, result.Acquired)
	require.Equal(t, openAIAccountScheduleLayerShare, decision.Layer)
	require.Equal(t, result.Account.ID, decision.SelectedAccountID)
	require.Equal(t, []string{"openai:s1"}, env.cache.created, "OpenAI bindings live under the openai: key with SET NX")
	require.Equal(t, time.Hour, env.cache.ttls["openai:s1"])
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonNewSession)))

	env.cache.remaining["openai:s1"] = time.Hour - 10*time.Second
	again, decision := env.selectAccount(t, context.Background(), "", "s1", nil)
	require.Equal(t, result.Account.ID, again.Account.ID)
	require.True(t, decision.StickySessionHit)
	require.Equal(t, []string{"openai:s1"}, env.cache.refreshed)
}

func TestOpenAIShareSchedulingNewSessionsFollowWeights(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	served := map[int64]int{}
	for i := range 1000 {
		result, _ := env.selectAccount(t, context.Background(), "", "w"+string(rune('a'+i%26))+time.Duration(i).String(), nil)
		served[result.Account.ID]++
	}
	require.InDelta(t, 0.75, float64(served[1])/1000, 0.05)
}

func TestOpenAIShareSchedulingSpillsAndRebinds(t *testing.T) {
	policy := twoAccountPolicy()
	policy.Affinity.Wait.MaxWaiting = 0
	env := newOpenAIShareTestEnv(t, policy, 1, 2)
	target, other := openAIShareTarget(t, "busy")
	env.cache.bind("openai:busy", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1

	result, _ := env.selectAccount(t, cacheableContext(), "", "busy", nil)
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, other, env.cache.sessionBindings["openai:busy"])
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSpillRebind)))
}

func TestOpenAIShareSchedulingSoftSpillKeepsBinding(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	target, other := openAIShareTarget(t, "outage")
	resetAt := time.Now().Add(time.Minute)
	env.account(target).RateLimitResetAt = &resetAt
	env.cache.bind("openai:outage", target, time.Hour-30*time.Second)

	result, _ := env.selectAccount(t, cacheableContext(), "", "outage", nil)
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, target, env.cache.sessionBindings["openai:outage"])
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSoftSpill)))
}

func TestOpenAIShareSchedulingAdmissionBindKeepsPlannedBinding(t *testing.T) {
	policy := twoAccountPolicy()
	policy.Affinity.Wait.MaxWaiting = 0
	policy.Affinity.OnSpill = sharesched.OnSpillKeep
	env := newOpenAIShareTestEnv(t, policy, 1, 2)
	target, other := openAIShareTarget(t, "keep")
	env.cache.bind("openai:keep", target, time.Hour-30*time.Second)
	env.conc.busy[target] = -1

	result, _ := env.selectAccount(t, cacheableContext(), "", "keep", nil)
	require.Equal(t, other, result.Account.ID)

	// The WebSocket ingress binds after every admission; a spill that keeps
	// the binding must survive it.
	gid := shareTestGroupID
	ctx := ContextWithSelectionProfitGate(context.Background(), result)
	require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ctx, &gid, "keep", other))
	require.Equal(t, target, env.cache.sessionBindings["openai:keep"])
	require.Empty(t, env.cache.replaced)
}

func TestOpenAIShareSchedulingFallbackWaitDefersBinding(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	env.conc.busy[1], env.conc.busy[2] = -1, -1

	result, _ := env.selectAccount(t, context.Background(), "", "queued", nil)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)
	require.NotContains(t, env.cache.sessionBindings, "openai:queued")

	gid := shareTestGroupID
	ctx := ContextWithSelectionProfitGate(context.Background(), result)
	require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ctx, &gid, "queued", result.Account.ID))
	require.Equal(t, []string{"openai:queued"}, env.cache.created)
}

func TestOpenAIShareSchedulingFailoverAndExhaustion(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	target, other := openAIShareTarget(t, "fo")
	env.cache.bind("openai:fo", target, time.Hour-30*time.Second)

	result, _ := env.selectAccount(t, cacheableContext(), "", "fo", map[int64]struct{}{target: {}})
	require.Equal(t, other, result.Account.ID)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonFailoverNext)))

	gid := shareTestGroupID
	_, _, err := env.svc.SelectAccountWithScheduler(context.Background(), &gid, "", "fo", "gpt-5.1",
		map[int64]struct{}{1: {}, 2: {}}, OpenAIUpstreamTransportAny, false)
	require.True(t, errors.Is(err, ErrNoAvailableAccounts))
	require.Contains(t, err.Error(), "excluded=2")
}

func TestOpenAIShareSchedulingHonoursResponseOwner(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	target, other := openAIShareTarget(t, "chain")
	require.NoError(t, env.svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), shareTestGroupID, "resp_owner", other, time.Hour))

	result, decision := env.selectAccount(t, context.Background(), "resp_owner", "chain", nil)
	require.Equal(t, other, result.Account.ID, "the response owner wins over the HRW target %d", target)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+string(sharesched.ReasonOwnerContinuation)))
}

func TestOpenAIShareSchedulingReadsLegacySessionKey(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	env.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
	_, other := openAIShareTarget(t, "migrate")
	env.cache.bind("openai:legacy-hash", other, 0)
	ctx := withOpenAILegacySessionHash(cacheableContext(), "legacy-hash")

	result, _ := env.selectAccount(t, ctx, "", "migrate", nil)
	require.Equal(t, other, result.Account.ID, "an active legacy binding is honoured")
	require.Equal(t, other, env.cache.sessionBindings["openai:migrate"])
	require.Equal(t, []string{"openai:migrate"}, env.cache.created, "the hit creates the primary binding")
}

func TestOpenAIShareSchedulingShadowAndBypasses(t *testing.T) {
	env := newOpenAIShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShadow,
		sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 3},
		sharesched.AccountRule{AccountID: 2, Tier: 1, Weight: 1}), 1, 2)
	_, decision := env.selectAccount(t, context.Background(), "", "shadow", nil)
	require.NotEqual(t, openAIAccountScheduleLayerShare, decision.Layer)
	require.Empty(t, env.cache.created)
	require.Eventually(t, func() bool { return env.counter(shareStatsShadowField) == 1 }, 2*time.Second, 10*time.Millisecond)

	share := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	gid := shareTestGroupID
	_, decision, _ = share.svc.SelectAccountWithScheduler(context.Background(), &gid, "", "compact", "gpt-5.1", nil, OpenAIUpstreamTransportAny, true)
	require.NotEqual(t, openAIAccountScheduleLayerShare, decision.Layer, "compact keeps its tiered original selection")
	_, _, _ = share.svc.SelectAccountWithSchedulerForImages(context.Background(), &gid, "image", "gpt-image-1", nil, OpenAIImagesCapabilityBasic)
	require.Zero(t, share.counter(shareStatsRequestsField))
}

func TestOpenAIShareSchedulingRecordsTraceAndTTL(t *testing.T) {
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-openai-1")
	result, _ := env.selectAccount(t, ctx, "", "traced", nil)
	trace, ok := env.share.traces.take("req-openai-1")
	require.True(t, ok)
	require.Equal(t, result.Account.ID, trace.accountID)
	require.Equal(t, sharesched.ReasonNewSession, trace.reason)

	gid := shareTestGroupID
	require.NoError(t, env.svc.BindStickySession(context.Background(), &gid, "ttl", 1))
	require.Equal(t, time.Hour, env.cache.ttls["openai:ttl"])
}

func TestShareExecutorNamesCodexPressure(t *testing.T) {
	e := &shareExecutor{}
	require.Equal(t, sharesched.ReasonCodexPressure, e.busyReason(&Account{Concurrency: 5}, 2))
	require.Equal(t, sharesched.ReasonSaturated, e.busyReason(&Account{Concurrency: 5}, 5))
}

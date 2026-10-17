//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// A no-account result answers other requests only when it holds for the
// whole group: no account that could serve the model on the request's
// platform is free of penalties. Sticky-only accounts past their RPM or
// window-cost threshold, session limits and forced platforms make the
// result depend on the request (review F2).

func withNegativeScopeRuntime(t *testing.T, byGroup map[int64][]Account) {
	t.Helper()
	withResilienceSettings(t, nil)
	r := NewResilienceService(nil, newMemResilienceStore())
	r.ConfigureFallbackGroups(&groupAccountsRepo{byGroup: byGroup}, &fallbackGroupRepo{groups: map[int64]*Group{}})
	prev := Resilience()
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(prev) })
}

func TestNegativeCacheOnlyForGroupLevelNoAccount(t *testing.T) {
	until := time.Now().Add(time.Hour)
	hard := Account{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x"}
	stickyOnly := Account{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}
	withNegativeScopeRuntime(t, map[int64][]Account{31: {stickyOnly, hard}, 32: {hard}})
	noAccount := fmt.Errorf("%w supporting model: m (total=2 eligible=0)", ErrNoAvailableAccounts)
	ctx := context.Background()

	// An unpenalised account exists: a new session got no account, a bound
	// session of the same group may still be served.
	g31 := int64(31)
	newSession := resilience.WithRequestState(ctx, resilience.NewRequestState())
	resilienceNegativeCachePut(newSession, &g31, "m", "", nil, noAccount)
	require.NoError(t, resilienceNegativeCacheHit(ctx, &g31, "m", "", nil))
	require.False(t, resilience.StateFrom(newSession).GroupNoAccount(), "the per-key fast fail must not count it")

	// Every account penalised: the result holds for the group, and the
	// requests answered from the cache count for the fast fail too.
	g32 := int64(32)
	first := resilience.WithRequestState(ctx, resilience.NewRequestState())
	resilienceNegativeCachePut(first, &g32, "m", "", nil, noAccount)
	require.True(t, resilience.StateFrom(first).GroupNoAccount())
	next := resilience.WithRequestState(ctx, resilience.NewRequestState())
	require.ErrorIs(t, resilienceNegativeCacheHit(next, &g32, "m", "", nil), errNegativeCached)
	require.True(t, resilience.StateFrom(next).GroupNoAccount())

	// A model no account of the group supports is group level as well.
	other := Account{ID: 4, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"claude-x": "claude-x"}}}
	r := Resilience()
	r.lastResortGroups.drop(34)
	r.fallbackAccounts.(*groupAccountsRepo).byGroup[34] = []Account{other}
	g34 := int64(34)
	resilienceNegativeCachePut(ctx, &g34, "m", "", nil, noAccount)
	require.ErrorIs(t, resilienceNegativeCacheHit(ctx, &g34, "m", "", nil), errNegativeCached)
}

func TestNegativeCacheKeepsAForcedPlatformApart(t *testing.T) {
	until := time.Now().Add(time.Hour)
	withNegativeScopeRuntime(t, map[int64][]Account{33: {{ID: 3, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x"}}})
	noAccount := fmt.Errorf("%w supporting model: m (total=1 eligible=0)", ErrNoAvailableAccounts)
	forced := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformAntigravity)
	g := int64(33)

	// /antigravity found no account; the group's normal routes are not
	// answered by that result.
	resilienceNegativeCachePut(forced, &g, "m", "", nil, noAccount)
	require.ErrorIs(t, resilienceNegativeCacheHit(forced, &g, "m", "", nil), errNegativeCached)
	require.NoError(t, resilienceNegativeCacheHit(context.Background(), &g, "m", "", nil))
}

type fixedRPMCache struct{ rpm int }

func (c fixedRPMCache) IncrementRPM(context.Context, int64) (int, error) { return c.rpm, nil }
func (c fixedRPMCache) GetRPM(context.Context, int64) (int, error)       { return c.rpm, nil }
func (c fixedRPMCache) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = c.rpm
	}
	return out, nil
}

func TestStickyOnlyAccountKeepsServingBoundSessions(t *testing.T) {
	// The review's F2 scenario end to end: an Anthropic OAuth account past
	// its RPM threshold serves only its bound sessions. A new session gets
	// no account; a bound session right after must still be served.
	acc := Account{ID: 91, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2,
		Extra: map[string]any{"base_rpm": 10, "rpm_strategy": "sticky_exempt"}, AccountGroups: []AccountGroup{{GroupID: 7}}}
	svc, cache := newLastResortGatewayFixture(t, []Account{acc})
	withNegativeScopeRuntime(t, map[int64][]Account{7: {acc}})
	svc.rpmCache = fixedRPMCache{rpm: 15}
	cache.sessionBindings = map[string]int64{"bound": 91}
	groupID := int64(7)
	ctx := context.Background()

	_, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "new", "claude-sonnet-4", nil, "", 0)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	res, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "bound", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err, "the bound session is not answered by the new session's result")
	require.Equal(t, int64(91), res.Account.ID)
}

func newNegativeScopeOpenAIService(accounts []Account) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	return &OpenAIGatewayService{
		accountRepo:        openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
}

func TestUncachedOpenAISelectionStillMarksGroupNoAccount(t *testing.T) {
	// Continuation, image and compact selections are never cached, but
	// their group-level no-account results count for the fast fail.
	until := time.Now().Add(time.Hour)
	down := Account{ID: 72, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x"}
	withNegativeScopeRuntime(t, map[int64][]Account{9: {down}})
	svc := newNegativeScopeOpenAIService([]Account{down})
	groupID := int64(9)
	st := resilience.NewRequestState()
	_, _, err := svc.SelectAccountWithScheduler(resilience.WithRequestState(context.Background(), st), &groupID, "resp_prev", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, st.GroupNoAccount())
}

func TestUncachedOpenAISelectionIgnoresTheCacheEntirely(t *testing.T) {
	// A cached entry answers only selections that may be cached: a
	// continuation that found its account is not marked by it.
	healthy := Account{ID: 73, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	withNegativeScopeRuntime(t, map[int64][]Account{10: {}})
	svc := newNegativeScopeOpenAIService([]Account{healthy})
	groupID := int64(10)
	variant := "openai:" + string(OpenAIUpstreamTransportAny) + "::" + NormalizeOpenAICompatiblePlatform(PlatformOpenAI)
	resilienceNegativeCachePut(context.Background(), &groupID, "gpt-5.5", variant, nil,
		fmt.Errorf("%w supporting model: gpt-5.5 (total=0)", ErrNoAvailableAccounts))
	require.ErrorIs(t, resilienceNegativeCacheHit(context.Background(), &groupID, "gpt-5.5", variant, nil), errNegativeCached)

	st := resilience.NewRequestState()
	sel, _, err := svc.SelectAccountWithScheduler(resilience.WithRequestState(context.Background(), st), &groupID, "resp_prev", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(73), sel.Account.ID)
	require.False(t, st.GroupNoAccount())
}

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountLatencyHoldDecayAndThreshold(t *testing.T) {
	state := newOpenAIAccountLatencyHoldState(16)
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		_, started, _, _ := state.record(1, "gpt-test", time.Second, now)
		require.False(t, started)
	}
	entry, started, _, _ := state.record(1, "gpt-test", 11*time.Second, now.Add(time.Second))
	require.False(t, started)
	require.Zero(t, entry.holdUntil)

	state = newOpenAIAccountLatencyHoldState(16)
	for i := 0; i < 5; i++ {
		state.record(1, "gpt-test", 11*time.Second, now)
	}
	state.record(1, "gpt-test", time.Second, now.Add(openAIAccountLatencyHoldDecayTau))
	snap := state.snapshot(1, "gpt-test", now.Add(openAIAccountLatencyHoldDecayTau))
	require.False(t, snap.Held)
}

func TestOpenAIAccountLatencyHoldStartClearsSamples(t *testing.T) {
	state := newOpenAIAccountLatencyHoldState(16)
	now := time.Unix(2000, 0)
	for i := 0; i < 5; i++ {
		state.record(2, "gpt-test", 11*time.Second, now)
	}
	state.mu.Lock()
	before := state.entries[openAIAccountModelKey{AccountID: 2, Model: "gpt-test"}]
	state.mu.Unlock()
	require.GreaterOrEqual(t, before.weightedSamples, float64(5))
	entry, started, _, _ := state.record(2, "gpt-test", 11*time.Second, now)
	require.True(t, started)
	require.Zero(t, entry.weightedSamples)
	require.Zero(t, entry.weightedSlow)
	state.mu.Lock()
	entry = state.entries[openAIAccountModelKey{AccountID: 2, Model: "gpt-test"}]
	state.mu.Unlock()
	require.Zero(t, entry.weightedSamples)
}

func TestOpenAIAccountLatencyHoldExpiresAndRequiresNewEvidence(t *testing.T) {
	state := newOpenAIAccountLatencyHoldState(16)
	now := time.Unix(3000, 0)
	for i := 0; i < 6; i++ {
		state.record(3, "gpt-test", 11*time.Second, now)
	}
	require.True(t, state.snapshot(3, "gpt-test", now).Held)
	expired := now.Add(openAIAccountLatencyHoldDuration)
	require.False(t, state.snapshot(3, "gpt-test", expired).Held)
	for i := 0; i < 5; i++ {
		state.record(3, "gpt-test", 11*time.Second, expired)
	}
	require.False(t, state.snapshot(3, "gpt-test", expired).Held)
	state.record(3, "gpt-test", 11*time.Second, expired)
	require.True(t, state.snapshot(3, "gpt-test", expired).Held)
}

func TestPartitionLatencyHeldCompactDoesNotCrossTier(t *testing.T) {
	tierTwoHeld := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_compact_supported": true}}
	tierOneHealthy := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{}}
	holds := map[int64]openAIAccountLatencyHoldSnapshot{1: {Held: true}}
	items := []*Account{tierTwoHeld, tierOneHealthy}
	got := partitionLatencyHeld(items, func(account *Account) *Account { return account }, holds, true)
	require.Equal(t, []int64{1, 2}, []int64{got[0].ID, got[1].ID})
}

func TestMigrateOpenAILatencyHeldOwnerKeepsSingleOrAllHeldOwner(t *testing.T) {
	group := int64(77)
	owner := Account{ID: 101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{group}}
	cache := &atomicStickyTestCache{bindings: map[string]int64{"openai:v2:migrate": owner.ID}}
	svc := &OpenAIGatewayService{
		cache:       cache,
		cfg:         &config.Config{RunMode: config.RunModeStandard},
		accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{owner}}},
	}
	now := time.Now()
	for i := 0; i < openAIAccountLatencyMinSamples; i++ {
		svc.recordOpenAIAccountLatencySample(&owner, "gpt-test", 11*time.Second, now)
	}
	require.True(t, svc.openAIAccountLatencyHoldSnapshot(&owner, "gpt-test", time.Now()).Held)
	requestCtx := resilience.WithRequestState(context.Background(), &resilience.RequestState{})
	ctx := resilience.StateFrom(requestCtx)
	ctx.KeepSticky(owner.ID)
	got := svc.migrateOpenAILatencyHeldOwner(requestCtx, &group, PlatformOpenAI, "v2:migrate", "gpt-test", owner.ID, nil, false, "")
	require.Equal(t, owner.ID, got)
	require.Equal(t, owner.ID, ctx.KeptSticky())
	bound, err := cache.GetSessionAccountID(context.Background(), group, "openai:v2:migrate")
	require.NoError(t, err)
	require.Equal(t, owner.ID, bound)
}

func TestMigrateOpenAILatencyHeldOwnerUsesHealthyAlternativeAndProfitGate(t *testing.T) {
	group := int64(78)
	owner := Account{ID: 201, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{group}}
	healthy := Account{ID: 202, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{group}}
	cache := &atomicStickyTestCache{bindings: map[string]int64{"openai:v2:migrate": owner.ID}}
	svc := &OpenAIGatewayService{
		cache:       cache,
		cfg:         &config.Config{RunMode: config.RunModeStandard},
		accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{owner, healthy}}},
	}
	now := time.Now()
	for i := 0; i < openAIAccountLatencyMinSamples; i++ {
		svc.recordOpenAIAccountLatencySample(&owner, "gpt-test", 11*time.Second, now)
	}
	require.True(t, svc.openAIAccountLatencyHoldSnapshot(&owner, "gpt-test", time.Now()).Held)
	requestCtx := resilience.WithRequestState(context.Background(), &resilience.RequestState{})
	state := resilience.StateFrom(requestCtx)
	state.KeepSticky(owner.ID)
	got := svc.migrateOpenAILatencyHeldOwner(requestCtx, &group, PlatformOpenAI, "v2:migrate", "gpt-test", owner.ID, nil, false, "")
	require.Zero(t, got)
	require.Zero(t, state.KeptSticky())
	_, err := cache.GetSessionAccountID(context.Background(), group, "openai:v2:migrate")
	require.ErrorIs(t, err, ErrStickySessionNotFound)

	cache.bindings["openai:v2:migrate"] = owner.ID
	state.KeepSticky(owner.ID)
	profitCtx := context.WithValue(resilience.WithRequestState(context.Background(), &resilience.RequestState{}), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{threshold: 0, pricingAt: time.Now()})
	state = resilience.StateFrom(profitCtx)
	state.KeepSticky(owner.ID)
	got = svc.migrateOpenAILatencyHeldOwner(profitCtx, &group, PlatformOpenAI, "v2:migrate", "gpt-test", owner.ID, nil, false, "")
	require.Equal(t, owner.ID, got)
	require.Equal(t, owner.ID, state.KeptSticky())
}

func TestMigrateOpenAILatencyHeldOwnerRequiresFreshCapacity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		concurrency int
		load        *AccountLoadInfo
		loadErr     error
		warm        bool
		migrate     bool
	}{
		{name: "full despite cached free slot", concurrency: 1, load: &AccountLoadInfo{CurrentConcurrency: 1}, warm: true},
		{name: "query failed", concurrency: 1, loadErr: errors.New("load unavailable")},
		{name: "missing load", concurrency: 1},
		{name: "free slot despite load percentage", concurrency: 2, load: &AccountLoadInfo{CurrentConcurrency: 1, LoadRate: 100}, migrate: true},
		{name: "unlimited", load: &AccountLoadInfo{CurrentConcurrency: 100}, migrate: true},
		{name: "unlimited missing load"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := int64(79)
			owner := Account{ID: 301, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{group}}
			alternative := owner
			alternative.ID, alternative.Concurrency = 302, tc.concurrency
			sticky := &atomicStickyTestCache{bindings: map[string]int64{"openai:v2:migrate": owner.ID}}
			loads := &schedulerTestConcurrencyCache{skipDefaultLoad: true, loadMap: map[int64]*AccountLoadInfo{}}
			concurrency := NewConcurrencyService(loads)
			if tc.warm {
				loads.loadMap[alternative.ID] = &AccountLoadInfo{}
				concurrency.SetAccountLoadBatchCacheTTL(time.Hour)
				_, err := concurrency.GetAccountsLoadBatch(context.Background(), []AccountWithConcurrency{{ID: alternative.ID, MaxConcurrency: alternative.Concurrency}})
				require.NoError(t, err)
			}
			loads.loadMap[alternative.ID], loads.loadBatchErr = tc.load, tc.loadErr
			svc := &OpenAIGatewayService{
				cache: sticky, cfg: &config.Config{RunMode: config.RunModeStandard}, concurrencyService: concurrency,
				accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{owner, alternative}}},
			}
			now := time.Now()
			for i := 0; i < openAIAccountLatencyMinSamples; i++ {
				svc.recordOpenAIAccountLatencySample(&owner, "gpt-test", 11*time.Second, now)
			}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			resilience.StateFrom(ctx).KeepSticky(owner.ID)
			got := svc.migrateOpenAILatencyHeldOwner(ctx, &group, PlatformOpenAI, "v2:migrate", "gpt-test", owner.ID, nil, false, "")
			bound, err := sticky.GetSessionAccountID(ctx, group, "openai:v2:migrate")
			if tc.migrate {
				require.Zero(t, got)
				require.Zero(t, resilience.StateFrom(ctx).KeptSticky())
				require.ErrorIs(t, err, ErrStickySessionNotFound)
			} else {
				require.Equal(t, owner.ID, got)
				require.Equal(t, owner.ID, resilience.StateFrom(ctx).KeptSticky())
				require.NoError(t, err)
				require.Equal(t, owner.ID, bound)
			}
		})
	}
}

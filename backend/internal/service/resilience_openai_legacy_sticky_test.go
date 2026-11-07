//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// With the OpenAI advanced scheduler off (the default) the original
// scheduler takes its legacy path. A soft-penalised owner keeps its binding
// there too while the request is served elsewhere (design §4.2), as under
// the advanced scheduler and the Gateway.

func TestStickyBypassReasonKeepsFirstReason(t *testing.T) {
	state := resilience.NewRequestState()
	state.NoteStickyBypassReason("owner_excluded")
	state.NoteStickyBypassReason("capacity")
	require.Equal(t, "owner_excluded", state.StickyBypassReason())
	state.ResetStickyBypassReason()
	state.NoteStickyBypassReason("capacity")
	require.Equal(t, "capacity", state.StickyBypassReason())
}

func TestStickyBypassFirstOutputTimeoutReselectionRecordsRequestFailure(t *testing.T) {
	for _, binding := range []string{"retained", "cleared"} {
		t.Run(binding, func(t *testing.T) {
			withResilienceSettings(t, nil)
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			groupID := int64(10119)
			accounts := []Account{
				{ID: 36061, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
				{ID: 36062, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
			}
			const session = "timeout-reselect"
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + session: accounts[0].ID}}
			cfg := &config.Config{}
			cfg.Gateway.Scheduling.LoadBatchEnabled = true
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
				cache:              cache,
				cfg:                cfg,
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
				resilience:         NewResilienceService(nil, newMemResilienceStore()),
			}
			req := newWebSearchReplayRequest(t)
			ctx := req.ctx
			state := resilience.StateFrom(ctx)
			first, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", session, "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, accounts[0].ID, first.Account.ID)
			if first.ReleaseFunc != nil {
				first.ReleaseFunc()
			}
			logs := captureOpenAIObservationLogs(t)
			state.SetAttemptProfile(resilience.AttemptProfile{AccountID: accounts[0].ID, Model: "gpt-test", AttemptStart: time.Now().Add(-time.Second), Stream: true})
			timeout := svc.newOpenAIFirstOutputTimeoutError(ctx, req.c, &accounts[0], nil, "", time.Now().Add(-time.Second), "gpt-test", "", time.Second, "headers", http.Header{})
			require.Equal(t, http.StatusGatewayTimeout, timeout.StatusCode)
			require.Equal(t, PlannedFirstOutputTimeoutReason, timeout.Reason)
			require.True(t, state.AccountFailed(accounts[0].ID), "the actual timeout reporter marks the failed attempt")
			holdAt := time.Now()
			for i := 0; i < openAIAccountLatencyMinSamples; i++ {
				svc.recordOpenAIAccountLatencySample(&accounts[0], "gpt-test", 11*time.Second, holdAt)
			}
			require.True(t, svc.openAIAccountLatencyHoldSnapshot(&accounts[0], "gpt-test", time.Now()).Held)
			// An earlier migration/cleanup can remove the cache entry while RequestState
			// still retains the original owner for the request's next selection.
			if binding == "cleared" {
				require.NoError(t, svc.deleteStickySessionAccountIDIfOwner(ctx, &groupID, session, accounts[0].ID))
			}
			retry, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", session, "gpt-test", map[int64]struct{}{accounts[0].ID: {}}, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, accounts[1].ID, retry.Account.ID)
			if retry.ReleaseFunc != nil {
				retry.ReleaseFunc()
			}
			if binding == "retained" {
				require.Equal(t, "latency_migration", state.StickyBypassReason(), "the failed-owner log reason takes precedence over the observed migration")
			}
			require.Contains(t, logs.String(), `"reason":"owner_failed_in_request"`)
			require.NotContains(t, logs.String(), `"reason":"unrecorded"`)
			require.Equal(t, 1, strings.Count(logs.String(), `"msg":"openai.sticky_bypass"`))
		})
	}
}

func TestStickyBypassStateRepairAvoidanceIsRequestScoped(t *testing.T) {
	for _, currentStateRepair := range []bool{true, false} {
		name := "historical avoidance and nonfailure exclusion"
		if currentStateRepair {
			name = "M3 state switch"
		}
		t.Run(name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			groupID := int64(10129)
			accounts := []Account{
				{ID: 36071, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
				{ID: 36072, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
			}
			const session = "state-reselect"
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + session: accounts[0].ID}}
			store := newMemResilienceStore()
			cfg := &config.Config{}
			cfg.Gateway.Scheduling.LoadBatchEnabled = true
			svc := &OpenAIGatewayService{
				accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
				cache:              cache,
				cfg:                cfg,
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
				resilience:         NewResilienceService(nil, store),
			}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			state.NoteStickyOwner(accounts[0].ID)
			if currentStateRepair {
				// Mirror the M3 handler's single avoidance/diagnostic write point.
				svc.AvoidOpenAIStateAccount(ctx, groupID, session, accounts[0].ID)
				state.NoteStateRepairAvoidance(accounts[0].ID)
				require.NotContains(t, cache.sessionBindings, "openai:"+session)
			} else {
				// Historical avoidance may coexist with a retained cache owner.
				require.NoError(t, store.AddSessionAvoid(ctx, groupID, session, accounts[0].ID, time.Minute))
				svc.resilience.avoidingSessions.Store(sessionMarkKey(groupID, session), time.Now().Add(time.Minute))
				require.Contains(t, svc.resilience.sessionAvoid(ctx, &groupID, session), accounts[0].ID)
			}
			require.True(t, state.Avoided(accounts[0].ID))
			require.False(t, state.AccountFailed(accounts[0].ID))
			logs := captureOpenAIObservationLogs(t)
			// The caller can exclude an owner for a nonfailure gate such as profit.
			excluded := map[int64]struct{}{accounts[0].ID: {}}
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", session, "gpt-test", excluded, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, accounts[1].ID, selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			require.False(t, state.AccountFailed(accounts[0].ID), "diagnostics must not mark account failure")
			failures, err := store.SessionFailures(ctx, groupID, session)
			require.NoError(t, err)
			require.Empty(t, failures)
			if currentStateRepair {
				require.Contains(t, logs.String(), `"reason":"owner_failed_in_request"`)
				require.NotContains(t, logs.String(), `"reason":"unrecorded"`)
			} else {
				require.NotContains(t, logs.String(), `"reason":"owner_failed_in_request"`)
				require.Contains(t, logs.String(), `"reason":"owner_excluded"`)
			}
			require.Equal(t, 1, strings.Count(logs.String(), `"msg":"openai.sticky_bypass"`))
		})
	}
}

func TestResilienceOpenAILoadBatchStickyBypassRecordsReason(t *testing.T) {
	withResilienceSettings(t, nil)
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	resetAt := time.Now().Add(time.Minute)
	groupID := int64(10109)
	accounts := []Account{
		{ID: 36021, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, RateLimitResetAt: &resetAt},
		{ID: 36022, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:load-batch-session": accounts[0].ID}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	logs := captureOpenAIObservationLogs(t)
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "load-batch-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, accounts[1].ID, selection.Account.ID)
	require.NotEqual(t, "unrecorded", state.StickyBypassReason())
	require.Equal(t, "soft_owner_preserved", state.StickyBypassReason())
	require.Contains(t, logs.String(), `"msg":"openai.sticky_bypass"`)
	require.Contains(t, logs.String(), `"reason":"soft_owner_preserved"`)
	require.Contains(t, logs.String(), `"level":"INFO"`)
	require.Contains(t, logs.String(), `"group_id":10109`)
	require.Contains(t, logs.String(), `"session":"load-bat"`)
	require.Contains(t, logs.String(), `"request_id":`)
	retry, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "load-batch-session", "gpt-5.1", map[int64]struct{}{accounts[0].ID: {}}, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, "owner_excluded", state.StickyBypassReason())
	require.Equal(t, 1, strings.Count(logs.String(), `"msg":"openai.sticky_bypass"`))
	if retry.ReleaseFunc != nil {
		retry.ReleaseFunc()
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestResilienceOpenAILoadBatchStickyDeleteWarns(t *testing.T) {
	withResilienceSettings(t, nil)
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	groupID := int64(10110)
	accounts := []Account{
		{ID: 36031, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusError, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
		{ID: 36032, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:delete-session": accounts[0].ID}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	logs := captureOpenAIObservationLogs(t)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "delete-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, accounts[1].ID, selection.Account.ID)
	require.Equal(t, 1, cache.deletedSessions["openai:delete-session"])
	require.Contains(t, logs.String(), `"level":"WARN"`)
	require.Contains(t, logs.String(), `"reason":"owner_should_clear"`)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

type stickyDeleteFailureCache struct {
	*atomicStickyTestCache
	deleteAttempts int
}

func (c *stickyDeleteFailureCache) DeleteSessionAccountIDIfOwner(context.Context, int64, string, int64) error {
	c.deleteAttempts++
	return errors.New("cache deletion failed")
}

func (c *stickyDeleteFailureCache) DeleteSessionAccountIDIfOwnerAndReport(ctx context.Context, groupID int64, session string, owner int64) (bool, error) {
	return false, c.DeleteSessionAccountIDIfOwner(ctx, groupID, session, owner)
}

func TestResilienceOpenAILoadBatchStickyDeleteFailureIsInfo(t *testing.T) {
	withResilienceSettings(t, nil)
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	groupID := int64(10111)
	accounts := []Account{
		{ID: 36041, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusError, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
		{ID: 36042, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	const session = "v2:failed-delete-session"
	cache := &stickyDeleteFailureCache{atomicStickyTestCache: &atomicStickyTestCache{
		bindings: map[string]int64{"openai:" + session: accounts[0].ID},
	}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	logs := captureOpenAIObservationLogs(t)
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", session, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, accounts[1].ID, selection.Account.ID, "deletion errors must not change fallback selection")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	require.Equal(t, 1, cache.deleteAttempts)
	require.Equal(t, accounts[0].ID, cache.bindings["openai:"+session], "failed deletion keeps the original owner")
	require.False(t, state.StickyBypassChangesBinding())
	require.Contains(t, logs.String(), `"reason":"owner_should_clear"`)
	require.Contains(t, logs.String(), `"level":"INFO"`)
	require.Equal(t, 1, strings.Count(logs.String(), `"msg":"openai.sticky_bypass"`))
}

func TestStickyBypassDeleteNoopDoesNotMarkBindingChange(t *testing.T) {
	const session = "v2:delete-noop"
	cache := &atomicStickyTestCache{bindings: map[string]int64{"openai:" + session: 2}}
	svc := &OpenAIGatewayService{cache: cache}
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	require.NoError(t, svc.deleteStickySessionAccountIDIfOwner(ctx, nil, session, 1))
	require.False(t, state.StickyBypassChangesBinding())
	require.EqualValues(t, 2, cache.bindings["openai:"+session], "an owner mismatch must remain a no-op")
	require.NoError(t, svc.deleteStickySessionAccountIDIfOwner(ctx, nil, session, 2))
	require.True(t, state.StickyBypassChangesBinding(), "a confirmed owner deletion must still be reported")
}

type stickyBypassRetryScheduler struct {
	OpenAIAccountScheduler
	calls    int
	selected *Account
}

func (s *stickyBypassRetryScheduler) Select(ctx context.Context, _ OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	s.calls++
	state := resilience.StateFrom(ctx)
	if !openAIProxyStreamQuarantineBypassed(ctx) {
		state.NoteStickyBypassReason("owner_unavailable")
		state.NoteStickyBypassBindingChange()
		return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
	}
	state.NoteStickyBypassReason("owner_excluded")
	return &AccountSelectionResult{Account: s.selected}, OpenAIAccountScheduleDecision{SelectedAccountID: s.selected.ID}, nil
}

func TestStickyBypassProxyRetryUsesFinalPassReason(t *testing.T) {
	withResilienceSettings(t, nil)
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	scheduler := &stickyBypassRetryScheduler{selected: &Account{ID: 36052, Platform: PlatformOpenAI}}
	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true"),
		openaiScheduler:  scheduler,
		cache: &schedulerTestGatewayCache{
			sessionBindings: map[string]int64{"openai:proxy-retry-session": 36051},
		},
		openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: 10 * time.Minute, maxEntries: 16,
		}),
	}
	svc.openaiProxyStreamCircuit.recordFailure(5056, time.Now())
	logs := captureOpenAIObservationLogs(t)
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "proxy-retry-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, scheduler.selected.ID, selection.Account.ID)
	require.Equal(t, 2, scheduler.calls, "exercise the internal quarantine retry")
	require.Equal(t, "owner_excluded", state.StickyBypassReason())
	require.False(t, state.StickyBypassChangesBinding(), "binding diagnostics also belong to the final pass")
	require.Contains(t, logs.String(), `"reason":"owner_excluded"`)
	require.Contains(t, logs.String(), `"level":"INFO"`)
	require.Equal(t, 1, strings.Count(logs.String(), `"msg":"openai.sticky_bypass"`))
	require.False(t, state.ClaimStickyBypassLog(), "internal resets must preserve the request's log claim")
}

func TestResilienceOpenAILegacySchedulerKeepsSoftBinding(t *testing.T) {
	const key = "openai:" + lrSession

	t.Run("last_resort", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, sharesched.SchedulerSub2API, 5, 1)
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
		sel := selectOpenAILastResort(t, env)
		require.Equal(t, lrAccountB, sel.Account.ID, "the original order is by priority")
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "the soft-penalised owner keeps the binding")
	})

	t.Run("healthy_alternative", func(t *testing.T) {
		logs := captureOpenAIObservationLogs(t)
		env := newOpenAILastResortEnv(t, sharesched.SchedulerSub2API, 5, 1)
		healthy := env.account(lrAccountB)
		healthy.TempUnschedulableUntil, healthy.TempUnschedulableReason = nil, ""
		healthy.RateLimitResetAt, healthy.OverloadUntil = nil, nil
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)

		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(cacheableContext(), st)
		gid := shareTestGroupID
		sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", lrSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.Equal(t, lrAccountB, sel.Account.ID)
		require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(ctx, sel), &gid, lrSession, sel.Account.ID))
		if sel.ReleaseFunc != nil {
			sel.ReleaseFunc()
		}
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "served elsewhere, the binding stays on the owner")
		require.Contains(t, logs.String(), `"msg":"openai.sticky_bypass"`)
		require.Contains(t, logs.String(), `"level":"INFO"`)
		require.Contains(t, logs.String(), `"owner":1`)
		require.Contains(t, logs.String(), `"selected":2`)
		require.Contains(t, logs.String(), `"reason":"soft_owner_preserved"`)
	})
}

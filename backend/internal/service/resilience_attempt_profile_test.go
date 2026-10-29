//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAttemptWarmUsesSessionStickyLayer(t *testing.T) {
	selection := &AccountSelectionResult{Account: &Account{ID: 7}}
	base := OpenAIAccountScheduleDecision{StickySessionHit: true, Layer: openAIAccountScheduleLayerSessionSticky}
	require.True(t, OpenAIAttemptWarm(base, selection, false))
	require.False(t, OpenAIAttemptWarm(OpenAIAccountScheduleDecision{StickySessionHit: true, Layer: openAIAccountScheduleLayerGuardianParent}, selection, false))
	selection.StickyCapacitySpillover = true
	require.False(t, OpenAIAttemptWarm(base, selection, false))
	require.False(t, OpenAIAttemptWarm(base, selection, true))
}

func TestFirstOutputColdFloorUsesMatchingAttemptProfileOnly(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{}
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	started := time.Now()
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, started))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, AttemptStart: started, BodyBytes: 128 * 1024})
	require.Equal(t, 45*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", started))
	require.Equal(t, 20*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 8, "gpt-5.5", "", started))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, AttemptStart: started, BodyBytes: 256 * 1024})
	require.Equal(t, 75*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", started))
}

func TestLastCandidateUsesSchedulerRequestEligibility(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	r, _, repo, _ := newFallbackFixture(t)
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(nil) })
	accounts := []Account{healthyAccount(2), healthyAccount(3)}
	for i := range accounts {
		accounts[i].Type = AccountTypeAPIKey
	}
	accounts[1].Extra = map[string]any{"openai_responses_supported": false}
	repo.byGroup[20] = accounts
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:       &schedulerTestGatewayCache{}, cfg: cfg, resilience: r,
	}
	ctx := budgetServiceCtx(t, 20)
	groupID := int64(20)
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "", "gpt-test", nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	start := resilience.StateFrom(ctx).Budget().Started()
	require.Equal(t, 240*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 2, "gpt-test", "", start),
		"a nominal candidate missing the endpoint capability must not cause a 20s cutoff")

	_, _, err = svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "", "gpt-test", nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, false)
	require.NoError(t, err)
	require.False(t, resilienceLastCandidate(ctx, 2), "each selection replaces the request gate")
	end := time.Now().Add(time.Minute)
	repo.byGroup[20][1].OverloadUntil = &end
	r.lastResortGroups.drop(20)
	require.False(t, resilienceLastCandidate(ctx, 2), "a soft penalty is still an eligible last-resort alternative")
}

func TestHTTPContinuationEligibilityInMixedAccountGroup(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	r, _, repo, _ := newFallbackFixture(t)
	group := int64(20)
	model := "gpt-test"
	accounts := []Account{*capabilityTestAccount(2), *capabilityTestAccount(3)}
	accounts[1].Type, accounts[1].Priority = AccountTypeOAuth, 1
	repo.byGroup[group] = accounts
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeHTTPBridge
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:       &schedulerTestGatewayCache{}, cfg: cfg, resilience: r,
	}
	ctx := budgetServiceCtx(t, group)
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "resp_previous", "", model, nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, false)
	require.NoError(t, err)
	require.Equal(t, accounts[0].ID, selection.Account.ID)
	require.True(t, r.soleCandidate(ctx, group, accounts[0].ID), "HTTP continuation cannot fail over to OAuth")
	end := time.Now().Add(8 * time.Second)
	repo.byGroup[group][0].OverloadUntil = &end
	r.lastResortGroups.drop(group)
	svc.accountRepo = repo
	require.InDelta(t, 8, svc.NoAccountRetryAfter(ctx, &group, model).Seconds(), 0.5)
	require.Equal(t, resilience.RetryAdvice{Seconds: 8, Source: "recovery"}, svc.RetryAdvice(ctx, &group, model))

	ctx = svc.withResilienceOpenAIEligibility(ctx, &group, "resp_previous", model, nil,
		OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityResponses, "", false, PlatformOpenAI)
	require.False(t, r.soleCandidate(ctx, group, accounts[0].ID), "WebSocket continuation still allows OAuth")
	require.Equal(t, 1, svc.RetryAdvice(ctx, &group, model).Seconds)
}

type eligibilityCountingGroupRepo struct {
	fallbackGroupRepo
	reads     int
	failFirst bool
}

func (r *eligibilityCountingGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	r.reads++
	if r.failFirst && r.reads == 1 {
		return nil, errors.New("group read unavailable")
	}
	return r.fallbackGroupRepo.GetByIDLite(ctx, id)
}

func TestSelectionEligibilityReusesPrivacyReadAndSkipsNegativeCache(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	withResilienceSettings(t, nil)
	group := &Group{ID: 21, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	model := "gpt-test"
	newFixture := func(failFirst bool) (*OpenAIGatewayService, *eligibilityCountingGroupRepo, context.Context) {
		account := capabilityTestAccount(1)
		account.GroupIDs = []int64{group.ID}
		repo := &eligibilityCountingGroupRepo{fallbackGroupRepo: fallbackGroupRepo{groups: map[int64]*Group{group.ID: group}}, failFirst: failFirst}
		cfg := testConfig()
		cfg.Gateway.Scheduling.LoadBatchEnabled = false
		svc := &OpenAIGatewayService{
			accountRepo: openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{*account}}},
			cache:       &schedulerTestGatewayCache{}, cfg: cfg, resilience: NewResilienceService(nil, newMemResilienceStore()),
			schedulerSnapshot: &SchedulerSnapshotService{groupRepo: repo, cache: &openAISnapshotCacheStub{
				snapshotAccounts: []*Account{account}, accountsByID: map[int64]*Account{account.ID: account},
			}},
		}
		return svc, repo, context.WithValue(budgetServiceCtx(t, group.ID), ctxkey.Group, group)
	}
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "first_read_error"}[failFirst], func(t *testing.T) {
			resilience.ProcessNegativeCache().InvalidateGroup(group.ID)
			baseline, baselineRepo, baselineCtx := newFixture(failFirst)
			_, _, baselineErr := baseline.selectAccountWithQualityRouting(baselineCtx, &group.ID, "", "", model, nil,
				OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, "", false, PlatformOpenAI, false, false)
			svc, repo, ctx := newFixture(failFirst)
			_, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group.ID, "", "", model, nil,
				OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, false)
			require.Equal(t, baselineErr == nil, err == nil)
			require.Equal(t, 1, baselineRepo.reads)
			require.Equal(t, baselineRepo.reads, repo.reads, "installing the gate adds no group query")
			gate := resilienceOpenAIEligibility(ctx, group.ID, model)
			require.NotNil(t, gate)
			require.Equal(t, failFirst, gate.req.RequirePrivacySet)
			if failFirst {
				require.ErrorIs(t, err, ErrNoAvailableAccounts)
				require.False(t, gate.allows(capabilityTestAccount(1), true, false, time.Now()), "gate and selection retain the first fail-closed result")
			}
		})
	}
	variant := "openai:" + string(OpenAIUpstreamTransportHTTPSSE) + ":" + string(OpenAIEndpointCapabilityResponses) + ":" + PlatformOpenAI
	svc, repo, ctx := newFixture(false)
	resilience.ProcessNegativeCache().Put(selectionNegativeKey(ctx, &group.ID, model, variant), group.ID, resilience.CachedResponse{}, time.Minute, time.Now())
	t.Cleanup(func() { resilience.ProcessNegativeCache().InvalidateGroup(group.ID) })
	_, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group.ID, "", "", model, nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, false)
	require.ErrorIs(t, err, errNegativeCached)
	require.Zero(t, repo.reads, "negative-cache hits return before privacy reads")
	require.Nil(t, resilienceOpenAIEligibility(ctx, group.ID, model))
}

func TestFirstOutputWarmToColdUsesSameAttemptStartAndColdFloor(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{}
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	start := time.Now()
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, AttemptStart: start, Warm: true, BodyBytes: 256 * 1024})
	warm := svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", start)
	warmAfterHeaders := svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", start)
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 8, AttemptStart: start, Warm: false, BodyBytes: 256 * 1024})
	cold := svc.openAIFirstOutputTimeoutFor(ctx, 8, "gpt-5.5", "", start)
	coldAfterHeaders := svc.openAIFirstOutputTimeoutFor(ctx, 8, "gpt-5.5", "", start)
	require.Equal(t, 30*time.Second, warm)
	require.Equal(t, 75*time.Second, cold)
	require.Equal(t, start.Add(warm), start.Add(warmAfterHeaders), "headers do not renew the warm deadline")
	require.Equal(t, start.Add(cold), start.Add(coldAfterHeaders), "headers do not renew the cold deadline")
}

func TestReportOpenAIAttemptSuccessSamplesOnlyWarmStreams(t *testing.T) {
	for _, tt := range []struct {
		name   string
		warm   bool
		stream bool
		sample bool
	}{
		{name: "warm stream", warm: true, stream: true, sample: true},
		{name: "cold stream", stream: true},
		{name: "warm nonstream", warm: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			account := &Account{ID: 7, Platform: PlatformOpenAI}
			start := time.Now().Add(-12 * time.Second)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: start, Warm: tt.warm, Stream: tt.stream})
			st.NoteFirstOutput(start.Add(11 * time.Second))
			svc.ReportOpenAIAttemptSuccess(ctx, account, "gpt-test")
			key, ok := openAIAccountModelTransientKey(account.ID, "gpt-test")
			require.True(t, ok)
			state := svc.getOpenAIAccountLatencyHoldState()
			state.mu.Lock()
			entry, sampled := state.entries[key]
			state.mu.Unlock()
			require.Equal(t, tt.sample, sampled)
			if tt.sample {
				require.Equal(t, float64(1), entry.weightedSamples)
				require.Equal(t, float64(1), entry.weightedSlow, "sample uses delivered content minus attempt start")
			}
		})
	}
}

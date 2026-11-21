//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSchedBoardDefaultTierMatchesDispatch(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, fast, ultra, accountType, endpoint, manifest, model string
		pricing, free                                             bool
		want                                                      string
	}{
		{name: "inherited default", accountType: AccountTypeOAuth},
		{name: "forced fast unsupported", fast: OpenAITierModeForceOn, accountType: AccountTypeOAuth, want: openAITierUnavailable},
		{name: "forced fast official", fast: OpenAITierModeForceOn},
		{name: "forced ultra unsupported", ultra: OpenAITierModeForceOn, accountType: AccountTypeOAuth, want: openAITierUnavailable},
		{name: "forced ultra official", ultra: OpenAITierModeForceOn},
		{name: "forced ultra third party unknown", ultra: OpenAITierModeForceOn, endpoint: "https://synthetic.invalid", want: openAITierUnavailable},
		{name: "forced ultra manifest", ultra: OpenAITierModeForceOn, accountType: AccountTypeOAuth, manifest: "fresh"},
		{name: "forced ultra stale manifest", ultra: OpenAITierModeForceOn, accountType: AccountTypeOAuth, manifest: "stale", want: openAITierUnavailable},
		{name: "forced standard", fast: OpenAITierModeForceOff, ultra: OpenAITierModeForceOff, accountType: AccountTypeOAuth},
		{name: "mapped model", ultra: OpenAITierModeForceOn, accountType: AccountTypeOAuth, model: "synthetic-text", want: openAITierUnavailable},
		{name: "paid fast margin", fast: OpenAITierModeForceOn, pricing: true},
		{name: "free fast margin", fast: OpenAITierModeForceOn, pricing: true, free: true, want: openAIProfitFilterReasonThreshold},
		{name: "paid ultra margin", ultra: OpenAITierModeForceOn, pricing: true},
		{name: "free ultra margin", ultra: OpenAITierModeForceOn, pricing: true, free: true, want: openAIProfitFilterReasonThreshold},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const upstream = "gpt-6-astra"
			model := tc.model
			if model == "" {
				model = upstream
			}
			account := selectionFixtureAccount(10, 1)
			if tc.accountType != "" {
				account.Type = tc.accountType
			}
			endpoint := tc.endpoint
			if endpoint == "" {
				endpoint = "https://api.openai.com"
			}
			account.Credentials = map[string]any{"base_url": endpoint, "model_mapping": map[string]any{model: upstream}}
			if tc.manifest != "" {
				at := time.Now()
				if tc.manifest == "stale" {
					at = at.Add(-48 * time.Hour)
				}
				account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{SyncedAt: at.UTC().Format(time.RFC3339), Models: map[string]UpstreamModelMetadata{upstream: {TierCredentialFingerprint: capabilityFingerprint(&account), CodexToolCapabilities: map[string]json.RawMessage{"service_tiers": json.RawMessage(`[{"id":"ultrafast"}]`)}}}})
			}
			group := explicitTierGroup(tc.fast, tc.ultra)
			group.ID = 7
			group.RateMultiplier = 1
			if tc.pricing {
				group.ProfitControlEnabled = true
				group.RateMultiplier = .2
				group.FreeOpenAIFast, group.FreeOpenAIUltrafast = tc.free, tc.free
				rate := .12
				account.RateMultiplier = &rate
			}
			f := newSelectionRuntimeFixture(t, account)
			f.svc.billingService = NewBillingService(nil, nil)
			f.svc.cfg.Gateway.Scheduling.DbFallbackEnabled = true
			f.svc.schedulerSnapshot = NewSchedulerSnapshotService(nil, nil, f.repo, &groupRepoStubForAdmin{getByID: group}, f.svc.cfg)
			body, err := json.Marshal(map[string]string{"model": model})
			require.NoError(t, err)
			ctx := resilience.WithRequestState(profitControlTestCtx(group), resilience.NewRequestState())
			ctx = resilience.WithCapabilityRequest(ctx, APIProtocolResponses, true)
			ctx = WithOpenAITierInput(ctx, body, APIProtocolResponses, "")
			ctx, _ = f.svc.WithOpenAIRequestPricingContext(ctx, &group.ID)
			compatible, reason := f.svc.isAccountRequestCompatibleReason(ctx, &account, OpenAIAccountScheduleRequest{GroupID: &group.ID, Platform: PlatformOpenAI, RequestedModel: model, RequiredCapability: OpenAIEndpointCapabilityChatCompletions, RequiredTransport: OpenAIUpstreamTransportHTTPSSE})
			require.Equal(t, tc.want, reason, "default request admission")
			require.Equal(t, tc.want == "", compatible)
			other := explicitTierGroup(OpenAITierModeForceOff, OpenAITierModeForceOff)
			other.ID = 8
			otherKey := &APIKey{GroupID: &other.ID, Group: other}
			adminContext := WithServingPricingKeySnapshot(WithBillingAPIKeySnapshot(profitControlTestCtx(other), otherKey), otherKey)
			for _, previewContext := range []context.Context{context.Background(), adminContext} {
				rows, supply, err := f.svc.selectionBoard.Board(previewContext, group.ID, model)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				require.Empty(t, f.attempts, "preview must not acquire a slot")
				wantLayer, wantAvailable := "front", 1
				if tc.want != "" {
					wantLayer, wantAvailable = "stopped", 0
				}
				require.Equal(t, wantLayer, rows[0].Layer)
				require.Equal(t, wantAvailable, supply.Available)
			}
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group.ID, "", "", model, nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformOpenAI)
			if tc.want != "" {
				require.ErrorIs(t, err, ErrNoAvailableAccounts)
				require.Nil(t, selected)
			} else {
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, account.ID, selected.Account.ID)
				if selected.ReleaseFunc != nil {
					selected.ReleaseFunc()
				}
			}
		})
	}
}

func TestSchedBoardTierGroupReadFailure(t *testing.T) {
	failed := errors.New("synthetic group read failed")
	s := &OpenAIGatewayService{accountRepo: &selectionBoardRepo{}, schedulerSnapshot: &SchedulerSnapshotService{groupRepo: &groupRepoStubForAdmin{getErr: failed}}}
	rows, supply, err := NewOpenAISelectionBoard(s, nil).Board(context.Background(), 7, "gpt-6.2-sol")
	require.ErrorIs(t, err, failed)
	require.Nil(t, rows)
	require.Nil(t, supply)
}

//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func selectUncollectedEndpoint(t *testing.T, f *selectionRuntimeFixture, endpoint, session string) *Account {
	t.Helper()
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	var result *AccountSelectionResult
	var err error
	switch endpoint {
	case "count_tokens":
		account, err := f.svc.SelectAccountForTokenCount(ctx, ptrInt64Selection(7), session, "m", OpenAIEndpointCapabilityResponses, PlatformOpenAI)
		require.NoError(t, err)
		return account
	case "models":
		rows, err := f.svc.fetchScheduledOpenAIModels(ctx, &Group{ID: 7}, 1, func(_ context.Context, _ *Account) (*OpenAIModelsResponse, error) {
			return &OpenAIModelsResponse{}, nil
		})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0].account
	case "images":
		result, _, err = f.svc.SelectAccountWithSchedulerForImages(ctx, ptrInt64Selection(7), session, "m", nil, OpenAIImagesCapabilityNative)
	default:
		model := "m"
		if endpoint == "live" {
			model = ""
		}
		result, _, err = f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, model, nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapability(endpoint), false, false, false)
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
	return result.Account
}

func newUncollectedSelectionFixture(t *testing.T, endpoint string, priority int) *selectionRuntimeFixture {
	t.Helper()
	a, b := selectionFixtureAccount(1, priority), selectionFixtureAccount(2, 2)
	if endpoint == "live" {
		a.Type, b.Type = AccountTypeOAuth, AccountTypeOAuth
	}
	return newSelectionRuntimeFixture(t, a, b)
}

func TestOpenAISelectionUncollectedEndpointsUseOnlyManualStanding(t *testing.T) {
	for _, endpoint := range []string{"images", "embeddings", "count_tokens", "models", "live", "alpha_search"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newUncollectedSelectionFixture(t, endpoint, 1)
			model := "m"
			if endpoint == "models" || endpoint == "live" {
				model = ""
			}
			key := fakeSchedViewKey{7, 1, model}
			f.view.States[key] = schedquality.StateView{State: schedquality.StateCooling}
			require.Equal(t, int64(1), selectUncollectedEndpoint(t, f, endpoint, "").ID, "automatic cooling must not affect uncollected traffic")
			f.view.States[key] = schedquality.StateView{State: schedquality.StateCooling, AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote}}
			require.Equal(t, int64(2), selectUncollectedEndpoint(t, f, endpoint, "").ID, "manual override still applies")
			f.view.States[key] = schedquality.StateView{State: schedquality.StateCooling}
			require.Equal(t, int64(1), selectUncollectedEndpoint(t, f, endpoint, "").ID)
		})
	}
}

func TestOpenAISelectionUncollectedEndpointsSkipMigrationAndTakeover(t *testing.T) {
	for _, endpoint := range []string{"images", "embeddings", "count_tokens", "live", "alpha_search"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, state := range []schedquality.StateView{
				{},
				{State: schedquality.StateCooling},
				{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote}},
			} {
				f := newUncollectedSelectionFixture(t, endpoint, 5)
				model := "m"
				if endpoint == "live" {
					model = ""
				}
				f.view.States[fakeSchedViewKey{7, 1, model}] = state
				f.cache.sessionBindings["openai:session"] = 1
				require.Equal(t, int64(1), selectUncollectedEndpoint(t, f, endpoint, "session").ID)
				require.Equal(t, int64(1), f.cache.sessionBindings["openai:session"])
				require.Empty(t, f.cache.casKeys)
				require.Empty(t, f.cache.ttlKeys)
				require.Zero(t, f.loads.batches)
			}
		})
	}
}

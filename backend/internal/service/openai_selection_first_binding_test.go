//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestStickyFirstBindingWaitsForSuccessfulDelivery(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel", "retry"} {
		t.Run(outcome, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			group := int64(7)
			selectTarget := func() *AccountSelectionResult {
				selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "v2:new-session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				require.NoError(t, err)
				require.NotNil(t, selected)
				t.Cleanup(selected.ReleaseFunc)
				return selected
			}
			selected := selectTarget()
			require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, &group, "v2:new-session", selected.Account.ID))
			require.NoError(t, f.svc.refreshStickySessionTTL(ctx, &group, "v2:new-session", time.Hour, selected.Account.ID))
			require.Zero(t, f.cache.sessionBindings["openai:v2:new-session"], "selection, admission, and refresh must not publish a new owner")
			if outcome == "retry" {
				CompleteOpenAIStickyChange(ctx, selected.Account.ID, false, false)
				selected = selectTarget()
			}
			CompleteOpenAIStickyChange(ctx, selected.Account.ID, outcome == "success" || outcome == "retry", outcome == "cancel")
			if outcome == "success" || outcome == "retry" {
				require.Equal(t, selected.Account.ID, f.cache.sessionBindings["openai:v2:new-session"])
			} else {
				require.Zero(t, f.cache.sessionBindings["openai:v2:new-session"])
			}
		})
	}
}

type bindingReadErrorCache struct{ *selectionRuntimeCache }

func (c *bindingReadErrorCache) GetSessionBinding(context.Context, int64, string) (StickySessionBinding, error) {
	return StickySessionBinding{}, errors.New("synthetic Redis read timeout")
}
func (c *bindingReadErrorCache) ResolveSessionBinding(context.Context, int64, string, string, bool) (StickySessionBinding, error) {
	return StickySessionBinding{}, errors.New("synthetic Redis read timeout")
}
func TestStickyFirstBindingReadFailureDoesNotBindBeforeDelivery(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	f.svc.cache = &bindingReadErrorCache{f.cache}
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	group := int64(7)
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "v2:synthetic", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, &group, "v2:synthetic", selected.Account.ID))
	require.Zero(t, f.cache.sessionBindings["openai:v2:synthetic"], "a failed binding read must not enable a pre-delivery write")
	CompleteOpenAIStickyChange(ctx, selected.Account.ID, true, false)
	require.Zero(t, f.cache.sessionBindings["openai:v2:synthetic"], "without a snapshot this request cannot safely commit")
}

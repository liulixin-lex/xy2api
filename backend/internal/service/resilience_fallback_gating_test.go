//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// PM decision P2-3: under an availability fallback a group capability flag
// is the origin group's eligibility and the serving group's capability.
func TestFallbackImageGenerationGate(t *testing.T) {
	for _, tc := range []struct {
		name             string
		origin, fallback bool
		want             GroupGate
	}{
		{"both_allow", true, true, GroupGateAllowed},
		{"origin_denies", false, true, GroupGateDenied},
		{"fallback_denies", true, false, GroupGateNoAccount},
		{"both_deny", false, false, GroupGateDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, authenticated, moved := fallbackBillingFixture(PlatformOpenAI)
			authenticated.Group.AllowImageGeneration = tc.origin
			moved.Group.AllowImageGeneration = tc.fallback
			require.Equal(t, tc.want, ImageGenerationGate(ctx, moved))
		})
	}

	// Without a fallback the key's own group decides, as before.
	_, authenticated, _ := fallbackBillingFixture(PlatformOpenAI)
	authenticated.Group.AllowImageGeneration = false
	require.Equal(t, GroupGateDenied, ImageGenerationGate(context.Background(), authenticated))
	require.Equal(t, GroupGateAllowed, ImageGenerationGate(context.Background(), &APIKey{ID: 1}), "ungrouped keys keep their access")
}

// The service-side image gate answers the origin's refusal as a permission
// error and the fallback group's as no available account, without naming it.
func TestFallbackImageGenerationGateResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, gate := range []GroupGate{GroupGateDenied, GroupGateNoAccount} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		writeImageGenerationGateError(c, gate)
		if gate == GroupGateDenied {
			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.Equal(t, ImageGenerationPermissionMessage(), gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
			continue
		}
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Equal(t, "api_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.Equal(t, "Service temporarily unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
		require.NotContains(t, rec.Body.String(), "fallback")
		require.NotContains(t, rec.Body.String(), "image")
	}
}

// PM decision P2-2: the profit gate prices the request at the origin group's
// rate (the price the user pays) while the margin stays the serving group's.
// The fixture's origin group costs 1x, the fallback group 3x.
func TestProfitControlPricesFallbackAtOriginGroup(t *testing.T) {
	t.Run("gateway", func(t *testing.T) {
		ctx, _, moved := fallbackBillingFixture(PlatformAnthropic)
		moved.Group.ProfitControlEnabled = true
		moved.Group.SubscriptionType = SubscriptionTypeStandard
		// The fallback middleware already pointed the request's group at the
		// serving group when the token request froze its pricing.
		ctx = context.WithValue(ctx, ctxkey.Group, moved.Group)
		ctx, _ = WithGatewayTokenRequestPricing(ctx)
		ctx = (&GatewayService{}).withGatewayProfitControlGate(ctx, moved.GroupID)
		gate, _ := ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
		require.NotNil(t, gate)
		require.Equal(t, moved.Group.ID, gate.groupID, "margin and platform stay the serving group's")
		require.InDelta(t, 1.0, gate.threshold, 1e-12, "the user pays the origin group's price")
	})

	t.Run("openai", func(t *testing.T) {
		ctx, _, moved := fallbackBillingFixture(PlatformOpenAI)
		moved.Group.ProfitControlEnabled = true
		moved.Group.SubscriptionType = SubscriptionTypeStandard
		ctx = context.WithValue(ctx, ctxkey.Group, moved.Group)
		ctx = context.WithValue(ctx, openAIPricingAtCtxKey{}, time.Now())
		gate := (&OpenAIGatewayService{}).resolveOpenAIProfitControlGate(ctx, moved.GroupID)
		require.NotNil(t, gate)
		require.Equal(t, moved.Group.ID, gate.groupID)
		require.InDelta(t, 1.0, gate.threshold, 1e-12, "the user pays the origin group's price")
	})
}

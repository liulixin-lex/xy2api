package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGroupFastPriorityOverridesPolicy(t *testing.T) {
	for _, action := range []string{BetaPolicyActionFilter, BetaPolicyActionBlock, BetaPolicyActionPass, OpenAIFastPolicyActionForcePriority} {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			t.Run(action+"/"+accountType, func(t *testing.T) {
				svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
					ServiceTier: OpenAIFastTierAny, Action: action, Scope: BetaPolicyScopeAll,
				}}})
				account := &Account{Platform: PlatformOpenAI, Type: accountType}
				ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
					ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true,
				})
				for _, tier := range []string{"", "default", "flex", "priority", "ultrafast"} {
					t.Run("tier="+tier, func(t *testing.T) {
						body := []byte(`{"type":"response.create","model":"gpt-6-sol","input":[]}`)
						if tier != "" {
							body = []byte(`{"type":"response.create","model":"gpt-6-sol","service_tier":"` + tier + `","input":[]}`)
						}
						httpBody, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-6-sol", body)
						require.NoError(t, err)
						require.Equal(t, "priority", gjson.GetBytes(httpBody, "service_tier").String())
						wsBody, blocked, err := svc.applyOpenAIFastPolicyToWSResponseCreate(ctx, account, "gpt-6-sol", body)
						require.NoError(t, err)
						require.Nil(t, blocked)
						require.Equal(t, "priority", gjson.GetBytes(wsBody, "service_tier").String())
					})
				}
			})
		}
	}
}

func TestGroupFastPriorityKeepsOtherGroupsPolicy(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "forced"}[forced], func(t *testing.T) {
			svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{
				{ServiceTier: OpenAIFastTierAny, Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll},
				{ServiceTier: OpenAIFastTierAny, Action: BetaPolicyActionBlock, Scope: BetaPolicyScopeAll, UserIDs: []int64{42}},
			}})
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			groupCtx := context.WithValue(context.Background(), ctxkey.Group, &Group{
				ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: forced,
			})
			for _, userID := range []int64{42, 43} {
				ctx := context.WithValue(groupCtx, ctxkey.UserID, userID)
				body := []byte(`{"type":"response.create","model":"gpt-6-sol","service_tier":"priority"}`)
				httpBody, httpErr := svc.applyOpenAIFastPolicyToBody(ctx, account, "gpt-6-sol", body)
				wsBody, blocked, wsErr := svc.applyOpenAIFastPolicyToWSResponseCreate(ctx, account, "gpt-6-sol", body)
				require.NoError(t, wsErr)
				if forced {
					require.NoError(t, httpErr)
					require.Nil(t, blocked)
					require.Equal(t, "priority", gjson.GetBytes(httpBody, "service_tier").String())
					require.Equal(t, "priority", gjson.GetBytes(wsBody, "service_tier").String())
				} else if userID == 42 {
					require.Error(t, httpErr)
					require.NotNil(t, blocked)
				} else {
					require.NoError(t, httpErr)
					require.Nil(t, blocked)
					require.False(t, gjson.GetBytes(httpBody, "service_tier").Exists())
					require.False(t, gjson.GetBytes(wsBody, "service_tier").Exists())
				}
			}
		})
	}
}

func TestGroupFastPriorityBilling(t *testing.T) {
	for _, tt := range []struct {
		name         string
		forced       bool
		free         bool
		unhydrated   bool
		noTier       bool
		observed     string
		inputTokens  int
		wantTier     string
		wantFastCost bool
	}{
		{name: "default echo keeps forced fast", forced: true, observed: "default", wantTier: "priority", wantFastCost: true},
		{name: "honoured fast", forced: true, observed: "priority", wantTier: "priority", wantFastCost: true},
		{name: "missing response tier", forced: true, wantTier: "priority", wantFastCost: true},
		{name: "forced group controls declared flex", forced: true, observed: "flex", wantTier: "priority", wantFastCost: true},
		{name: "long context", forced: true, observed: "default", inputTokens: 300000, wantTier: "priority", wantFastCost: true},
		{name: "free fast", forced: true, free: true, observed: "default", wantTier: "priority", wantFastCost: true},
		{name: "ordinary group still downgrades", observed: "default", wantTier: "default"},
		{name: "unhydrated group still downgrades", forced: true, unhydrated: true, observed: "default", wantTier: "default"},
		{name: "response cannot invent outbound fast", forced: true, noTier: true, observed: "priority"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			groupID := int64(7)
			group := &Group{
				ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: !tt.unhydrated,
				ForceOpenAIFast: tt.forced, FreeOpenAIFast: tt.free, RateMultiplier: 0.2,
				LongContextPricingEnabled: true, LongContextPricingScope: "all",
			}
			inputTokens := tt.inputTokens
			if inputTokens == 0 {
				inputTokens = 100
			}
			result := &OpenAIForwardResult{
				RequestID: "group-fast-billing", Model: "gpt-6-sol", Duration: time.Second,
				UpstreamResponseServiceTier: tt.observed,
				Usage:                       OpenAIUsage{InputTokens: inputTokens + 30, OutputTokens: 50, CacheReadInputTokens: 20, CacheCreationInputTokens: 10},
			}
			if !tt.noTier {
				tier := "priority"
				result.ServiceTier = &tier
			}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: result, APIKey: &APIKey{ID: 10, GroupID: &groupID, Group: group}, User: &User{ID: 20},
				Account: &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			log := usageRepo.lastLog
			t.Logf("tier=%q rate=%.1f total=%.8f actual=%.8f", optionalStringValue(log.ServiceTier), log.RateMultiplier, log.TotalCost, log.ActualCost)
			require.Equal(t, tt.wantTier, optionalStringValue(log.ServiceTier))
			require.InDelta(t, 0.2, log.RateMultiplier, 1e-12)
			require.InDelta(t, 0.2, group.RateMultiplier, 1e-12)
			require.Equal(t, tt.observed, result.UpstreamResponseServiceTier)
			inputPrice, outputPrice, cachePrice, cacheWritePrice := 2e-6, 10e-6, 0.2e-6, 2.5e-6
			if tt.inputTokens > 272000 {
				inputPrice *= 2
				outputPrice *= 1.5
				cachePrice *= 2
				cacheWritePrice *= 2
			}
			factor := 1.0
			if tt.wantFastCost {
				factor = 2
			}
			standardTotal := float64(inputTokens)*inputPrice + 50*outputPrice + 20*cachePrice + 10*cacheWritePrice
			require.InDelta(t, float64(inputTokens)*inputPrice*factor, log.InputCost, 1e-10)
			require.InDelta(t, 50*outputPrice*factor, log.OutputCost, 1e-10)
			require.InDelta(t, 20*cachePrice*factor, log.CacheReadCost, 1e-10)
			require.InDelta(t, 10*cacheWritePrice*factor, log.CacheCreationCost, 1e-10)
			require.InDelta(t, standardTotal*factor, log.TotalCost, 1e-10)
			customerFactor := factor
			if tt.free {
				customerFactor = 1
			}
			require.InDelta(t, standardTotal*customerFactor*0.2, log.ActualCost, 1e-10)
			require.InDelta(t, log.ActualCost, userRepo.lastAmount, 1e-10)
		})
	}
}

func TestGroupFastPriorityScope(t *testing.T) {
	for _, tt := range []struct {
		name            string
		groupPlatform   string
		accountPlatform string
		hydrated        bool
		wantForced      bool
	}{
		{"openai", PlatformOpenAI, PlatformOpenAI, true, true},
		{"composite openai", PlatformComposite, PlatformOpenAI, true, true},
		{"composite grok", PlatformComposite, PlatformGrok, true, false},
		{"unsupported group", PlatformAnthropic, PlatformOpenAI, true, false},
		{"unhydrated", PlatformOpenAI, PlatformOpenAI, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy())
			group := &Group{ID: 7, Platform: tt.groupPlatform, Status: StatusActive, Hydrated: tt.hydrated, ForceOpenAIFast: true}
			account := &Account{Platform: tt.accountPlatform, Type: AccountTypeAPIKey}
			ctx := context.WithValue(context.Background(), ctxkey.Group, group)
			action, _ := svc.evaluateOpenAIFastPolicy(ctx, account, "gpt-6-sol", "priority")
			wantAction := BetaPolicyActionFilter
			if tt.wantForced {
				wantAction = OpenAIFastPolicyActionForcePriority
			}
			require.Equal(t, wantAction, action)
		})
	}
}

func TestGroupFastPriorityForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, action := range []string{BetaPolicyActionFilter, BetaPolicyActionBlock} {
		for _, passthrough := range []bool{false, true} {
			name := action + map[bool]string{false: "/native", true: "/passthrough"}[passthrough]
			t.Run(name, func(t *testing.T) {
				body := []byte(`{"model":"gpt-6-sol","input":"hello","stream":false}`)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"id":"group-fast","object":"response","status":"completed","model":"gpt-6-sol","service_tier":"default","output":[],"usage":{"input_tokens":100,"output_tokens":50}}`)),
				}}
				svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
					ServiceTier: OpenAIFastTierAny, Action: action, Scope: BetaPolicyScopeAll,
				}}})
				svc.cfg = &config.Config{}
				svc.httpUpstream = upstream
				account := &Account{
					ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "fixture-key"},
					Extra:       map[string]any{"openai_responses_supported": true, "openai_passthrough": passthrough},
				}
				ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
					ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true,
				})
				result, err := svc.Forward(ctx, c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "priority", gjson.GetBytes(upstream.lastBody, "service_tier").String())
				require.Equal(t, "priority", optionalStringValue(result.ServiceTier))
				require.Equal(t, "default", result.UpstreamResponseServiceTier)
				require.Contains(t, recorder.Body.String(), `"service_tier":"default"`)
			})
		}
	}
}

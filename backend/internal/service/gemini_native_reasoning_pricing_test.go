//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiNativeReasoningPricingUsesExplicitForwardedLevel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"api_key", "oauth_ai_studio", "oauth_code_assist", "antigravity"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name       string
				thinking   map[string]any
				wantEffort string
				multiplier float64
			}{
				{"high", map[string]any{"thinkingLevel": "high"}, "high", 2},
				{"uppercase_high", map[string]any{"thinkingLevel": "HIGH"}, "high", 2},
				{"snake_case_high", map[string]any{"thinking_level": "high"}, "high", 2},
				{"minimal", map[string]any{"thinkingLevel": "minimal"}, "minimal", 1.1},
				{"low", map[string]any{"thinkingLevel": "low"}, "low", 1.2},
				{"medium", map[string]any{"thinkingLevel": "medium"}, "medium", 1.5},
				{"missing_level", nil, "", 1},
				{"budget_only", map[string]any{"thinkingBudget": 24576}, "", 1},
				{"unspecified_level", map[string]any{"thinkingLevel": "THINKING_LEVEL_UNSPECIFIED"}, "", 1},
			} {
				mode := "buffered"
				if stream {
					mode = "streaming"
				}
				t.Run(provider+"/"+mode+"/"+tc.name, func(t *testing.T) {
					const model = "gemini-3.1-pro-high"
					request := map[string]any{
						"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": "hello"}}}},
					}
					if tc.name == "snake_case_high" {
						request["generation_config"] = map[string]any{"thinking_config": tc.thinking}
					} else if tc.thinking != nil {
						request["generationConfig"] = map[string]any{"thinkingConfig": tc.thinking}
					}
					body, err := json.Marshal(request)
					require.NoError(t, err)
					action := "generateContent"
					if stream {
						action = "streamGenerateContent"
					}
					path := "/v1beta/models/" + model + ":" + action
					c, recorder := newAntigravityCompatContext(http.MethodPost, path, body)
					responseBody := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
					wrapped := provider == "oauth_code_assist" || provider == "antigravity"
					if wrapped {
						responseBody = `{"response":` + responseBody + `}`
					}
					contentType := "application/json"
					if stream || wrapped {
						contentType = "text/event-stream"
						responseBody = "data: " + responseBody + "\n\n"
					}
					upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{contentType}},
						Body:       io.NopCloser(strings.NewReader(responseBody)),
					}}}
					account := geminiSignalTestAccount()
					var result *ForwardResult
					if provider == "antigravity" {
						account = newAntigravityCompatAccount(AccountTypeOAuth)
						svc := newAntigravityCompatService(config.GatewayConfig{}, upstream)
						result, err = svc.ForwardGemini(context.Background(), c, account, model, action, stream, body, false)
					} else {
						if provider != "api_key" {
							account.Type = AccountTypeOAuth
							account.Credentials["access_token"] = "test-token"
						}
						if provider == "oauth_code_assist" {
							account.Credentials["project_id"] = "test-project"
						}
						svc := &GeminiMessagesCompatService{
							httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{},
						}
						result, err = svc.ForwardNative(context.Background(), c, account, model, action, stream, body)
					}
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, recorder.Code)
					require.Len(t, upstream.requestBodies, 1)
					forwardedPath := "generationConfig.thinkingConfig.thinkingLevel"
					requestedLevel, _ := tc.thinking["thinkingLevel"].(string)
					if tc.name == "snake_case_high" {
						forwardedPath = "generation_config.thinking_config.thinking_level"
						requestedLevel, _ = tc.thinking["thinking_level"].(string)
					}
					if wrapped {
						forwardedPath = "request." + forwardedPath
					}
					require.Equal(t, requestedLevel, gjson.GetBytes(upstream.requestBodies[0], forwardedPath).String())
					require.Equal(t, tc.wantEffort, optionalStringValue(result.ReasoningEffort))
					if tc.wantEffort == "" {
						require.Nil(t, result.ReasoningEffort)
					}

					usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
					userRepo := &openAIRecordUsageUserRepoStub{}
					billing := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
					billing.resolver = NewModelPricingResolver(nil, billing.billingService)
					groupID := int64(77)
					apiKey := &APIKey{ID: 10, GroupID: &groupID, Group: &Group{
						ID: groupID, Platform: account.Platform, Status: StatusActive, Hydrated: true, RateMultiplier: 0.5,
						ModelPricing: []ChannelModelPricing{{
							Models: []string{model}, BillingMode: BillingModeToken,
							InputPrice: testPtrFloat64(0.05), OutputPrice: testPtrFloat64(0.1),
							ReasoningEffortMultipliers: map[string]float64{"minimal": 1.1, "low": 1.2, "medium": 1.5, "high": 2},
						}},
					}}
					require.NoError(t, billing.RecordUsage(context.Background(), &RecordUsageInput{
						Result: result, APIKey: apiKey, User: &User{ID: 20}, Account: account,
					}))
					require.NotNil(t, usageRepo.lastLog)
					require.Equal(t, tc.wantEffort, optionalStringValue(usageRepo.lastLog.ReasoningEffort))
					require.InDelta(t, tc.multiplier, usageRepo.lastLog.TotalCost, 1e-12)
					require.InDelta(t, tc.multiplier*0.5, usageRepo.lastLog.ActualCost, 1e-12)
					require.InDelta(t, tc.multiplier*0.5, userRepo.lastAmount, 1e-12)
				})
			}
		}
	}
}

func TestGeminiNativeRoutingPreservesUpstreamURLAndCustomerAmount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		model   = "gemini-3.7-flash"
		baseURL = "https://gemini-upstream.example.test"
	)

	for _, prefixed := range []bool{false, true} {
		prefix := "without_antigravity_prefix"
		pathPrefix := ""
		upstreamBaseURL := baseURL
		platform := PlatformGemini
		if prefixed {
			prefix = "with_antigravity_prefix"
			pathPrefix = "/antigravity"
			upstreamBaseURL += "/antigravity"
			platform = PlatformAntigravity
		}
		for _, stream := range []bool{false, true} {
			mode := "generateContent"
			if stream {
				mode = "streamGenerateContent"
			}
			t.Run(prefix+"/"+mode, func(t *testing.T) {
				account := geminiSignalTestAccount()
				account.Platform = platform
				account.Credentials["base_url"] = baseURL

				path := pathPrefix + "/v1beta/models/" + model + ":" + mode
				if stream {
					path += "?alt=sse"
				}
				body := geminiSignalTestRequest()
				c, recorder := newAntigravityCompatContext(http.MethodPost, path, body)

				responseBody := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
				contentType := "application/json"
				wantURL := upstreamBaseURL + "/v1beta/models/" + model + ":" + mode
				if stream {
					contentType = "text/event-stream"
					responseBody = "data: " + responseBody + "\n\n"
					wantURL += "?alt=sse"
				}
				var actualURL string
				upstream := &queuedHTTPUpstreamStub{
					responses: []*http.Response{{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{contentType}},
						Body:       io.NopCloser(strings.NewReader(responseBody)),
					}},
					onCall: func(req *http.Request, _ *queuedHTTPUpstreamStub) {
						actualURL = req.URL.String()
					},
				}
				svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
				result, err := svc.ForwardNative(c.Request.Context(), c, account, model, mode, stream, body)

				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Len(t, upstream.requestBodies, 1)
				require.Equal(t, wantURL, actualURL)
				require.Equal(t, 10, result.Usage.InputTokens)
				require.Equal(t, 5, result.Usage.OutputTokens)

				groupID := int64(77)
				apiKey := &APIKey{ID: 10, GroupID: &groupID, Group: &Group{
					ID: groupID, Platform: account.Platform, Status: StatusActive, Hydrated: true, RateMultiplier: 0.5,
					ModelPricing: []ChannelModelPricing{{
						Models: []string{model}, BillingMode: BillingModeToken,
						InputPrice: testPtrFloat64(0.05), OutputPrice: testPtrFloat64(0.1),
					}},
				}}
				// The legacy bucket and the new action label must bill the same forwarded usage.
				for _, endpoint := range []string{"/v1beta/models", "/v1beta/models:" + mode} {
					usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
					userRepo := &openAIRecordUsageUserRepoStub{}
					billing := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
					billing.resolver = NewModelPricingResolver(nil, billing.billingService)
					require.NoError(t, billing.RecordUsage(context.Background(), &RecordUsageInput{
						Result: result, APIKey: apiKey, User: &User{ID: 20}, Account: account,
						InboundEndpoint: endpoint, UpstreamEndpoint: "/v1beta/models",
					}))
					require.NotNil(t, usageRepo.lastLog)
					require.NotNil(t, usageRepo.lastLog.InboundEndpoint)
					require.Equal(t, endpoint, *usageRepo.lastLog.InboundEndpoint)
					require.InDelta(t, 0.5, usageRepo.lastLog.ActualCost, 1e-12)
					require.Equal(t, 1, userRepo.deductCalls)
					require.InDelta(t, 0.5, userRepo.lastAmount, 1e-12)
					t.Logf("upstream_url=%s inbound_endpoint=%s customer_amount=%.2f", actualURL, endpoint, userRepo.lastAmount)
				}
			})
		}
	}
}

func TestGeminiChatCompatReasoningPricingUsesForwardedEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		mode := "buffered"
		if stream {
			mode = "streaming"
		}
		t.Run(mode, func(t *testing.T) {
			const model = "gemini-3.1-pro"
			body, err := json.Marshal(map[string]any{
				"model": model, "stream": stream, "reasoning_effort": "high",
				"messages": []map[string]string{{"role": "user", "content": "hello"}},
			})
			require.NoError(t, err)
			responseBody := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
			contentType := "application/json"
			if stream {
				contentType = "text/event-stream"
				responseBody = "data: " + responseBody + "\n\n"
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{contentType}},
				Body:       io.NopCloser(strings.NewReader(responseBody)),
			}}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiSignalTestAccount(), body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Len(t, upstream.requestBodies, 1)
			require.Equal(t, "high", gjson.GetBytes(upstream.requestBodies[0], "generationConfig.thinkingConfig.thinkingLevel").String())
			require.Equal(t, "high", optionalStringValue(result.ReasoningEffort))

			billing := NewBillingService(&config.Config{}, nil)
			cost, err := billing.CalculateTokenCostForRequest(TokenCostRequest{
				Ctx: context.Background(), Model: model,
				Group: &Group{ID: 77, Platform: PlatformGemini, ModelPricing: []ChannelModelPricing{{
					Models: []string{model}, BillingMode: BillingModeToken,
					InputPrice: testPtrFloat64(0.05), OutputPrice: testPtrFloat64(0.1),
					ReasoningEffortMultipliers: map[string]float64{"high": 2},
				}}},
				Tokens:          UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens},
				ReasoningEffort: optionalStringValue(result.ReasoningEffort), RateMultiplier: 1,
				Resolver: NewModelPricingResolver(nil, billing),
			})
			require.NoError(t, err)
			require.InDelta(t, 2, cost.TotalCost, 1e-12)
		})
	}
}

//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestReasoningBillingUsesFinalWire(t *testing.T) {
	withResilienceSettings(t, nil)
	t.Run("dispatch_matrix", testReasoningBillingDispatchMatrix)
	for _, tc := range []struct {
		name, fields, want string
		multiplier         float64
	}{
		{"max", `,"reasoning":{"effort":"max"}`, "max", 3},
		{"xhigh", `,"reasoning":{"effort":"xhigh"}`, "xhigh", 2},
		{"thinking_only", `,"thinking":{"type":"enabled","budget_tokens":4096}`, "", 1},
		{"missing", "", "", 1},
		{"unknown", `,"reasoning":{"effort":"unrecognized"}`, "", 1},
		{"ultra", `,"reasoning":{"effort":"ultra"}`, "ultra", 1},
	} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%t", tc.name, partial), func(t *testing.T) {
				body := []byte(`{"model":"fixture-wire","input":"hello","stream":true` + tc.fields + `}`)
				terminal := `{"type":"response.completed","response":{"id":"resp_fixture","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`
				if partial {
					terminal = `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"code":"server_error","message":"fixture"},"usage":{"input_tokens":10,"output_tokens":5}}}`
				}
				stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: " + terminal + "\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://fixture.invalid"}}
				result, err := svc.Forward(context.Background(), c, account, body)
				if partial {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NotNil(t, result)
				require.Len(t, upstream.bodies, 1)
				if tc.want == "" {
					require.Nil(t, result.ReasoningEffort)
				} else {
					require.Equal(t, tc.want, *result.ReasoningEffort)
					require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
				}
				require.Equal(t, extractOpenAIReasoningEffortFromRequest(upstream.lastReq), result.ReasoningEffort)
				repo := &openAIRecordUsageLogRepoStub{inserted: true}
				recorder := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				recorder.cfg.Default.RateMultiplier = 1
				billing := NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, `{"fixture-wire":{"input_cost_per_token":0.001,"output_cost_per_token":0.002}}`))
				recorder.billingService = billing
				recorder.resolver = NewModelPricingResolver(nil, billing)
				group := &Group{ID: 4, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{"fixture-wire"}, ReasoningEffortMultipliers: map[string]float64{"max": 3, "xhigh": 2, "high": 7}}}}
				key := &APIKey{ID: 2, GroupID: &group.ID, Group: group}
				result.RequestedReasoningEffort = new("high")
				require.NoError(t, recorder.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 1}, Account: account}))
				require.Equal(t, result.ReasoningEffort, repo.lastLog.ReasoningEffort)
				require.Equal(t, "high", *repo.lastLog.RequestedReasoningEffort)
				require.InDelta(t, 0.02*tc.multiplier, repo.lastLog.TotalCost, 1e-12)
				generic := &GatewayService{billingService: billing, resolver: recorder.resolver}
				genericResult := &ForwardResult{Model: result.Model, ReasoningEffort: result.ReasoningEffort, Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 5}}
				genericCost := generic.calculateTokenCost(context.Background(), genericResult, key, result.Model, 1, time.Now())
				require.InDelta(t, repo.lastLog.TotalCost, genericCost.TotalCost, 1e-12)
				snapshot := BillingUsageSnapshot(repo.lastLog)
				require.Equal(t, result.ReasoningEffort, snapshot.ReasoningEffort)
			})
		}
	}
	// A final builder snapshot with the field removed must not recover the inbound value.
	req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader(`{"model":"fixture-wire","reasoning":{}}`))
	require.NoError(t, err)
	require.Nil(t, extractOpenAIReasoningEffortFromRequest(req))
	require.Equal(t, 1.0, reasoningEffortBillingMultiplier(optionalStringValue(extractOpenAIReasoningEffortFromRequest(req)), map[string]float64{"high": 7}))
	remaining, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, `{"model":"fixture-wire","reasoning":{}}`, string(remaining))
}

// B3 follows real HTTP builders/readers into the two production usage ledgers.
func testReasoningBillingDispatchMatrix(t *testing.T) {
	routes := []struct {
		name, client, upstream string
		generic                bool
	}{
		{"native_responses", "responses", "responses", false},
		{"passthrough_responses", "responses", "responses", false},
		{"chat_to_responses", "chat", "responses", false},
		{"messages_to_responses", "messages", "responses", false},
		{"raw_chat", "chat", "chat", false},
		{"responses_to_raw_chat", "responses", "chat", false},
		{"messages_to_raw_chat", "messages", "chat", false},
		{"messages_to_native_anthropic", "messages", "anthropic", false},
		{"chat_to_native_anthropic", "chat", "anthropic", false},
		{"responses_to_native_anthropic", "responses", "anthropic", false},
		{"generic_chat_to_anthropic", "chat", "anthropic", true},
		{"generic_responses_to_anthropic", "responses", "anthropic", true},
		{"generic_messages_to_anthropic", "messages", "anthropic", true},
		{"generic_anthropic_passthrough", "messages", "anthropic", true},
		{"grok_responses", "responses", "responses", false},
		{"gemini_messages", "messages", "gemini", true},
		{"gemini_chat", "chat", "gemini", true},
		{"gemini_native", "gemini", "gemini", true},
	}
	for _, route := range routes {
		efforts := []string{"max", "xhigh"}
		if route.upstream == "gemini" {
			efforts = []string{"high", "budget_only"}
		}
		for _, effort := range efforts {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/partial=%t", route.name, effort, partial), func(t *testing.T) {
					model := "gpt-5.6-luna"
					if route.upstream == "anthropic" {
						model = "claude-fable-5-1"
					}
					if route.upstream == "gemini" {
						model = "gemini-3.8-flash"
					}
					if route.name == "grok_responses" {
						model = "grok-4.7"
					}
					bodyMap := map[string]any{"model": model, "stream": true}
					want := effort
					if route.name == "grok_responses" && effort == "max" {
						want = "high"
					}
					if effort == "budget_only" {
						want = ""
					}
					switch route.client {
					case "responses":
						bodyMap["input"] = "hello"
						bodyMap["reasoning"] = map[string]any{"effort": effort}
					case "chat":
						bodyMap["messages"] = []map[string]string{{"role": "user", "content": "hello"}}
						if want != "" {
							bodyMap["reasoning_effort"] = effort
						} else {
							bodyMap["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 4096}
						}
					case "messages":
						bodyMap["max_tokens"] = 16384
						bodyMap["messages"] = []map[string]string{{"role": "user", "content": "hello"}}
						if want != "" {
							bodyMap["output_config"] = map[string]string{"effort": effort}
						} else {
							bodyMap["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 4096}
						}
					case "gemini":
						bodyMap["contents"] = []any{map[string]any{"role": "user", "parts": []map[string]string{{"text": "hello"}}}}
						thinking := map[string]any{"thinkingLevel": effort}
						if want == "" {
							thinking = map[string]any{"thinkingBudget": 4096}
						}
						bodyMap["generationConfig"] = map[string]any{"thinkingConfig": thinking}
					}
					body, err := json.Marshal(bodyMap)
					require.NoError(t, err)
					wire := intB3BillingStream(route.upstream, partial)
					responseBody := io.NopCloser(strings.NewReader(wire))
					if partial {
						responseBody = &errTailReader{data: []byte(wire), err: errors.New("INT B3 upstream read failure")}
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: responseBody}}
					ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+route.client, bytes.NewReader(body)).WithContext(ctx)
					account := newOpenAIRejectedFieldTestAccount()
					openai := newOpenAIRejectedFieldTestService(upstream)
					openai.cfg = rawChatCompletionsTestConfig()
					generic := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, tlsFPProfileService: &TLSFingerprintProfileService{}}
					var result *OpenAIForwardResult
					var genericResult *ForwardResult
					switch route.name {
					case "native_responses":
						result, err = openai.Forward(ctx, c, account, body)
					case "passthrough_responses":
						account.Extra["openai_passthrough"] = true
						result, err = openai.Forward(ctx, c, account, body)
					case "chat_to_responses":
						result, err = openai.ForwardAsChatCompletions(ctx, c, account, body, "", "")
					case "messages_to_responses":
						result, err = openai.ForwardAsAnthropic(ctx, c, account, body, "", "")
					case "raw_chat":
						account = rawChatCompletionsTestAccount()
						account.Credentials["api_protocol"] = APIProtocolChatCompletions
						result, err = openai.forwardAsRawChatCompletions(ctx, c, account, body, "")
					case "responses_to_raw_chat":
						account = forceChatMessagesFallbackAccount()
						result, err = openai.forwardResponsesViaRawChatCompletions(ctx, c, account, body)
					case "messages_to_raw_chat":
						account = forceChatMessagesFallbackAccount()
						result, err = openai.forwardAnthropicViaRawChatCompletions(ctx, c, account, body, "")
					case "messages_to_native_anthropic":
						account = nativeAnthropicTestAccount()
						result, err = openai.forwardAnthropicViaNativeAnthropicEndpoint(ctx, c, account, body, "")
					case "chat_to_native_anthropic":
						account = nativeAnthropicTestAccount()
						result, err = openai.forwardChatCompletionsViaNativeAnthropic(ctx, c, account, body, "")
					case "responses_to_native_anthropic":
						account = nativeAnthropicTestAccount()
						result, err = openai.forwardResponsesViaNativeAnthropic(ctx, c, account, body, "")
					case "generic_chat_to_anthropic":
						account = newAnthropicAPIKeyAccountForTest()
						genericResult, err = generic.ForwardAsChatCompletions(ctx, c, account, body, nil)
					case "generic_responses_to_anthropic":
						account = newAnthropicAPIKeyAccountForTest()
						genericResult, err = generic.ForwardAsResponses(ctx, c, account, body, nil)
					case "generic_messages_to_anthropic":
						account = newAnthropicAPIKeyAccountForTest()
						var parsed *ParsedRequest
						parsed, err = ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
						require.NoError(t, err)
						genericResult, err = generic.Forward(ctx, c, account, parsed)
					case "generic_anthropic_passthrough":
						account = newAnthropicAPIKeyAccountForTest()
						genericResult, err = generic.forwardAnthropicAPIKeyPassthrough(ctx, c, account, body, model, model, true, time.Now())
					case "grok_responses":
						account = grokProtocolAPIKeyAccount(7104)
						result, err = openai.forwardGrokResponses(ctx, c, account, body, model, true, time.Now())
					case "gemini_messages", "gemini_chat", "gemini_native":
						account = geminiSignalTestAccount()
						gemini := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
						if route.name == "gemini_chat" {
							genericResult, err = gemini.ForwardAsChatCompletions(ctx, c, account, body)
						} else if route.name == "gemini_messages" {
							genericResult, err = gemini.Forward(ctx, c, account, body)
						} else {
							genericResult, err = gemini.ForwardNative(ctx, c, account, model, "streamGenerateContent", true, body)
						}
					}
					if partial {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
					require.Len(t, upstream.bodies, 1)
					field := map[string]string{"responses": "reasoning.effort", "chat": "reasoning_effort", "anthropic": "output_config.effort", "gemini": "generationConfig.thinkingConfig.thinkingLevel"}[route.upstream]
					require.Equal(t, want, gjson.GetBytes(upstream.lastBody, field).String(), "actual outbound body: %s", upstream.lastBody)
					if partial && route.upstream == "gemini" {
						// E31：已知限制，与基线相同。读错返回 nil result，不创建用量账单。
						require.Nil(t, genericResult)
						return
					}
					var actual *string
					var input, output int
					var resultModel string
					if route.generic {
						require.NotNil(t, genericResult)
						actual = genericResult.ReasoningEffort
						input = genericResult.Usage.InputTokens
						output = genericResult.Usage.OutputTokens
						resultModel = genericResult.Model
						genericResult.RequestedReasoningEffort = new("low")
					} else {
						require.NotNil(t, result)
						actual = result.ReasoningEffort
						input = result.Usage.InputTokens
						output = result.Usage.OutputTokens
						resultModel = result.Model
						result.RequestedReasoningEffort = new("low")
					}
					require.Equal(t, want, optionalStringValue(actual))
					if want == "" {
						require.Nil(t, actual)
					}
					require.Equal(t, 10, input)
					require.Equal(t, 5, output)
					repo := &openAIRecordUsageLogRepoStub{inserted: true}
					billing := NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, fmt.Sprintf(`{"%s":{"input_cost_per_token":0.001,"output_cost_per_token":0.002}}`, resultModel)))
					multipliers := map[string]float64{"max": 3, "xhigh": 2, "high": 7, "low": 11}
					group := &Group{ID: 4, Platform: account.Platform, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{resultModel}, ReasoningEffortMultipliers: multipliers}}}
					key := &APIKey{ID: 2, GroupID: &group.ID, Group: group}
					if route.generic {
						ledger := newGatewayRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
						ledger.cfg.Default.RateMultiplier = 1
						ledger.billingService = billing
						ledger.resolver = NewModelPricingResolver(nil, billing)
						require.NoError(t, ledger.RecordUsage(ctx, &RecordUsageInput{Result: genericResult, APIKey: key, User: &User{ID: 1}, Account: account}))
					} else {
						ledger := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
						ledger.cfg.Default.RateMultiplier = 1
						ledger.billingService = billing
						ledger.resolver = NewModelPricingResolver(nil, billing)
						require.NoError(t, ledger.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 1}, Account: account}))
					}
					require.NotNil(t, repo.lastLog)
					require.Equal(t, actual, repo.lastLog.ReasoningEffort)
					require.Equal(t, "low", optionalStringValue(repo.lastLog.RequestedReasoningEffort))
					multiplier := 1.0
					if v, ok := multipliers[want]; ok {
						multiplier = v
					}
					require.InDelta(t, 0.02*multiplier, repo.lastLog.TotalCost, 1e-12)
					require.Equal(t, actual, BillingUsageSnapshot(repo.lastLog).ReasoningEffort)
				})
			}
		}
	}
}

func intB3BillingStream(protocol string, partial bool) string {
	frame := func(s string) string { return "data: " + s + "\n\n" }
	switch protocol {
	case "responses":
		s := frame(`{"type":"response.created","response":{"id":"resp_b3","status":"in_progress","output":[]}}`) + frame(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`) + frame(`{"type":"response.usage","usage":{"input_tokens":10,"output_tokens":5}}`)
		if !partial {
			s += frame(`{"type":"response.completed","response":{"id":"resp_b3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":10,"output_tokens":5}}}`)
		}
		return s
	case "chat":
		s := frame(`{"id":"chat_b3","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`) + frame(`{"id":"chat_b3","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`)
		if !partial {
			s += frame(`{"id":"chat_b3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) + frame("[DONE]")
		}
		return s
	case "anthropic":
		s := "event: message_start\n" + frame(`{"type":"message_start","message":{"id":"msg_b3","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":5}}}`) + "event: content_block_start\n" + frame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) + "event: content_block_delta\n" + frame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
		if !partial {
			s += "event: content_block_stop\n" + frame(`{"type":"content_block_stop","index":0}`) + "event: message_delta\n" + frame(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`) + "event: message_stop\n" + frame(`{"type":"message_stop"}`)
		}
		return s
	default:
		finish := ""
		if !partial {
			finish = `,"finishReason":"STOP"`
		}
		return frame(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}` + finish + `}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`)
	}
}

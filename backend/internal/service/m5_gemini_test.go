//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestM5IntermediateThinkingBudget(t *testing.T) {
	for _, limit := range []int{512, 8192} {
		for _, stream := range []bool{false, true} {
			for _, alias := range []bool{false, true} {
				for _, model := range []string{"gemini-2.5-flash", "gemini-3.1-pro"} {
					for _, tc := range []struct{ name, field, effort string }{
						{"default", "", ""}, {"null", `,"reasoning_effort":null`, ""}, {"empty", `,"reasoning_effort":""`, ""},
						{"none", `,"reasoning_effort":"none"`, "none"}, {"low", `,"reasoning_effort":"low"`, "low"}, {"medium", `,"reasoning_effort":"medium"`, "medium"}, {"high", `,"reasoning_effort":"high"`, "high"},
						{"reasoning_string", `,"reasoning":"high"`, "high"},
					} {
						t.Run(fmt.Sprintf("%s/%d/%s/alias=%t/stream=%t", model, limit, tc.name, alias, stream), func(t *testing.T) {
							account := geminiSignalTestAccount()
							requested := model
							if alias {
								requested = "claude-sonnet-4-5"
								account.Credentials["model_mapping"] = map[string]any{requested: model, model: "wrong-second-mapping"}
							}
							body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":%d,"stream":%t,"messages":[{"role":"user","content":"hi"}]%s}`, requested, limit, stream, tc.field))
							wire, result, _ := m5ForwardChat(t, body, account)
							require.Equal(t, int64(limit), gjson.GetBytes(wire, "generationConfig.maxOutputTokens").Int())
							thinking := gjson.GetBytes(wire, "generationConfig.thinkingConfig")
							if tc.effort == "" {
								require.False(t, thinking.Exists())
								require.Nil(t, result.ReasoningEffort)
								return
							}
							require.Equal(t, tc.effort, optionalStringValue(result.RequestedReasoningEffort))
							if model == "gemini-2.5-flash" {
								require.Equal(t, int64(map[string]int{"none": 0, "low": 1024, "medium": 8192, "high": 24576}[tc.effort]), thinking.Get("thinkingBudget").Int())
								require.Nil(t, result.ReasoningEffort)
							} else {
								actual := tc.effort
								if actual == "none" {
									actual = "low"
								}
								require.Equal(t, actual, thinking.Get("thinkingLevel").String())
								require.Equal(t, actual, optionalStringValue(result.ReasoningEffort))
							}
							if tc.effort != "none" {
								require.False(t, thinking.Get("includeThoughts").Exists(), "the intermediate must not invent adaptive/output intent")
							}
						})
					}
				}
			}
		}
	}
	t.Run("real_claude_budget_and_copy", func(t *testing.T) {
		limit := 512
		req := &apicompat.ResponsesRequest{Model: "claude-sonnet-4-5", MaxOutputTokens: &limit, Reasoning: &apicompat.ResponsesReasoning{Effort: "high"}, Input: json.RawMessage(`"hi"`)}
		before, err := json.Marshal(req)
		require.NoError(t, err)
		out, err := apicompat.ResponsesToGeminiIntermediateRequest(req, "gemini-2.5-flash", apicompat.ConversionOptions{})
		require.NoError(t, err)
		require.Nil(t, out.Thinking)
		require.Equal(t, 512, out.MaxTokens)
		require.Equal(t, "high", out.OutputConfig.Effort)
		after, err := json.Marshal(req)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))
		_, err = apicompat.ResponsesToAnthropicRequest(req)
		require.Error(t, err, "Gemini tagging must not leak into a real Claude request")
		limit = 8192
		claude, err := apicompat.ResponsesToAnthropicRequest(req)
		require.NoError(t, err)
		require.Equal(t, 8191, claude.Thinking.BudgetTokens)
	})
}

func TestM5ThinkingConfig(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tt := range []struct {
			name, model, fields, want string
			actual                    string
			multiplier                float64
		}{
			{"default", "gemini-2.5-flash", ``, ``, "", 1},
			{"null", "gemini-2.5-flash", `,"output_config":{"effort":null}`, ``, "", 1},
			{"empty", "gemini-2.5-flash", `,"output_config":{"effort":""}`, ``, "", 1},
			{"none", "gemini-2.5-flash", `,"output_config":{"effort":"none"}`, `{"thinkingBudget":0,"includeThoughts":false}`, "", 1},
			{"low", "gemini-2.5-flash", `,"output_config":{"effort":"low"}`, `{"thinkingBudget":1024}`, "", 1},
			{"medium", "gemini-2.5-flash", `,"output_config":{"effort":"medium"}`, `{"thinkingBudget":8192}`, "", 1},
			{"high", "gemini-2.5-flash", `,"output_config":{"effort":"high"}`, `{"thinkingBudget":24576}`, "", 1},
			{"level_low", "gemini-3.1-pro", `,"output_config":{"effort":"low"}`, `{"thinkingLevel":"low"}`, "low", 1.2},
			{"level_high", "gemini-3.1-pro", `,"output_config":{"effort":"high"}`, `{"thinkingLevel":"high"}`, "high", 2},
			{"effort_before_adaptive", "gemini-3.1-pro", `,"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}`, `{"thinkingLevel":"low","includeThoughts":true}`, "low", 1.2},
			{"disabled_before_effort", "gemini-2.5-flash", `,"thinking":{"type":"disabled"},"output_config":{"effort":"high"}`, `{"thinkingBudget":0,"includeThoughts":false}`, "", 1},
			{"budget", "gemini-2.5-flash", `,"thinking":{"type":"enabled","budget_tokens":2048}`, `{"thinkingBudget":2048,"includeThoughts":true}`, "", 1},
			{"budget_on_level_model", "gemini-3.1-pro", `,"thinking":{"type":"enabled","budget_tokens":2048}`, `{"thinkingBudget":2048,"includeThoughts":true}`, "", 1},
			{"adaptive", "gemini-2.5-flash", `,"thinking":{"type":"adaptive"}`, `{"thinkingBudget":-1,"includeThoughts":true}`, "", 1},
			{"pro_cannot_disable", "gemini-2.5-pro", `,"output_config":{"effort":"none"}`, `{"thinkingBudget":128,"includeThoughts":false}`, "", 1},
			{"unknown", "gemini-9-flash", `,"output_config":{"effort":"high"}`, ``, "", 1},
			{"nearest", "gemini-3.1-pro", `,"output_config":{"effort":"max"}`, `{"thinkingLevel":"high"}`, "high", 2},
		} {
			t.Run(fmt.Sprintf("messages/%s/stream=%t", tt.name, stream), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":512,"stream":%t,"messages":[{"role":"user","content":"hi"}]%s}`, tt.model, stream, tt.fields))
				wire, result, notices := m5ForwardMessages(t, body)
				thinking := gjson.GetBytes(wire, "generationConfig.thinkingConfig")
				if tt.want == "" {
					require.False(t, thinking.Exists())
				} else {
					require.JSONEq(t, tt.want, thinking.Raw)
				}
				require.Equal(t, int64(512), gjson.GetBytes(wire, "generationConfig.maxOutputTokens").Int())
				require.Equal(t, tt.actual, optionalStringValue(result.ReasoningEffort))
				require.Equal(t, gjson.GetBytes(body, "output_config.effort").String(), optionalStringValue(result.RequestedReasoningEffort))
				if tt.name == "unknown" {
					require.Contains(t, notices, apicompat.ConversionNotice{Field: "output_config.effort", Action: "omit"})
				}
				billing := NewBillingService(&config.Config{}, nil)
				cost, err := billing.CalculateTokenCostForRequest(TokenCostRequest{
					Ctx: context.Background(), Model: tt.model,
					Group:  &Group{ID: 77, Platform: PlatformGemini, ModelPricing: []ChannelModelPricing{{Models: []string{tt.model}, BillingMode: BillingModeToken, InputPrice: testPtrFloat64(0.05), OutputPrice: testPtrFloat64(0.1), ReasoningEffortMultipliers: map[string]float64{"low": 1.2, "high": 2}}}},
					Tokens: UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens}, ReasoningEffort: optionalStringValue(result.ReasoningEffort),
					Resolver: NewModelPricingResolver(nil, billing), RateMultiplier: 1,
				})
				require.NoError(t, err)
				require.InDelta(t, tt.multiplier, cost.TotalCost, 1e-12)
			})
		}
	}

	t.Run("last_rebuilt_body", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`)
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
			{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"invalid thought_signature"}}`))},
			{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`))},
		}}
		account := geminiSignalTestAccount()
		account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-5": "gemini-3.1-pro"}
		svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
		c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/messages", body)
		result, err := svc.Forward(c.Request.Context(), c, account, body)
		require.NoError(t, err)
		require.Len(t, upstream.requestBodies, 2)
		require.Equal(t, "low", gjson.GetBytes(upstream.requestBodies[0], "generationConfig.thinkingConfig.thinkingLevel").String())
		require.Equal(t, "high", gjson.GetBytes(upstream.requestBodies[1], "generationConfig.thinkingConfig.thinkingLevel").String())
		require.Equal(t, "high", optionalStringValue(result.ReasoningEffort))
		require.Equal(t, "high", optionalStringValue(result.RequestedReasoningEffort))
	})
	t.Run("thought_output", func(t *testing.T) {
		raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"private thought","thought":true},{"text":"public answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"thoughtsTokenCount":3}}`)
		var response map[string]any
		require.NoError(t, json.Unmarshal(raw, &response))
		messages, _ := convertGeminiToClaudeMessage(response, "gemini-2.5-flash", raw, false)

		blocks := messages["content"].([]any)
		require.Equal(t, map[string]any{"type": "thinking", "thinking": "private thought", "signature": ""}, blocks[0])
		require.Equal(t, map[string]any{"type": "text", "text": "public answer"}, blocks[1])
		chat, _, err := geminiResponseToChatCompletions(response, "gemini-2.5-flash", raw, nil)
		require.NoError(t, err)
		require.Equal(t, "private thought", chat.Choices[0].Message.ReasoningContent)
		require.JSONEq(t, `"public answer"`, string(chat.Choices[0].Message.Content))
		for _, protocol := range []string{"messages", "chat"} {
			t.Run(protocol, func(t *testing.T) {
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+protocol, nil)
				svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\n"))}
				if protocol == "messages" {
					_, err = svc.handleStreamingResponse(c, resp, time.Now(), "gemini-2.5-flash")
				} else {
					_, err = svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-2.5-flash", false, true)
				}
				require.NoError(t, err)
				out := recorder.Body.String()
				if protocol == "messages" {
					require.Contains(t, out, `"thinking":"private thought"`)
					require.NotContains(t, out, `"text":"private thought"`)
					require.Contains(t, out, `"text":"public answer"`)
				} else {
					require.Contains(t, out, `"reasoning_content":"private thought"`)
					require.NotContains(t, out, `"content":"private thought"`)
					require.Contains(t, out, `"content":"public answer"`)
				}
			})
		}
	})
}

func TestM5EmptyToolEnum(t *testing.T) {
	tests := []struct {
		name, schema, want string
		omitted            []string
	}{
		{"empty", `{"type":"STRING","enum":[]}`, `{"type":"STRING"}`, []string{"schema.enum"}},
		{"empty_string", `{"enum":[""]}`, `{}`, []string{"schema.enum"}},
		{"mixed", `{"enum":["","ok"]}`, `{"enum":["ok"]}`, []string{"schema.enum"}},
		{"whitespace", `{"enum":[" "]}`, `{"enum":[" "]}`, nil},
		{"numbers", `{"enum":[1,2]}`, `{"enum":[1,2]}`, nil},
		{"nested", `{"properties":{"x":{"items":{"anyOf":[{"enum":[]},{"allOf":[{"enum":["","ok"]}]}]}}}}`, `{"properties":{"x":{"items":{"anyOf":[{},{"allOf":[{"enum":["ok"]}]}]}}}}`, []string{"schema.properties.x.items.anyOf[0].enum", "schema.properties.x.items.anyOf[1].allOf[0].enum"}},
		{"dependent", `{"dependentSchemas":{"x":{"properties":{"y":{"enum":[]}}}}}`, `{"dependentSchemas":{"x":{"properties":{"y":{}}}}}`, []string{"schema.dependentSchemas.x.properties.y.enum"}},
		{"not", `{"type":"STRING","not":{"enum":[]}}`, `{"type":"STRING"}`, []string{"schema.not"}},
		{"conditional", `{"if":{"enum":[]},"then":{"enum":["a"]},"else":{"enum":["b"]},"type":"STRING"}`, `{"type":"STRING"}`, []string{"schema.if", "schema.then", "schema.else"}},
		{"then", `{"if":{"type":"STRING"},"then":{"properties":{"x":{"enum":[]}}}}`, `{}`, []string{"schema.if", "schema.then"}},
		{"one_of", `{"oneOf":[{"enum":[]},{"enum":[""]}],"minLength":2}`, `{"minLength":2}`, []string{"schema.oneOf"}},
		{"contains", `{"contains":{"enum":[]},"minContains":1,"maxContains":2,"type":"ARRAY"}`, `{"type":"ARRAY"}`, []string{"schema.contains", "schema.minContains", "schema.maxContains"}},
		{"unaffected", `{"not":{"enum":["x"]},"if":{"enum":["a"]},"then":{"enum":["b"]},"else":{"enum":["c"]},"oneOf":[{"enum":["x"]}],"contains":{"enum":[" "]},"minContains":1}`, `{"not":{"enum":["x"]},"if":{"enum":["a"]},"then":{"enum":["b"]},"else":{"enum":["c"]},"oneOf":[{"enum":["x"]}],"contains":{"enum":[" "]},"minContains":1}`, nil},
		{"data", `{"default":{"enum":[]},"const":{"enum":[""]},"examples":[{"enum":[]}],"properties":{"enum":{"type":"STRING"}},"not":{"const":{"enum":[]}}}`, `{"default":{"enum":[]},"const":{"enum":[""]},"examples":[{"enum":[]}],"properties":{"enum":{"type":"STRING"}},"not":{"const":{"enum":[]}}}`, nil},
		{"containers", `{"patternProperties":{"x":{"enum":[]}},"$defs":{"x":{"enum":[]}},"definitions":{"x":{"enum":[]}},"items":[{"enum":[]}],"prefixItems":[{"enum":[]}],"additionalProperties":{"enum":[]},"propertyNames":{"enum":[]}}`, `{"patternProperties":{"x":{}},"$defs":{"x":{}},"definitions":{"x":{}},"items":[{}],"prefixItems":[{}],"additionalProperties":{},"propertyNames":{}}`, []string{"schema.patternProperties.x.enum", "schema.$defs.x.enum", "schema.definitions.x.enum", "schema.items[0].enum", "schema.prefixItems[0].enum", "schema.additionalProperties.enum", "schema.propertyNames.enum"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.schema), &schema))
			require.Equal(t, len(tt.omitted) > 0, hasGeminiEmptyEnum(schema))
			before, err := json.Marshal(schema)
			require.NoError(t, err)
			require.JSONEq(t, tt.schema, string(before), "detection must be read-only")
			require.Equal(t, tt.omitted, pruneGeminiEmptyEnums(schema, "schema"))
			after, err := json.Marshal(schema)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(after))
			body := []byte(`{"model":"gemini-2.5-flash","max_tokens":512,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","input_schema":` + tt.schema + `}]}`)
			wire, _, notices := m5ForwardMessages(t, body)
			var expected map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.want), &expected))
			cleanedExpected, err := json.Marshal(cleanToolSchema(expected))
			require.NoError(t, err)
			require.JSONEq(t, string(cleanedExpected), gjson.GetBytes(wire, "tools.0.functionDeclarations.0.parameters").Raw)
			var wantNotices []apicompat.ConversionNotice
			for _, field := range tt.omitted {
				if tt.name == "containers" && (strings.Contains(field, "patternProperties") || strings.Contains(field, "$defs") || strings.Contains(field, "definitions") || strings.Contains(field, "additionalProperties")) {
					continue // The established cleaner already removes these keywords.
				}
				wantNotices = append(wantNotices, apicompat.ConversionNotice{Field: strings.Replace(field, "schema", "tools[0].functionDeclarations[0].parameters", 1), Action: "omit"})
			}
			require.Equal(t, wantNotices, notices)
		})
	}
}

func TestM5StructuredOutput(t *testing.T) {
	t.Run("chat_final_request", func(t *testing.T) {
		for _, tc := range []struct{ name, model, format, mime, schemaNotice string }{
			{"default", "gemini-2.5-flash", "", "", ""},
			{"text", "gemini-2.5-flash", `{"type":"text"}`, "", ""},
			{"object", "gemini-2.5-flash", `{"type":"json_object"}`, "application/json", ""},
			{"schema", "gemini-2.5-flash", `{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","properties":{"x":{"type":"integer","minimum":9007199254740993,"maximum":18446744073709551615,"enum":[]}}}}}`, "application/json", ""},
			{"mime_only", "gemini-2.0-flash", `{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}`, "application/json", "output_config.format.schema"},
			{"unknown", "gemini-9-flash", `{"type":"json_object"}`, "", "output_config.format"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				body := []byte(`{"model":"format-alias","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}},{"type":"web_search"}]}`)
				if tc.format != "" {
					var err error
					body, err = sjson.SetRawBytes(body, "response_format", []byte(tc.format))
					require.NoError(t, err)
				}
				account := geminiSignalTestAccount()
				account.Credentials["model_mapping"] = map[string]any{"format-alias": tc.model}
				wire, _, notices := m5ForwardChat(t, body, account)
				require.Equal(t, tc.mime, gjson.GetBytes(wire, "generationConfig.responseMimeType").String())
				require.Contains(t, string(wire), `"inlineData":{"data":"aW1hZ2U=","mimeType":"image/png"}`)
				require.Equal(t, "f", gjson.GetBytes(wire, "tools.0.functionDeclarations.0.name").String())
				require.True(t, gjson.GetBytes(wire, "tools.1.google_search").Exists())
				if tc.name == "schema" {
					require.Equal(t, "9007199254740993", gjson.GetBytes(wire, "generationConfig.responseJsonSchema.properties.x.minimum").Raw)
					require.Equal(t, "18446744073709551615", gjson.GetBytes(wire, "generationConfig.responseJsonSchema.properties.x.maximum").Raw)
					require.Equal(t, "[]", gjson.GetBytes(wire, "generationConfig.responseJsonSchema.properties.x.enum").Raw)
				} else {
					require.False(t, gjson.GetBytes(wire, "generationConfig.responseJsonSchema").Exists())
				}
				if tc.schemaNotice != "" {
					require.Contains(t, notices, apicompat.ConversionNotice{Field: tc.schemaNotice, Action: "omit"})
				} else {
					require.Empty(t, notices)
				}
			})
		}
	})
	for _, tt := range []struct{ name, model, format, mime, schema, notice string }{
		{"default", "gemini-2.5-flash", ``, "", "", ""},
		{"text", "gemini-2.5-flash", `{"type":"text"}`, "", "", ""},
		{"object", "gemini-2.5-flash", `{"type":"json_object"}`, "application/json", "", ""},
		{"schema", "gemini-2.5-flash", `{"type":"json_schema","schema": { "type":"object", "properties":{"x":{"enum":[],"minimum":9007199254740993,"maximum":18446744073709551615}} }}`, "application/json", `{ "type":"object", "properties":{"x":{"enum":[],"minimum":9007199254740993,"maximum":18446744073709551615}} }`, ""},
		{"mime_only", "gemini-2.0-flash", `{"type":"json_schema","schema":{"type":"object"}}`, "application/json", "", "output_config.format.schema"},
		{"unknown", "gemini-9-flash", `{"type":"json_object"}`, "", "", "output_config.format"},
	} {
		t.Run("messages/"+tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":512,"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}],"tools":[{"name":"f","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`, tt.model))
			var err error
			if tt.format != "" {
				body, err = sjson.SetRawBytes(body, "output_config.format", []byte(tt.format))
				require.NoError(t, err)
			}
			wire, _, notices := m5ForwardMessages(t, body)
			require.Equal(t, tt.mime, gjson.GetBytes(wire, "generationConfig.responseMimeType").String())
			require.Equal(t, tt.schema, gjson.GetBytes(wire, "generationConfig.responseJsonSchema").Raw)
			require.Equal(t, "aW1hZ2U=", gjson.GetBytes(wire, "contents.0.parts.1.inlineData.data").String())
			require.Equal(t, "f", gjson.GetBytes(wire, "tools.0.functionDeclarations.0.name").String())
			require.True(t, gjson.GetBytes(wire, "tools.1.google_search").Exists())
			if tt.notice != "" {
				require.Equal(t, []apicompat.ConversionNotice{{Field: tt.notice, Action: "omit"}}, notices)
			} else {
				require.Empty(t, notices)
			}
		})
	}

	t.Run("google_search_preserves_schema", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"responseJsonSchema": { "type":"integer", "minimum":9007199254740993, "maximum":18446744073709551615 }},"tools":[{"googleSearch":{"timeRangeFilter": { "startTime":"2026-01-01T00:00:00Z" }}},{"functionDeclarations":[{"name":"lookup","parameters":{"type":"OBJECT"}}]}],"contents":[{"role":"user","parts":[{"text":"lookup"}]}]}`)
		original := string(body)
		got := normalizeGeminiRequestForAIStudio(body)
		require.Equal(t, original, string(body), "normalization must not mutate its input")
		require.Equal(t, gjson.GetBytes(body, "generationConfig").Raw, gjson.GetBytes(got, "generationConfig").Raw)
		require.Equal(t, gjson.GetBytes(body, "tools.0.googleSearch").Raw, gjson.GetBytes(got, "tools.0.google_search").Raw)
		require.False(t, gjson.GetBytes(got, "tools.0.googleSearch").Exists())
		require.Equal(t, gjson.GetBytes(body, "tools.1").Raw, gjson.GetBytes(got, "tools.1").Raw)
		require.Equal(t, gjson.GetBytes(body, "contents").Raw, gjson.GetBytes(got, "contents").Raw)
	})
}

func TestM5ToolChoice(t *testing.T) {
	t.Run("intermediate_conversion_error", func(t *testing.T) {
		body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{broken"}}]}]}`)
		upstream := &queuedHTTPUpstreamStub{}
		svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
		c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)
		_, err := svc.ForwardAsChatCompletions(c.Request.Context(), c, geminiSignalTestAccount(), body)
		var conversion *apicompat.UnsupportedConversionError
		require.ErrorAs(t, err, &conversion)
		require.Equal(t, "messages[0].tool_calls[0].function.arguments", conversion.Field)
		require.Zero(t, upstream.callCount)
		require.Empty(t, recorder.Body.String(), "preserve the field for the handler before writing a generic response")
	})
	for _, tt := range []struct {
		name, choice, mode, field string
		builtin                   bool
		notices                   []apicompat.ConversionNotice
	}{
		{name: "default", builtin: true},
		{name: "auto", choice: `{"type":"auto"}`, mode: "AUTO", builtin: true},
		{name: "none", choice: `{"type":"none"}`, mode: "NONE"},
		{name: "any", choice: `{"type":"any"}`, mode: "ANY", builtin: true},
		{name: "named", choice: `{"type":"tool","name":"f"}`, mode: "ANY", builtin: true},
		{name: "builtin", choice: `{"type":"tool","name":"web_search"}`, builtin: true, notices: []apicompat.ConversionNotice{{Field: "tool_choice", Action: "map"}}},
		{name: "serial", choice: `{"type":"auto","disable_parallel_tool_use":true}`, mode: "AUTO", builtin: true, notices: []apicompat.ConversionNotice{{Field: "tool_choice.disable_parallel_tool_use", Action: "omit"}}},
		{name: "parallel", choice: `{"type":"auto","disable_parallel_tool_use":false}`, mode: "AUTO", builtin: true},
		{name: "undeclared", choice: `{"type":"tool","name":"unknown"}`, field: "tool_choice.name"},
		{name: "bad_name", choice: `{"type":"tool","name":4}`, field: "tool_choice.name"},
		{name: "bad_choice", choice: `"none"`, field: "tool_choice"},
		{name: "bad_type", choice: `{"type":"unknown"}`, field: "tool_choice.type"},
		{name: "bad_parallel", choice: `{"type":"auto","disable_parallel_tool_use":"true"}`, field: "tool_choice.disable_parallel_tool_use"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"old","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"history"}]}],"tools":[{"name":"f","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`)
			if tt.choice != "" {
				var err error
				body, err = sjson.SetRawBytes(body, "tool_choice", []byte(tt.choice))
				require.NoError(t, err)
			}
			if tt.field != "" {
				upstream := &queuedHTTPUpstreamStub{}
				svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/messages", body)
				_, err := svc.Forward(c.Request.Context(), c, geminiSignalTestAccount(), body)
				var conversion *apicompat.UnsupportedConversionError
				require.ErrorAs(t, err, &conversion)
				require.Equal(t, tt.field, conversion.Field)
				require.Zero(t, upstream.callCount)
				require.Empty(t, recorder.Body.String(), "handler must receive the precise conversion error")
				return
			}
			wire, _, notices := m5ForwardMessages(t, body)
			require.Equal(t, tt.mode, gjson.GetBytes(wire, "toolConfig.functionCallingConfig.mode").String())
			require.Equal(t, tt.builtin, gjson.GetBytes(wire, "tools.1.google_search").Exists())
			if tt.name == "named" {
				require.Equal(t, `["f"]`, gjson.GetBytes(wire, "toolConfig.functionCallingConfig.allowedFunctionNames").Raw)
			}
			require.Equal(t, "history", gjson.GetBytes(wire, "contents.1.parts.0.functionResponse.response.content").String())
			require.Equal(t, tt.notices, notices)
		})
	}
}

func m5ForwardMessages(t *testing.T, body []byte, accounts ...*Account) ([]byte, *ForwardResult, []apicompat.ConversionNotice) {
	t.Helper()
	response := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
	contentType := "application/json"
	if gjson.GetBytes(body, "stream").Bool() {
		response = "data: " + response + "\n\n"
		contentType = "text/event-stream"
	}
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}}}
	svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/messages", body)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	c.Request = c.Request.WithContext(ctx)
	account := geminiSignalTestAccount()
	if len(accounts) > 0 {
		account = accounts[0]
	}
	result, err := svc.Forward(ctx, c, account, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, upstream.requestBodies, 1)
	return upstream.requestBodies[0], result, resilience.SnapshotConversionNotices(ctx)
}

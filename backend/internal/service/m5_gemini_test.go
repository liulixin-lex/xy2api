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
	t.Run("history_thinking_round_trip", func(t *testing.T) {
		turn1 := []byte(`{"model":"gemini-2.5-flash","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"q1"}]}`)
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"**Planning** I will look at X first.","thought":true},{"text":"a1"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":11}}`))}}}
		svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
		c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/messages", turn1)
		_, err := svc.Forward(c.Request.Context(), c, geminiSignalTestAccount(), turn1)
		require.NoError(t, err)
		require.JSONEq(t, `{"includeThoughts":true,"thinkingBudget":2048}`, gjson.GetBytes(upstream.requestBodies[0], "generationConfig.thinkingConfig").Raw)
		assistant := gjson.Get(recorder.Body.String(), "content").Raw
		require.Equal(t, "thinking", gjson.Get(assistant, "0.type").String())

		turn2, err := sjson.SetRawBytes(turn1, "messages", []byte(`[{"role":"user","content":"q1"},{"role":"assistant","content":`+assistant+`},{"role":"user","content":"q2"}]`))
		require.NoError(t, err)
		wire, _, _ := m5ForwardMessages(t, turn2)
		require.JSONEq(t, `[{"role":"user","parts":[{"text":"q1"}]},{"role":"model","parts":[{"text":"a1"}]},{"role":"user","parts":[{"text":"q2"}]}]`, gjson.GetBytes(wire, "contents").Raw)

		// A turn holding only thinking keeps its existing conversion, so no turn is removed.
		onlyThinking, err := sjson.SetRawBytes(turn1, "messages", []byte(`[{"role":"user","content":"q1"},{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":""}]},{"role":"user","content":"q2"}]`))
		require.NoError(t, err)
		wire, _, _ = m5ForwardMessages(t, onlyThinking)
		require.Len(t, gjson.GetBytes(wire, "contents").Array(), 3)
		require.Equal(t, "model", gjson.GetBytes(wire, "contents.1.role").String())
	})
	t.Run("thought_output", func(t *testing.T) {
		raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"private thought","thought":true},{"text":"public answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"thoughtsTokenCount":3}}`)
		var response map[string]any
		require.NoError(t, json.Unmarshal(raw, &response))
		messages, _, conversionErr := convertGeminiToClaudeMessage(response, "gemini-2.5-flash", raw, false)
		require.NoError(t, conversionErr)
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
			for _, withTools := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/tools=%t", tc.name, withTools), func(t *testing.T) {
					body := []byte(`{"model":"format-alias","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}},{"type":"web_search"}]}`)
					var err error
					if !withTools {
						body, err = sjson.DeleteBytes(body, "tools")
						require.NoError(t, err)
					}
					if tc.format != "" {
						body, err = sjson.SetRawBytes(body, "response_format", []byte(tc.format))
						require.NoError(t, err)
					}
					account := geminiSignalTestAccount()
					account.Credentials["model_mapping"] = map[string]any{"format-alias": tc.model}
					wire, _, notices := m5ForwardChat(t, body, account)
					require.Contains(t, string(wire), `"inlineData":{"data":"aW1hZ2U=","mimeType":"image/png"}`)
					if withTools {
						// Gemini 2.x rejects tools with a JSON MIME type: keep the baseline request.
						require.Equal(t, "f", gjson.GetBytes(wire, "tools.0.functionDeclarations.0.name").String())
						require.True(t, gjson.GetBytes(wire, "tools.1.google_search").Exists())
						require.False(t, gjson.GetBytes(wire, "generationConfig.responseMimeType").Exists())
						require.False(t, gjson.GetBytes(wire, "generationConfig.responseJsonSchema").Exists())
						if tc.mime != "" || tc.schemaNotice != "" {
							require.Equal(t, []apicompat.ConversionNotice{{Field: "output_config.format", Action: "omit"}}, notices)
						} else {
							require.Empty(t, notices)
						}
						return
					}
					require.Equal(t, tc.mime, gjson.GetBytes(wire, "generationConfig.responseMimeType").String())
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
		for _, withTools := range []bool{false, true} {
			t.Run(fmt.Sprintf("messages/%s/tools=%t", tt.name, withTools), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":512,"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}],"tools":[{"name":"f","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`, tt.model))
				var err error
				if !withTools {
					body, err = sjson.DeleteBytes(body, "tools")
					require.NoError(t, err)
				}
				if tt.format != "" {
					body, err = sjson.SetRawBytes(body, "output_config.format", []byte(tt.format))
					require.NoError(t, err)
				}
				wire, _, notices := m5ForwardMessages(t, body)
				require.Equal(t, "aW1hZ2U=", gjson.GetBytes(wire, "contents.0.parts.1.inlineData.data").String())
				mime, schema, notice := tt.mime, tt.schema, tt.notice
				if withTools {
					require.Equal(t, "f", gjson.GetBytes(wire, "tools.0.functionDeclarations.0.name").String())
					require.True(t, gjson.GetBytes(wire, "tools.1.google_search").Exists())
					if mime != "" || notice != "" {
						mime, schema, notice = "", "", "output_config.format"
					}
				}
				require.Equal(t, mime, gjson.GetBytes(wire, "generationConfig.responseMimeType").String())
				require.Equal(t, schema, gjson.GetBytes(wire, "generationConfig.responseJsonSchema").Raw)
				if notice != "" {
					require.Equal(t, []apicompat.ConversionNotice{{Field: notice, Action: "omit"}}, notices)
				} else {
					require.Empty(t, notices)
				}
			})
		}
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

func TestM5CacheOnlyUsage(t *testing.T) {
	for _, tt := range []struct {
		name, metadata                      string
		input, cached, output               int
		promptPresent, incomplete, conflict bool
	}{
		{"cache_only", `{"cachedContentTokenCount":7}`, 0, 7, 0, false, true, false},
		{"cache_exceeds_prompt", `{"promptTokenCount":3,"cachedContentTokenCount":7,"candidatesTokenCount":2}`, 0, 7, 2, true, false, true},
		{"complete", `{"promptTokenCount":10,"cachedContentTokenCount":7,"candidatesTokenCount":2,"thoughtsTokenCount":3}`, 3, 7, 5, true, false, false},
		{"zeros", `{"promptTokenCount":0,"cachedContentTokenCount":0,"candidatesTokenCount":0,"thoughtsTokenCount":0}`, 0, 0, 0, true, false, false},
		{"null", `null`, 0, 0, 0, false, true, false},
		{"empty", `{}`, 0, 0, 0, false, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":` + tt.metadata + `}`)
			u := extractGeminiUsage(raw)
			require.NotNil(t, u)
			require.Equal(t, tt.input, u.InputTokens)
			require.Equal(t, tt.cached, u.CacheReadInputTokens)
			require.Equal(t, tt.output, u.OutputTokens)
			require.Equal(t, tt.promptPresent, u.InputTokensPresent)
			require.Equal(t, tt.incomplete, u.UsageIncomplete)
			require.Equal(t, tt.conflict, u.UsageInconsistent)
			if tt.promptPresent {
				require.NotNil(t, u.RawPromptTokens)
				require.Equal(t, int(gjson.Get(tt.metadata, "promptTokenCount").Int()), *u.RawPromptTokens)
			} else {
				require.Nil(t, u.RawPromptTokens)
			}
			var response map[string]any
			require.NoError(t, json.Unmarshal(raw, &response))
			messages, got, conversionErr := convertGeminiToClaudeMessage(response, "gemini-2.5-flash", raw, false)
			require.NoError(t, conversionErr)
			require.Equal(t, u, got)
			usage := messages["usage"].(map[string]any)
			require.Equal(t, tt.input, usage["input_tokens"])
			require.Equal(t, tt.output, usage["output_tokens"])
			require.NotContains(t, usage, "cache_read_input_tokens", "E8: cache reads stay internal")
			chat, got, err := geminiResponseToChatCompletions(response, "gemini-2.5-flash", raw, nil)
			require.NoError(t, err)
			require.Equal(t, u, got)
			require.Equal(t, tt.input, chat.Usage.PromptTokens)
			require.Equal(t, tt.output, chat.Usage.CompletionTokens)
		})
	}
	t.Run("missing_and_blocked", func(t *testing.T) {
		for _, raw := range []string{`{"candidates":[]}`, `{"promptFeedback":{"blockReason":"SAFETY"}}`} {
			require.Nil(t, extractGeminiUsage([]byte(raw)))
			var response map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &response))
			message, usage, conversionErr := convertGeminiToClaudeMessage(response, "gemini-2.5-flash", []byte(raw), false)
			require.NoError(t, conversionErr)
			require.False(t, usage.InputTokensPresent)
			encoded, err := json.Marshal(message["usage"])
			require.NoError(t, err)
			require.Equal(t, `{"input_tokens":0,"output_tokens":0}`, string(encoded), "E8 preserves both counts")
		}
	})
	for _, correction := range []bool{false, true} {
		t.Run(fmt.Sprintf("progressive/correction=%t", correction), func(t *testing.T) {
			frames := []string{
				`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":7,"candidatesTokenCount":2,"thoughtsTokenCount":3}}`,
				`{"usageMetadata":{"candidatesTokenCount":4}}`,
			}
			wantInput, wantCache, wantOutput := 3, 7, 7
			if correction {
				frames = append(frames, `{"usageMetadata":{"promptTokenCount":3,"cachedContentTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":0}}`)
				wantInput, wantCache, wantOutput = 2, 1, 1
			}
			frames = append(frames, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{}}`)
			sse := "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
			_, collected, _, err := collectGeminiSSEObserved(strings.NewReader(sse), false, nil)
			require.NoError(t, err)
			require.Equal(t, wantInput, collected.InputTokens)
			require.Equal(t, wantCache, collected.CacheReadInputTokens)
			require.Equal(t, wantOutput, collected.OutputTokens)
			for _, protocol := range []string{"messages", "chat"} {
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+protocol, nil)
				svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}
				var result *geminiStreamResult
				if protocol == "messages" {
					result, err = svc.handleStreamingResponse(c, resp, time.Now(), "gemini-2.5-flash")
				} else {
					result, err = svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-2.5-flash", false, true)
				}
				require.NoError(t, err)
				require.Equal(t, collected, result.usage)
				if protocol == "messages" {
					require.Contains(t, recorder.Body.String(), fmt.Sprintf(`"usage":{"input_tokens":%d,"output_tokens":%d}`, wantInput, wantOutput))
					require.NotContains(t, recorder.Body.String(), `"cache_read_input_tokens"`)
				} else {
					require.Contains(t, recorder.Body.String(), fmt.Sprintf(`"prompt_tokens":%d`, wantInput+wantCache))
					require.Contains(t, recorder.Body.String(), fmt.Sprintf(`"completion_tokens":%d`, wantOutput))
				}
			}
		})
	}
}

// TestM5ClientUsageBytes pins the client usage to the bytes sent before M5 (E8)
// for a complete usageMetadata with an implicit cache hit.
func TestM5ClientUsageBytes(t *testing.T) {
	payload := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":7,"candidatesTokenCount":5}}`
	for _, tt := range []struct {
		protocol string
		stream   bool
		want     string
	}{
		{"messages", false, `{"input_tokens":3,"output_tokens":5}`},
		{"messages", true, `{"input_tokens":3,"output_tokens":5}`},
		{"chat", false, `{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}`},
		{"chat", true, `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":7}}`},
	} {
		t.Run(fmt.Sprintf("%s/stream=%t", tt.protocol, tt.stream), func(t *testing.T) {
			response, contentType := payload, "application/json"
			if tt.stream {
				response, contentType = "data: "+payload+"\n\n", "text/event-stream"
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			var body []byte
			var err error
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+tt.protocol, nil)
			if tt.protocol == "chat" {
				body = []byte(fmt.Sprintf(`{"model":"gemini-2.5-flash","stream":%t,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`, tt.stream))
				_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, geminiSignalTestAccount(), body)
			} else {
				body = []byte(fmt.Sprintf(`{"model":"gemini-2.5-flash","stream":%t,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, tt.stream))
				_, err = svc.Forward(c.Request.Context(), c, geminiSignalTestAccount(), body)
			}
			require.NoError(t, err)
			last := ""
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				line = strings.TrimPrefix(line, "data: ")
				if usage := gjson.Get(line, "usage"); usage.Exists() && gjson.Get(line, "type").String() != "message_start" {
					last = usage.Raw
				}
			}
			require.Equal(t, tt.want, last)
		})
	}
}

func m5HistoryCall(id, args string) apicompat.ChatMessage {
	return apicompat.ChatMessage{Role: "assistant", ToolCalls: []apicompat.ChatToolCall{{ID: id, Type: "function", Function: apicompat.ChatFunctionCall{Name: "lookup", Arguments: args}}}}
}
func m5HistoryResult(id string, raw json.RawMessage) apicompat.ChatMessage {
	return apicompat.ChatMessage{Role: "tool", ToolCallID: id, Content: raw}
}
func TestM5ToolHistoryRounds(t *testing.T) {
	t.Run("chat_occurrences", func(t *testing.T) {
		for _, tc := range []struct {
			name, messages string
			want           []string
		}{
			{"rounds", `[{"role":"assistant","tool_calls":[{"id":"same","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"same","content":"first"},{"role":"assistant","tool_calls":[{"id":"same","type":"function","function":{"name":"g","arguments":"{}"}}]},{"role":"tool","tool_call_id":"same","content":"second"}]`, []string{"f:first", "g:second"}},
			{"interleaved", `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"b","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"A"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"c","arguments":"{}"}}]},{"role":"tool","tool_call_id":"b","content":"B"},{"role":"tool","tool_call_id":"c","content":"C"}]`, []string{"a:A", "b:B", "c:C"}},
			{"legacy_fifo", `[{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"assistant","function_call":{"name":"f","arguments":"{}"}},{"role":"function","name":"f","content":"one"},{"role":"function","name":"f","content":"two"}]`, []string{"f:one", "f:two"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				body := []byte(`{"model":"gemini-2.5-flash","messages":` + tc.messages + `}`)
				wire, _, _ := m5ForwardChat(t, body)
				var got []string
				for _, content := range gjson.GetBytes(wire, "contents").Array() {
					for _, part := range content.Get("parts").Array() {
						if result := part.Get("functionResponse"); result.Exists() {
							got = append(got, result.Get("name").String()+":"+result.Get("response.content").String())
						}
					}
				}
				require.Equal(t, tc.want, got)
			})
		}
	})
	t.Run("messages_slots", func(t *testing.T) {
		for _, tt := range []struct{ name, messages, want string }{
			{"reused_id", `[{"role":"assistant","content":[{"type":"tool_use","id":"same","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"same","content":"first"}]},{"role":"assistant","content":[{"type":"tool_use","id":"same","name":"g","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"same","content":"second"}]}]`, `f:first,g:second`},
			{"reversed_results", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"a","input":{}},{"type":"tool_use","id":"b","name":"b","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"B"},{"type":"tool_result","tool_use_id":"a","content":"A"}]}]`, `a:A,b:B`},
			{"interleaved", `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"a","input":{}},{"type":"tool_use","id":"b","name":"b","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"c","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"C"},{"type":"tool_result","tool_use_id":"b","content":"B"}]}]`, `a:A,b:B,c:C`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				body := []byte(`{"model":"gemini-2.5-flash","max_tokens":512,"messages":` + tt.messages + `}`)
				wire, _, _ := m5ForwardMessages(t, body)
				var got []string
				for _, content := range gjson.GetBytes(wire, "contents").Array() {
					for _, part := range content.Get("parts").Array() {
						if result := part.Get("functionResponse"); result.Exists() {
							got = append(got, result.Get("name").String()+":"+result.Get("response.content").String())
						}
					}
				}
				require.Equal(t, tt.want, strings.Join(got, ","))
			})
		}
	})

	t.Run("messages_projection", func(t *testing.T) {
		body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"first","input":{},"signature":"signed-first"},{"type":"tool_use","id":"b","name":"second","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"two"},{"type":"text","text":"between"},{"type":"tool_result","tool_use_id":"a","content":"one"}]}]}`)
		wire, _, _ := m5ForwardMessages(t, body)
		require.Equal(t, "signed-first", gjson.GetBytes(wire, "contents.0.parts.0.thoughtSignature").String())
		require.Equal(t, "first", gjson.GetBytes(wire, "contents.1.parts.0.functionResponse.name").String())
		require.Equal(t, "one", gjson.GetBytes(wire, "contents.1.parts.0.functionResponse.response.content").String())
		require.Equal(t, "between", gjson.GetBytes(wire, "contents.1.parts.1.text").String())
		require.Equal(t, "second", gjson.GetBytes(wire, "contents.1.parts.2.functionResponse.name").String())
		require.Equal(t, "two", gjson.GetBytes(wire, "contents.1.parts.2.functionResponse.response.content").String())
	})
	t.Run("messages_interleaved", func(t *testing.T) {
		body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"A","input":{}},{"type":"tool_use","id":"b","name":"B","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"a"}]},{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"C","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"c"},{"type":"tool_result","tool_use_id":"b","content":"b"}]}]}`)
		wire, _, _ := m5ForwardMessages(t, body)
		require.Equal(t, "A", gjson.GetBytes(wire, "contents.1.parts.0.functionResponse.name").String())
		require.Equal(t, "B", gjson.GetBytes(wire, "contents.3.parts.0.functionResponse.name").String())
		require.Equal(t, "C", gjson.GetBytes(wire, "contents.3.parts.1.functionResponse.name").String())
	})
	t.Run("messages_reused_id", func(t *testing.T) {
		body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"same","name":"first","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"same","content":"one"}]},{"role":"assistant","content":[{"type":"tool_use","id":"same","name":"second","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"same","content":"two"}]}]}`)
		wire, _, _ := m5ForwardMessages(t, body)
		require.Equal(t, "first", gjson.GetBytes(wire, "contents.1.parts.0.functionResponse.name").String())
		require.Equal(t, "one", gjson.GetBytes(wire, "contents.1.parts.0.functionResponse.response.content").String())
		require.Equal(t, "second", gjson.GetBytes(wire, "contents.3.parts.0.functionResponse.name").String())
		require.Equal(t, "two", gjson.GetBytes(wire, "contents.3.parts.0.functionResponse.response.content").String())
	})

	wantInputs := map[string]string{
		"id_length_41":                "[{\"type\":\"function_call\",\"call_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"output\":\"ok\"}]",
		"id_length_65":                "[{\"type\":\"function_call\",\"call_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"output\":\"ok\"}]",
		"id_length_129":               "[{\"type\":\"function_call\",\"call_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"output\":\"ok\"}]",
		"id_no_prefix":                "[{\"type\":\"function_call\",\"call_id\":\"xyz789\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"xyz789\",\"output\":\"ok\"}]",
		"id_special":                  "[{\"type\":\"function_call\",\"call_id\":\"abc:/+?= 空格#[]\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"abc:/+?= 空格#[]\",\"output\":\"ok\"}]",
		"content_string":              "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"ok\"}]",
		"content_text_array":          "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"onetwo\"}]",
		"content_image_array":         "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":[{\"type\":\"input_image\",\"image_url\":\"https://example.invalid/image.png\"}]}]",
		"content_empty_string":        "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"(empty)\"}]",
		"content_missing":             "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"(empty)\"}]",
		"content_null":                "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"(empty)\"}]",
		"content_empty_array":         "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"(empty)\"}]",
		"parallel_reverse_results":    "[{\"type\":\"function_call\",\"call_id\":\"a\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call\",\"call_id\":\"b\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"b\",\"output\":\"B\"},{\"type\":\"function_call_output\",\"call_id\":\"a\",\"output\":\"A\"}]",
		"intervening_user":            "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"message\",\"role\":\"user\",\"content\":\"intervening\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"ok\"}]",
		"intervening_system":          "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"message\",\"role\":\"system\",\"content\":\"intervening\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"ok\"}]",
		"id_reused_across_rounds":     "[{\"type\":\"function_call\",\"call_id\":\"same\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"same\",\"output\":\"first\"},{\"type\":\"message\",\"role\":\"user\",\"content\":\"next\"},{\"type\":\"function_call\",\"call_id\":\"same\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"same\",\"output\":\"second\"}]",
		"arguments_empty":             "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"ok\"}]",
		"arguments_invalid":           "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{broken\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"ok\"}]",
		"legacy_function_pair":        "[{\"type\":\"function_call\",\"call_id\":\"call_legacy_1\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"call_legacy_1\",\"output\":\"ok\"}]",
		"legacy_same_name_fifo":       "[{\"type\":\"function_call\",\"call_id\":\"call_legacy_1\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call\",\"call_id\":\"call_legacy_2\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"call_legacy_1\",\"output\":\"one\"},{\"type\":\"function_call_output\",\"call_id\":\"call_legacy_2\",\"output\":\"two\"}]",
		"assistant_content_and_calls": "[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"I will call lookup\"}]},{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"ok\"}]",
		"call_without_result":         "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"}]",
		"result_without_call":         "[{\"type\":\"function_call_output\",\"call_id\":\"c\",\"output\":\"ok\"}]",
		"result_missing_id":           "[{\"type\":\"function_call_output\",\"output\":\"ok\"}]",
		"result_wrong_id":             "[{\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"lookup\",\"arguments\":\"{}\"},{\"type\":\"function_call_output\",\"call_id\":\"other\",\"output\":\"ok\"}]",
		"legacy_result_without_call":  "[{\"type\":\"function_call_output\",\"call_id\":\"lookup\",\"output\":\"ok\"}]",
	}

	type fixture struct {
		name                   string
		msgs                   []apicompat.ChatMessage
		wantCalls, wantOutputs int
		wantPair, wantError    bool
	}
	var cases []fixture
	pair := func(name, id string, raw json.RawMessage) {
		cases = append(cases, fixture{name, []apicompat.ChatMessage{m5HistoryCall(id, `{}`), m5HistoryResult(id, raw)}, 1, 1, true, false})
	}
	for _, n := range []int{41, 65, 129} {
		pair(fmt.Sprintf("id_length_%d", n), strings.Repeat("a", n), json.RawMessage(`"ok"`))
	}
	pair("id_no_prefix", "xyz789", json.RawMessage(`"ok"`))
	pair("id_special", "abc:/+?= 空格#[]", json.RawMessage(`"ok"`))
	pair("content_string", "c", json.RawMessage(`"ok"`))
	pair("content_text_array", "c", json.RawMessage(`[{"type":"text","text":"one"},{"type":"text","text":"two"}]`))
	pair("content_image_array", "c", json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]`))
	pair("content_empty_string", "c", json.RawMessage(`""`))
	pair("content_missing", "c", nil)
	pair("content_null", "c", json.RawMessage(`null`))
	pair("content_empty_array", "c", json.RawMessage(`[]`))
	cases = append(cases, fixture{"content_unknown_array", []apicompat.ChatMessage{m5HistoryCall("c", `{}`), m5HistoryResult("c", json.RawMessage(`[{"type":"mystery","text":"ok"}]`))}, 0, 0, false, true})
	parallel := m5HistoryCall("a", `{}`)
	parallel.ToolCalls = append(parallel.ToolCalls, m5HistoryCall("b", `{}`).ToolCalls[0])
	cases = append(cases, fixture{"parallel_reverse_results", []apicompat.ChatMessage{parallel, m5HistoryResult("b", json.RawMessage(`"B"`)), m5HistoryResult("a", json.RawMessage(`"A"`))}, 2, 2, true, false})
	for _, role := range []string{"user", "system"} {
		cases = append(cases, fixture{"intervening_" + role, []apicompat.ChatMessage{m5HistoryCall("c", `{}`), {Role: role, Content: json.RawMessage(`"intervening"`)}, m5HistoryResult("c", json.RawMessage(`"ok"`))}, 1, 1, true, false})
	}
	cases = append(cases, fixture{"id_reused_across_rounds", []apicompat.ChatMessage{m5HistoryCall("same", `{}`), m5HistoryResult("same", json.RawMessage(`"first"`)), {Role: "user", Content: json.RawMessage(`"next"`)}, m5HistoryCall("same", `{}`), m5HistoryResult("same", json.RawMessage(`"second"`))}, 2, 2, true, false})
	for _, arg := range []string{"", "{broken"} {
		name := "arguments_invalid"
		if arg == "" {
			name = "arguments_empty"
		}
		cases = append(cases, fixture{name, []apicompat.ChatMessage{m5HistoryCall("c", arg), m5HistoryResult("c", json.RawMessage(`"ok"`))}, 1, 1, true, false})
	}
	legacy := apicompat.ChatMessage{Role: "assistant", FunctionCall: &apicompat.ChatFunctionCall{Name: "lookup", Arguments: `{}`}}
	cases = append(cases, fixture{"legacy_function_pair", []apicompat.ChatMessage{legacy, {Role: "function", Name: "lookup", Content: json.RawMessage(`"ok"`)}}, 1, 1, true, false})
	cases = append(cases, fixture{"legacy_same_name_fifo", []apicompat.ChatMessage{legacy, legacy, {Role: "function", Name: "lookup", Content: json.RawMessage(`"one"`)}, {Role: "function", Name: "lookup", Content: json.RawMessage(`"two"`)}}, 2, 2, true, false})
	both := m5HistoryCall("c", `{}`)
	both.Content = json.RawMessage(`"I will call lookup"`)
	cases = append(cases, fixture{"assistant_content_and_calls", []apicompat.ChatMessage{both, m5HistoryResult("c", json.RawMessage(`"ok"`))}, 1, 1, true, false})
	cases = append(cases, fixture{"call_without_result", []apicompat.ChatMessage{m5HistoryCall("c", `{}`)}, 1, 0, false, false}, fixture{"result_without_call", []apicompat.ChatMessage{m5HistoryResult("c", json.RawMessage(`"ok"`))}, 0, 1, false, false}, fixture{"result_missing_id", []apicompat.ChatMessage{m5HistoryResult("", json.RawMessage(`"ok"`))}, 0, 1, false, false}, fixture{"result_wrong_id", []apicompat.ChatMessage{m5HistoryCall("c", `{}`), m5HistoryResult("other", json.RawMessage(`"ok"`))}, 1, 1, false, false}, fixture{"legacy_result_without_call", []apicompat.ChatMessage{{Role: "function", Name: "lookup", Content: json.RawMessage(`"ok"`)}}, 0, 1, false, false})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := apicompat.ChatCompletionsToResponses(&apicompat.ChatCompletionsRequest{Model: "probe", Messages: tc.msgs})
			if tc.name == "arguments_invalid" {
				require.EqualError(t, err, "unsupported protocol conversion at messages[0].tool_calls[0].function.arguments: expected complete JSON arguments")
				return
			}
			if err != nil {
				require.EqualError(t, err, "unsupported protocol conversion at content.type: unsupported Chat content part")
				if !tc.wantError {
					t.Fatal(err)
				}
				return
			}
			if tc.wantError {
				t.Fatal("wanted local error")
			}
			require.Equal(t, wantInputs[tc.name], string(out.Input))
			var items []map[string]any
			if err = json.Unmarshal(out.Input, &items); err != nil {
				t.Fatal(err)
			}
			pending := map[string]int{}
			nc, no := 0, 0
			order := true
			intervened := false
			for _, it := range items {
				id, _ := it["call_id"].(string)
				switch it["type"] {
				case "function_call":
					nc++
					pending[id]++
				case "function_call_output":
					no++
					if id == "" || pending[id] == 0 {
						order = false
					} else {
						pending[id]--
					}
				case "message":
					for _, v := range pending {
						if v > 0 {
							intervened = true
						}
					}
				}
			}
			paired := order
			for id, v := range pending {
				if id == "" || v != 0 {
					paired = false
				}
			}
			t.Logf("calls=%d outputs=%d paired=%v call_before_output=%v intervening_message=%v input=%s", nc, no, paired, order, intervened, out.Input)
			if nc != tc.wantCalls || no != tc.wantOutputs || paired != tc.wantPair {
				t.Fatalf("unexpected shape")
			}
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

func TestM5FinishReasonMapping(t *testing.T) {
	for _, tt := range []struct {
		name, reason, args, messages, chat string
		bad                                bool
	}{
		{"stop", "STOP", "", "end_turn", "stop", false},
		{"tool", "STOP", `{"x":1}`, "tool_use", "tool_calls", false},
		{"length_tool", "MAX_TOKENS", `{"x":1}`, "max_tokens", "length", false},
		{"partial_tool", "MAX_TOKENS", `"{\"x\":"`, "max_tokens", "length", false},
		{"filter", "SAFETY", "", "refusal", "content_filter", false},
		{"recitation", "RECITATION", "", "refusal", "content_filter", false},
		{"other", "OTHER", "", "end_turn", "stop", false},
		{"unknown", "FUTURE_REASON", "", "end_turn", "stop", false},
		{"bad_call", "MALFORMED_FUNCTION_CALL", "", "", "", true},
		{"array_arguments", "STOP", `[]`, "", "", true},
		{"scalar_arguments", "STOP", `123`, "", "", true},
		{"null_arguments", "STOP", `null`, "", "", true},
		{"absent_arguments", "STOP", `absent`, "tool_use", "tool_calls", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parts := `[{"text":"answer"}]`
			if tt.args != "" {
				parts = `[{"functionCall":{"name":"f","args":` + tt.args + `}}]`
				if tt.args == "absent" {
					parts = `[{"functionCall":{"name":"f"}}]`
				}
			}
			raw := []byte(`{"candidates":[{"content":{"parts":` + parts + `},"finishReason":"` + tt.reason + `"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`)
			var response map[string]any
			require.NoError(t, json.Unmarshal(raw, &response))
			messages, usage, err := convertGeminiToClaudeMessage(response, "gemini-2.5-flash", raw, false)
			require.Equal(t, 5, usage.InputTokens)
			if tt.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.messages, messages["stop_reason"])
			}
			chat, usage, err := geminiResponseToChatCompletions(response, "gemini-2.5-flash", raw, nil)
			require.Equal(t, 5, usage.InputTokens)
			if tt.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.chat, chat.Choices[0].FinishReason)
			}
			for _, protocol := range []string{"messages", "chat"} {
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+protocol, nil)
				ctx := resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState())
				c.Request = c.Request.WithContext(ctx)
				svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\n"))}
				var got *geminiStreamResult
				if protocol == "messages" {
					got, err = svc.handleStreamingResponse(c, resp, time.Now(), "gemini-2.5-flash")
				} else {
					got, err = svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-2.5-flash", false, true)
				}
				require.NotNil(t, got, protocol)
				require.Equal(t, 5, got.usage.InputTokens)
				if tt.name == "unknown" || tt.name == "other" {
					require.Equal(t, []apicompat.ConversionNotice{{Field: "candidates[0].finishReason", Action: "map"}}, resilience.SnapshotConversionNotices(ctx))
				}
				if tt.bad {
					var conversion *apicompat.UpstreamConversionError
					require.ErrorAs(t, err, &conversion, protocol)
					require.Contains(t, recorder.Body.String(), `"error":`)
					require.NotContains(t, recorder.Body.String(), `"finish_reason":"`)
					require.NotContains(t, recorder.Body.String(), `"type":"message_stop"`)
				} else {
					require.NoError(t, err, protocol)
					if protocol == "messages" {
						require.Contains(t, recorder.Body.String(), `"stop_reason":"`+tt.messages+`"`)
					} else {
						require.Contains(t, recorder.Body.String(), `"finish_reason":"`+tt.chat+`"`)
					}
				}
			}
		})
	}
	t.Run("prompt_feedback", func(t *testing.T) {
		raw := []byte(`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":2}}`)
		var response map[string]any
		require.NoError(t, json.Unmarshal(raw, &response))
		messages, _, err := convertGeminiToClaudeMessage(response, "gemini-2.5-flash", raw, false)
		require.NoError(t, err)
		require.Equal(t, "refusal", messages["stop_reason"])
		chat, _, err := geminiResponseToChatCompletions(response, "gemini-2.5-flash", raw, nil)
		require.NoError(t, err)
		require.Equal(t, "content_filter", chat.Choices[0].FinishReason)
	})
	t.Run("partial_result", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			for _, protocol := range []string{"messages", "chat"} {
				body := []byte(fmt.Sprintf(`{"model":"gemini-3.1-pro","stream":%t,"max_tokens":512,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`, stream))
				response := `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`
				contentType := "application/json"
				if stream {
					contentType = "text/event-stream"
					response = "data: " + response + "\n\n"
				}
				upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}}}
				svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
				c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/"+protocol, body)
				var result *ForwardResult
				var err error
				if protocol == "messages" {
					result, err = svc.Forward(c.Request.Context(), c, geminiSignalTestAccount(), body)
				} else {
					result, err = svc.forwardClaudeBodyAsChatCompletions(c.Request.Context(), c, geminiSignalTestAccount(), body, "gemini-3.1-pro", stream, true, time.Now())
				}
				var conversion *apicompat.UpstreamConversionError
				require.ErrorAs(t, err, &conversion)
				require.NotNil(t, result)
				require.Equal(t, 5, result.Usage.InputTokens)
				require.Equal(t, "high", optionalStringValue(result.ReasoningEffort))
				require.Len(t, upstream.requestBodies, 1)
			}
		}
	})
	t.Run("missing_terminal", func(t *testing.T) {
		// Gemini sends no [DONE]; a clean EOF without finishReason ends normally, as in Messages.
		raw := `data: {"candidates":[{"content":{"parts":[{"text":"complete answer"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}` + "\n\n"
		for _, protocol := range []string{"messages", "chat"} {
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+protocol, nil)
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(raw))}
			svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
			var err error
			if protocol == "messages" {
				_, err = svc.handleStreamingResponse(c, resp, time.Now(), "gemini-2.5-flash")
				require.Contains(t, recorder.Body.String(), `"stop_reason":"end_turn"`)
			} else {
				_, err = svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-2.5-flash", false, true)
				require.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
				require.True(t, strings.HasSuffix(recorder.Body.String(), "data: [DONE]\n\n"))
			}
			require.NoError(t, err, protocol)
			require.Contains(t, recorder.Body.String(), "complete answer")
			require.NotContains(t, recorder.Body.String(), `"error"`)
		}
	})
}

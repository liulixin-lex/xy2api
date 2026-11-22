package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 当数组末尾不是用户消息时（典型场景：Agent 工具循环结束于 tool/assistant），
// 应直接跳过审计——不再回溯查找历史中的某条用户消息。

func TestExtractContentModerationInput_AnthropicAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"调用一下天气工具"},
			{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"weather","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"晴 25 度"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_AnthropicFirstTurnExtractsUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "Q1", input.Text)
}

func TestExtractContentModerationInput_AnthropicMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":"A1"},
			{"role":"user","content":"Q2"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_AnthropicTrailingSystemExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":"A1"},
			{"role":"user","content":"Q2"},
			{"role":"system","content":"request metadata"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_AnthropicStreamResendExtractsResend(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"原问题"},
			{"role":"assistant","content":"部分回答……"},
			{"role":"user","content":"重发"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "重发", input.Text)
}

func TestExtractContentModerationInput_OpenAIChatAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"system","content":"sys"},
			{"role":"user","content":"列出我的订单"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"orders","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"[]"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_OpenAIChatMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":"A1"},
			{"role":"user","content":"Q2"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_GeminiAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"查询天气"}]},
			{"role":"model","parts":[{"functionCall":{"name":"weather","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"temp":25}}}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_GeminiFirstTurnExtractsUser(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"你好"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Equal(t, "你好", input.Text)
}

func TestExtractContentModerationInput_GeminiMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"Q1"}]},
			{"role":"model","parts":[{"text":"A1"}]},
			{"role":"user","parts":[{"text":"Q2"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_ResponsesAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"运行测试"}]},
			{"type":"function_call","call_id":"call_1","name":"run_tests","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"all passed"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_ResponsesLastUserMessageExtracted(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Equal(t, "latest", input.Text)
}

func TestExtractContentModerationInput_ResponsesLastIsAssistantSkipped(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"q1"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func fcoModerationSettings(mode string, adminNames ...string) *resilience.Settings {
	cfg := resilience.Defaults()
	cfg.Enabled = true
	cfg.FCOCompat.Mode = mode
	cfg.FCOCompat.AdminNames = adminNames
	return &cfg
}

func fcoModerationContext(cfg *resilience.Settings, body []byte) context.Context {
	return WithOpenAIFCOCompatSnapshot(context.Background(), NewOpenAIFCOCompatSnapshot(cfg, body))
}

// fcoModerationExtract returns the semantic input and the keyword text both
// checks of ContentModerationService.Check would extract for ctx.
func fcoModerationExtract(ctx context.Context, protocol string, body []byte) (ContentModerationInput, string) {
	fco := openAIFCOModerationFromContext(ctx)
	semantic := extractContentModerationInput(protocol, body, true, fco)
	semantic.Normalize()
	return semantic, extractContentModerationKeywordTextWithFCO(protocol, body, fco)
}

func TestModerationFCOSharedRules(t *testing.T) {
	// One turn: every item after the assistant reply belongs to it.
	multi := []byte(`{"model":"gpt-5.1","input":[
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"assistant reply"}]},
		{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"  page one  "},
		{"type":"function_call_output","namespace":"agent","name":"ide_context","call_id":null,"output":"ide two"},
		{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"term three"},
		{"type":"function_call_output","namespace":"agent","name":"custom_ctx","output":"admin <four> & more"},
		{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","output":"{\"page_id\":\"p1\"}"},
		{"type":"function_call_output","namespace":"agent","name":"mystery_name","output":"unknown five"},
		{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":"call_1","output":"with id six"},
		{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":"","output":"empty id seven"},
		{"type":"function_call_output","namespace":"agent","name":"browser_info","output":[{"type":"input_text","text":"array eight"}]},
		{"type":"function_call_output","namespace":"agent","name":"browser_info","output":{"text":"object nine"}},
		{"type":"function_call_output","namespace":"tools","name":"browser_info","output":"namespace ten"},
		{"type":"function_call_output","name":"browser_info","output":"no namespace eleven"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"latest question"}]}
	]}`)
	wantTexts := []string{
		"<external_browser_info>  page one  </external_browser_info>",
		"<external_ide_context>ide two</external_ide_context>",
		"<external_terminal_info>term three</external_terminal_info>",
		"<external_custom_ctx>admin <four> & more</external_custom_ctx>",
		"<external_codex_apps_open_page>\n{\"page_id\":\"p1\"}\n</external_codex_apps_open_page>",
	}
	unmatched := []string{"assistant reply", "unknown five", "with id six", "empty id seven", "array eight", "object nine", "namespace ten", "no namespace eleven"}
	active := fcoModerationSettings(resilience.FCOCompatModeReactive, "custom_ctx")

	t.Run("every matched item of the turn once in input order across layers", func(t *testing.T) {
		ctx := fcoModerationContext(active, multi)
		fco := openAIFCOModeration{}
		require.Empty(t, fco.texts(gjson.GetBytes(multi, "input")), "the zero value adds nothing")
		fco = openAIFCOModerationFromContext(ctx)
		require.Equal(t, wantTexts, fco.texts(gjson.GetBytes(multi, "input")))

		semantic, keyword := fcoModerationExtract(ctx, ContentModerationProtocolOpenAIResponses, multi)
		want := normalizeContentModerationText(strings.Join(append([]string{"latest question"}, wantTexts...), "\n"))
		require.Equal(t, want, semantic.Text, "the latest user content stays first, matched items follow")
		require.Equal(t, want, keyword)
		for _, text := range unmatched {
			require.NotContains(t, semantic.Text, text)
		}
		// Proactive is active as well; only off disables the added extraction.
		semantic, _ = fcoModerationExtract(fcoModerationContext(fcoModerationSettings(resilience.FCOCompatModeProactive, "custom_ctx"), multi), ContentModerationProtocolOpenAIResponses, multi)
		require.Equal(t, want, semantic.Text)
	})

	t.Run("only the current turn window is added", func(t *testing.T) {
		const bi = `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":%q}`
		item := func(output string) string { return fmt.Sprintf(bi, output) }
		user := func(text string) string {
			return fmt.Sprintf(`{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}`, text)
		}
		assistant := func(text string) string {
			return fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}`, text)
		}
		wrap := func(output string) string { return "<external_browser_info>" + output + "</external_browser_info>" }
		cases := []struct {
			name  string
			items []string
			want  []string
		}{
			{
				name:  "full history adds only the last turn",
				items: []string{item("turn one"), user("q1"), assistant("a1"), item("turn two"), user("q2"), assistant("a2"), item("turn three"), user("q3")},
				want:  []string{"q3", wrap("turn three")},
			},
			{
				name:  "context before and after the user message",
				items: []string{user("q1"), item("old after"), assistant("a1"), item("now before"), user("q2"), item("now after")},
				want:  []string{wrap("now before"), wrap("now after")},
			},
			{
				name: "continuation keeps the turn of the last user message",
				items: []string{
					user("q1"), assistant("a1"),
					`{"type":"function_call_output","namespace":"agent","name":"ide_context","output":"turn ide"}`, user("q2"),
					`{"type":"function_call_output","namespace":"agent","name":"goal_objective","output":"turn goal"}`,
					`{"type":"reasoning","summary":[]}`,
					`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}`,
					`{"type":"function_call_output","call_id":"call_1","output":"tool result"}`,
					item("after the tool call"),
				},
				want: []string{"<external_ide_context>turn ide</external_ide_context>", "<external_goal_objective>turn goal</external_goal_objective>"},
			},
			{
				name: "without a user message the trailing run is the window",
				items: []string{
					item("old"), assistant("a"),
					`{"type":"function_call","call_id":"call_2","name":"shell","arguments":"{}"}`,
					`{"type":"function_call_output","call_id":"call_2","output":"tool result"}`,
					`{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"tail term"}`,
					item("tail page"),
				},
				want: []string{"<external_terminal_info>tail term</external_terminal_info>", wrap("tail page")},
			},
			{
				name:  "a message without a type is a message",
				items: []string{assistant("a"), item("typed"), `{"role":"developer","content":"note"}`, item("after note"), `{"role":"user","content":"untyped question"}`},
				want:  []string{"untyped question", wrap("typed"), wrap("after note")},
			},
		}
		for _, boundary := range []struct{ label, item string }{
			{"assistant message", assistant("reply")},
			{"untyped assistant message", `{"role":"assistant","content":"untyped reply"}`},
			{"function_call", `{"type":"function_call","call_id":"call_3","name":"shell","arguments":"{}"}`},
			{"custom_tool_call", `{"type":"custom_tool_call","call_id":"call_4","name":"exec","input":"ls"}`},
			{"reasoning", `{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}`},
			{"web_search_call", `{"type":"web_search_call","id":"ws_1","status":"completed"}`},
			{"item_reference", `{"type":"item_reference","id":"msg_1"}`},
			{"string element", `"bare string element"`},
			{"number element", `42`},
			{"null element", `null`},
		} {
			cases = append(cases, struct {
				name  string
				items []string
				want  []string
			}{
				name:  "bounded by " + boundary.label,
				items: []string{item("outside"), boundary.item, item("inside"), user("q")},
				want:  []string{"q", wrap("inside")},
			})
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.1","input":[` + strings.Join(tc.items, ",") + `]}`)
				require.True(t, gjson.ValidBytes(body))
				semantic, keyword := fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
				want := normalizeContentModerationText(strings.Join(tc.want, "\n"))
				require.Equal(t, want, semantic.Text)
				require.Equal(t, want, keyword)
			})
		}
	})

	t.Run("a string or object input adds nothing", func(t *testing.T) {
		for _, body := range [][]byte{
			[]byte(`{"input":"plain function_call_output text from \"agent\""}`),
			[]byte(`{"input":{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"object input"}}`),
		} {
			semantic, keyword := fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
			legacy := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)
			legacy.Normalize()
			require.Equal(t, legacy, semantic)
			require.Equal(t, extractContentModerationKeywordText(ContentModerationProtocolOpenAIResponses, body), keyword)
		}
	})

	t.Run("the keyword check gets every added text untruncated", func(t *testing.T) {
		long := strings.Repeat("x", maxModerationInputRunes) + " TAIL-MARKER"
		body := []byte(`{"input":[` + fmt.Sprintf(`{"type":"function_call_output","namespace":"agent","name":"browser_info","output":%q}`, long) +
			`,{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"second TAIL-TERM"}` +
			`,{"type":"message","role":"user","content":"latest question"}]}`)
		semantic, keyword := fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
		want := normalizeContentModerationText("latest question\n<external_browser_info>" + long + "</external_browser_info>\n<external_terminal_info>second TAIL-TERM</external_terminal_info>")
		require.Equal(t, want, keyword, "the keyword check sees the whole window")
		require.True(t, strings.HasPrefix(semantic.Text, "latest question <external_browser_info>xxx"), "user content comes first")
		require.Equal(t, maxModerationInputRunes, utf8.RuneCountInString(semantic.Text), "the semantic check keeps its existing budget")
		require.NotContains(t, semantic.Text, "TAIL-MARKER")
	})

	t.Run("chat native input uses the same recognizer", func(t *testing.T) {
		native := []byte(`{"model":"gpt-5.1","input":[
			{"type":"function_call_output","namespace":"agent","name":"untrusted_context","output":"chat context"},
			{"type":"function_call_output","namespace":"agent","name":"mystery_name","output":"chat unknown"}
		]}`)
		semantic, keyword := fcoModerationExtract(fcoModerationContext(active, native), ContentModerationProtocolOpenAIChat, native)
		require.Equal(t, "<external_untrusted_context>chat context</external_untrusted_context>", semantic.Text)
		require.Equal(t, semantic.Text, keyword)

		// With messages the request is not Responses shaped and input is not sent.
		withMessages := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"chat question"}],"input":[
			{"type":"function_call_output","namespace":"agent","name":"untrusted_context","output":"chat context"}
		]}`)
		semantic, keyword = fcoModerationExtract(fcoModerationContext(active, withMessages), ContentModerationProtocolOpenAIChat, withMessages)
		require.Equal(t, "chat question", semantic.Text)
		require.Equal(t, "chat question", keyword)
	})

	t.Run("keyword and semantic checks get the same added content", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"<system-reminder>page reminder</system-reminder> rest"}]}`)
		semantic, keyword := fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
		require.Equal(t, "<external_browser_info><system-reminder>page reminder</system-reminder> rest</external_browser_info>", semantic.Text,
			"the outbound text is user text, so the reminder filter does not drop it")
		require.Equal(t, semantic.Text, keyword)
	})

	t.Run("nothing is added when the snapshot disables rewriting", func(t *testing.T) {
		duplicate := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"dup page","output":"dup page"},{"role":"user","content":"hi"}]}`)
		masterOff := fcoModerationSettings(resilience.FCOCompatModeProactive, "custom_ctx")
		masterOff.Enabled = false
		cases := []struct {
			name string
			ctx  context.Context
			body []byte
		}{
			{"global mode off", fcoModerationContext(fcoModerationSettings(resilience.FCOCompatModeOff, "custom_ctx"), multi), multi},
			{"resilience master switch off", fcoModerationContext(masterOff, multi), multi},
			{"nil settings", fcoModerationContext(nil, multi), multi},
			{"no snapshot in context", context.Background(), multi},
			{"duplicate member at ingress", fcoModerationContext(active, duplicate), duplicate},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				for _, protocol := range []string{ContentModerationProtocolOpenAIResponses, ContentModerationProtocolOpenAIChat} {
					semantic, keyword := fcoModerationExtract(tc.ctx, protocol, tc.body)
					legacy := ExtractContentModerationInput(protocol, tc.body)
					legacy.Normalize()
					require.Equal(t, legacy, semantic, "audit input must match the behavior without the fco extraction")
					require.Equal(t, extractContentModerationKeywordText(protocol, tc.body), keyword)
				}
			})
		}
		// The duplicate flag comes from the ingress bytes: a body rebuilt later
		// without the duplicate still adds nothing.
		ctx := fcoModerationContext(active, duplicate)
		rebuilt := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"dup page"},{"role":"user","content":"hi"}]}`)
		semantic, _ := fcoModerationExtract(ctx, ContentModerationProtocolOpenAIResponses, rebuilt)
		require.Equal(t, "hi", semantic.Text)
	})

	t.Run("a settings reload after ingress does not change the audited rules", func(t *testing.T) {
		saved := *resilience.Current()
		t.Cleanup(func() { resilience.Publish(saved) })
		body := []byte(`{"input":[
			{"type":"function_call_output","namespace":"agent","name":"alpha_name","output":"alpha output"},
			{"type":"function_call_output","namespace":"agent","name":"beta_name","output":"beta output"}
		]}`)
		resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeReactive, "alpha_name"))
		ctx := WithOpenAIFCOCompatSnapshot(context.Background(), NewOpenAIFCOCompatSnapshot(resilience.Current(), body))
		resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeOff, "beta_name"))

		semantic, keyword := fcoModerationExtract(ctx, ContentModerationProtocolOpenAIResponses, body)
		require.Equal(t, "<external_alpha_name>alpha output</external_alpha_name>", semantic.Text)
		require.Equal(t, semantic.Text, keyword)
	})
}

func TestModerationFCOSharedRulesThroughCheck(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1","input":[
		{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"turn page with forbidden-marker"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"latest question"}]}
	]}`)
	active := fcoModerationContext(fcoModerationSettings(resilience.FCOCompatModeReactive), body)
	newService := func(t *testing.T, keywords []string) (*ContentModerationService, func() []string) {
		t.Helper()
		var mu sync.Mutex
		inputs := []string{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Input string `json:"input"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			mu.Lock()
			inputs = append(inputs, payload.Input)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{}}})
		}))
		t.Cleanup(server.Close)
		cfg := defaultContentModerationConfig()
		cfg.Enabled = true
		cfg.Mode = ContentModerationModePreBlock
		cfg.BaseURL = server.URL
		cfg.APIKeys = []string{"sk-test"}
		cfg.BlockedKeywords = keywords
		rawCfg, err := json.Marshal(cfg)
		require.NoError(t, err)
		svc := NewContentModerationService(&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(rawCfg),
		}}, &contentModerationTestRepo{}, &contentModerationTestHashCache{}, nil, nil, nil, nil, nil)
		return svc, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string{}, inputs...)
		}
	}
	check := func(t *testing.T, svc *ContentModerationService, ctx context.Context, body []byte) *ContentModerationDecision {
		t.Helper()
		decision, err := svc.Check(ctx, ContentModerationCheckInput{
			Endpoint: "/v1/responses", Provider: PlatformOpenAI, Protocol: ContentModerationProtocolOpenAIResponses, Body: body,
		})
		require.NoError(t, err)
		return decision
	}

	t.Run("keyword check", func(t *testing.T) {
		svc, inputs := newService(t, []string{"forbidden-marker"})
		decision := check(t, svc, active, body)
		require.True(t, decision.Blocked)
		require.Equal(t, ContentModerationActionKeywordBlock, decision.Action)
		require.Empty(t, inputs())

		// Once that turn is history, the next request of the thread passes.
		next := []byte(`{"model":"gpt-5.1","input":[
			{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"history page with forbidden-marker"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"latest question"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"follow-up question"}]}
		]}`)
		decision = check(t, svc, fcoModerationContext(fcoModerationSettings(resilience.FCOCompatModeReactive), next), next)
		require.False(t, decision.Blocked, "an earlier turn's item is not moderated again")
		require.Equal(t, []string{"follow-up question"}, inputs())

		decision = check(t, svc, context.Background(), body)
		require.False(t, decision.Blocked, "without an ingress snapshot the item is not moderated")
		require.Equal(t, []string{"follow-up question", "latest question"}, inputs())
	})

	t.Run("semantic check", func(t *testing.T) {
		svc, inputs := newService(t, nil)
		check(t, svc, active, body)
		require.Equal(t, []string{"latest question <external_browser_info>turn page with forbidden-marker</external_browser_info>"}, inputs())
	})
}

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
	"time"

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

func TestExtractContentModerationInput_ResponsesTrailingWindow(t *testing.T) {
	const (
		oldUser   = `{"type":"message","role":"user","content":[{"type":"input_text","text":"old question"}]}`
		newUser   = `{"type":"message","role":"user","content":[{"type":"input_text","text":"new question"}]}`
		assistant = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`
		developer = `{"type":"message","role":"developer","content":"developer note"}`
		system    = `{"role":"system","content":"system note"}`
		call      = `{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}`
		paired    = `{"type":"function_call_output","call_id":"call_1","output":"tool result"}`
		agent     = `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page context"}`
		withText  = `{"type":"custom_tool_call_output","call_id":"call_2","content":"output in content"}`
		reminder  = `{"role":"user","content":"<system-reminder>reminder</system-reminder>"}`
		picture   = `{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}`
		unlabeled = `{"type":"message","content":"UNLABELED"}`
		roleTool  = `{"type":"custom_tool_call_output","role":"user","content":"output with a user role"}`
		tools     = `{"type":"additional_tools","role":"developer","tools":[]}`
		toolsUser = `{"type":"additional_tools","role":"user","content":"declaration"}`
		trigger   = `{"type":"compaction_trigger"}`
		reference = `{"type":"item_reference","id":"msg_1"}`
	)
	for _, tc := range []struct {
		name     string
		items    []string
		keyword  string
		semantic string
		images   []string
	}{
		{"user then a standalone tool output", []string{newUser, agent}, "new question", "new question", nil},
		{"user then a developer message", []string{newUser, developer}, "new question", "new question", nil},
		{"user then a system message", []string{newUser, system}, "new question", "new question", nil},
		{"user then a tool output of an earlier response", []string{newUser, paired}, "new question", "new question", nil},
		{"user then several client items", []string{newUser, agent, developer, agent}, "new question", "new question", nil},
		{"user then a blank user message", []string{newUser, `{"role":"user","content":" "}`}, "new question", "new question", nil},
		{"image then a developer message", []string{picture, developer}, "", "", []string{"https://example.test/a.png"}},
		{"the new user message of the last turn", []string{oldUser, assistant, newUser, agent}, "new question", "new question", nil},
		{"the last of two user messages", []string{oldUser, newUser, developer}, "new question", "new question", nil},
		{"a steering message after tool outputs", []string{oldUser, call, paired, newUser, developer}, "new question", "new question", nil},
		{"a user message that ends a tool loop", []string{oldUser, call, paired, newUser}, "new question", "new question", nil},
		{"a message without a role does not hide the user", []string{newUser, unlabeled, developer}, "new question", "new question", nil},
		{"tool items with a user role do not hide the user", []string{newUser, roleTool, toolsUser, developer}, "new question", "new question", nil},
		{"user then tool declarations", []string{newUser, tools}, "new question", "new question", nil},
		{"user then a compaction trigger", []string{newUser, trigger}, "new question", "new question", nil},
		{"user, standalone output, then both transparent types", []string{newUser, agent, tools, trigger}, "new question", "new question", nil},
		{"tool declarations before the user", []string{oldUser, assistant, tools, newUser, agent}, "new question", "new question", nil},
		{"a reminder is filtered from semantic only", []string{oldUser, reminder, developer}, "<system-reminder>reminder</system-reminder>", "", nil},

		{"continuation", []string{oldUser, call, paired}, "", "", nil},
		{"continuation with a standalone tool output", []string{oldUser, call, paired, agent}, "", "", nil},
		{"continuation with a developer message", []string{oldUser, call, paired, developer}, "", "", nil},
		{"continuation with text in an earlier tool output", []string{oldUser, call, withText, agent}, "", "", nil},
		{"continuation with a compaction trigger", []string{oldUser, call, paired, trigger}, "", "", nil},
		{"continuation with tool declarations", []string{oldUser, call, paired, tools}, "", "", nil},
		{"only a message without a role in the window", []string{oldUser, assistant, unlabeled, developer}, "", "", nil},
		{"only tool items with a user role in the window", []string{oldUser, assistant, roleTool, toolsUser, developer}, "", "", nil},
		{"an item reference bounds the window", []string{newUser, reference}, "", "", nil},
		{"an empty object bounds the window", []string{newUser, `{}`}, "", "", nil},
		{"an unknown type bounds the window", []string{newUser, `{"type":"future_item"}`}, "", "", nil},
		{"no user after the last model output", []string{oldUser, assistant, developer}, "", "", nil},
		{"model output last", []string{oldUser, agent, assistant}, "", "", nil},
		{"only client context", []string{developer, agent}, "", "", nil},
		{"empty", nil, "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `"input":[` + strings.Join(tc.items, ",") + `]`
			body := []byte(`{` + input + `}`)
			for _, protocol := range []string{ContentModerationProtocolOpenAIResponses, ContentModerationProtocolOpenAIChat} {
				got := ExtractContentModerationInput(protocol, body)
				require.Equal(t, tc.semantic, got.Text, protocol)
				require.ElementsMatch(t, tc.images, got.Images, protocol)
				require.Equal(t, tc.keyword, extractContentModerationKeywordText(protocol, body), protocol)
			}
			// Other protocols keep the last-item rule.
			for _, protocol := range []string{"unknown", ""} {
				require.Equal(t, baselineExtractModerationInput(protocol, body, true), ExtractContentModerationInput(protocol, body), protocol)
				require.Equal(t, baselineExtractModerationInput(protocol, body, false).Text, extractContentModerationKeywordText(protocol, body), protocol)
			}
		})
	}
}

func TestExtractContentModerationInput_OpenAIChatInput(t *testing.T) {
	const image = "https://example.test/a.png"
	for _, tc := range []struct {
		name, body, text string
		images           []string
	}{
		{"string input", `{"input":"plain question"}`, "plain question", nil},
		{"string input that looks like a tool output", `{"input":"{\"type\":\"function_call_output\"}"}`, `{"type":"function_call_output"}`, nil},
		{"object input", `{"input":{"role":"user","content":"object question"}}`, "object question", nil},
		{"text and image", `{"input":[{"role":"user","content":[{"type":"input_text","text":"what is this"},{"type":"input_image","image_url":"` + image + `"}]}]}`, "what is this", []string{image}},
		{"input ending with assistant", `{"input":[{"role":"user","content":"q"},{"role":"assistant","content":"a"}]}`, "", nil},
		{"messages win over input", `{"messages":[{"role":"user","content":"from messages"}],"input":"from input"}`, "from messages", nil},
		{"messages without a last user still win", `{"messages":[{"role":"assistant","content":"a"}],"input":"from input"}`, "", nil},
		{"empty messages still win", `{"messages":[],"input":"from input"}`, "", nil},
		{"null messages still win", `{"messages":null,"input":"from input"}`, "", nil},
		{"neither", `{"model":"gpt-5.1"}`, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, []byte(tc.body))
			require.Equal(t, tc.text, got.Text)
			require.ElementsMatch(t, tc.images, got.Images)
			require.Equal(t, tc.text, extractContentModerationKeywordText(ContentModerationProtocolOpenAIChat, []byte(tc.body)))
		})
	}
}

// An item's own content or text field is not the user's words on the paths
// Chat and the trailing window add. Responses keeps taking such a last item,
// as it always did.
func TestExtractContentModerationInput_LastItemWithOwnContent(t *testing.T) {
	const (
		user      = `{"role":"user","content":"Q"}`
		call      = `{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}`
		side      = `{"type":"function_call_output","output":"MACHINE","content":"SIDE_CONTENT"}`
		sideText  = `{"type":"custom_tool_call_output","output":"MACHINE","text":"SIDE_CONTENT","content":[{"type":"input_image","image_url":"https://example.test/side.png"}]}`
		sideRole  = `{"type":"custom_tool_call_output","role":"user","content":"SIDE_CONTENT"}`
		declared  = `{"type":"additional_tools","role":"user","content":"DECLARED"}`
		unlabeled = `{"type":"message","content":"UNLABELED"}`
	)
	for _, tc := range []struct{ name, input, responses, chat string }{
		{"only a tool output", `[` + side + `]`, "SIDE_CONTENT", ""},
		{"tool output after the user", `[` + user + `,` + side + `]`, "SIDE_CONTENT", "Q"},
		{"tool output in a continuation", `[` + user + `,` + call + `,` + side + `]`, "SIDE_CONTENT", ""},
		{"tool output with a user role", `[` + user + `,` + sideRole + `]`, "SIDE_CONTENT", "Q"},
		{"tool output with text and an image", `[` + sideText + `]`, "", ""},
		{"tool output object", side, "SIDE_CONTENT", ""},
		{"tool output object with a user role", sideRole, "SIDE_CONTENT", ""},
		{"tool declarations", `[` + user + `,` + declared + `]`, "DECLARED", "Q"},
		{"tool declarations object", declared, "DECLARED", ""},
		{"message without a role", `[` + user + `,` + unlabeled + `]`, "UNLABELED", "UNLABELED"},
		{"message without a role object", unlabeled, "UNLABELED", "UNLABELED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"input":` + tc.input + `}`)
			responses := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)
			require.Equal(t, baselineExtractModerationInput(ContentModerationProtocolOpenAIResponses, body, true), responses, "Responses keeps the last-item rule")
			require.Equal(t, tc.responses, responses.Text)
			require.Equal(t, baselineExtractModerationInput(ContentModerationProtocolOpenAIResponses, body, false).Text, extractContentModerationKeywordText(ContentModerationProtocolOpenAIResponses, body))

			chat := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)
			require.Equal(t, tc.chat, chat.Text)
			require.Empty(t, chat.Images)
			require.Equal(t, tc.chat, extractContentModerationKeywordText(ContentModerationProtocolOpenAIChat, body))
		})
	}
}

func TestExtractContentModerationInput_UnknownProtocolKeepsLastItemRule(t *testing.T) {
	for body, want := range map[string]string{
		`{"input":[{"role":"user","content":"Q"},{"role":"developer","content":"note"}],"messages":[{"role":"user","content":"chat Q"}]}`: "chat Q",
		`{"input":[{"role":"user","content":"Q"}],"messages":[{"role":"user","content":"chat Q"}]}`:                                       "Q chat Q",
	} {
		for _, protocol := range []string{"unknown", ""} {
			require.Equal(t, want, ExtractContentModerationText(protocol, []byte(body)), "each field once")
			require.Equal(t, want, extractContentModerationKeywordText(protocol, []byte(body)))
		}
	}
}

func TestContentModerationCheck_SemanticEndpointGetsOnlyUserWords(t *testing.T) {
	const side = `{"type":"function_call_output","output":"MACHINE","content":"SIDE_CONTENT"}`
	for _, tc := range []struct {
		name, protocol, body string
		want                 []string
	}{
		{"chat tool output", ContentModerationProtocolOpenAIChat, `{"input":[` + side + `]}`, []string{}},
		{"chat tool output object", ContentModerationProtocolOpenAIChat, `{"input":` + side + `}`, []string{}},
		{"chat user then tool output", ContentModerationProtocolOpenAIChat, `{"input":[{"role":"user","content":"Q"},` + side + `]}`, []string{"Q"}},
		{"chat skips a message without a role", ContentModerationProtocolOpenAIChat, `{"input":[{"role":"user","content":"Q"},{"type":"message","content":"UNLABELED"},{"role":"developer","content":"D"}]}`, []string{"Q"}},
		{"responses skips a message without a role", ContentModerationProtocolOpenAIResponses, `{"input":[{"role":"user","content":"Q"},{"type":"message","content":"UNLABELED"},{"role":"developer","content":"D"}]}`, []string{"Q"}},
		{"unknown protocol keeps its input", "unknown", `{"input":[{"role":"user","content":"Q"},{"role":"developer","content":"D"}],"messages":[{"role":"user","content":"chat Q"}]}`, []string{"chat Q"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			inputs := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Input string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				mu.Lock()
				inputs = append(inputs, payload.Input)
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(moderationAPIResponse{Results: []moderationAPIResult{{}}})
			}))
			defer server.Close()
			cfg := defaultContentModerationConfig()
			cfg.Enabled = true
			cfg.Mode = ContentModerationModePreBlock
			cfg.BaseURL = server.URL
			cfg.APIKeys = []string{"sk-test"}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			svc := NewContentModerationService(&contentModerationTestSettingRepo{values: map[string]string{
				SettingKeyRiskControlEnabled:      "true",
				SettingKeyContentModerationConfig: string(raw),
			}}, &contentModerationTestRepo{}, &contentModerationTestHashCache{}, nil, nil, nil, nil, nil)
			decision, err := svc.Check(context.Background(), ContentModerationCheckInput{Protocol: tc.protocol, Body: []byte(tc.body)})
			require.NoError(t, err)
			require.False(t, decision.Blocked)
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, tc.want, inputs)
		})
	}
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
	semantic := ExtractContentModerationInput(protocol, body)
	semantic.Normalize()
	return semantic, openAIFCOModerationFromContext(ctx).keywordText(protocol, body)
}

// moderationBaseline returns the semantic input and the keyword text of the
// earlier extraction kept in content_moderation_baseline_ref_test.go.
func moderationBaseline(protocol string, body []byte) (ContentModerationInput, string) {
	semantic := baselineExtractModerationInput(protocol, body, true)
	semantic.Normalize()
	return semantic, baselineExtractModerationInput(protocol, body, false).Text
}

func TestModerationFCOSharedRules(t *testing.T) {
	// Every item after the assistant reply is in the trailing window.
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

	t.Run("every matched item of the window once in input order across layers", func(t *testing.T) {
		ctx := fcoModerationContext(active, multi)
		fco := openAIFCOModeration{}
		require.Empty(t, fco.texts(gjson.GetBytes(multi, "input")), "the zero value adds nothing")
		fco = openAIFCOModerationFromContext(ctx)
		require.Equal(t, wantTexts, fco.texts(gjson.GetBytes(multi, "input")))

		semantic, keyword := fcoModerationExtract(ctx, ContentModerationProtocolOpenAIResponses, multi)
		want := normalizeContentModerationText(strings.Join(append([]string{"latest question"}, wantTexts...), "\n"))
		require.Equal(t, "latest question", semantic.Text, "the semantic check never gets the items")
		require.Equal(t, want, keyword, "the latest user content stays first, matched items follow")
		for _, text := range unmatched {
			require.NotContains(t, keyword, text)
		}
		// Proactive is active as well; only off disables the added extraction.
		_, keyword = fcoModerationExtract(fcoModerationContext(fcoModerationSettings(resilience.FCOCompatModeProactive, "custom_ctx"), multi), ContentModerationProtocolOpenAIResponses, multi)
		require.Equal(t, want, keyword)
	})

	t.Run("only the trailing window is added", func(t *testing.T) {
		const bi = `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":%q}`
		item := func(output string) string { return fmt.Sprintf(bi, output) }
		user := func(text string) string {
			return fmt.Sprintf(`{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}`, text)
		}
		assistant := func(text string) string {
			return fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}`, text)
		}
		wrap := func(output string) string { return "<external_browser_info>" + output + "</external_browser_info>" }
		const (
			call   = `{"type":"function_call","call_id":"call_0","name":"shell","arguments":"{}"}`
			result = `{"type":"function_call_output","call_id":"call_0","output":"tool result"}`
		)
		type windowCase struct {
			name     string
			items    []string
			question string   // the user text both checks extract
			added    []string // what only the keyword check gets
		}
		cases := []windowCase{
			{
				name:     "full history adds only the last turn",
				items:    []string{item("turn one"), user("q1"), assistant("a1"), item("turn two"), user("q2"), assistant("a2"), item("turn three"), user("q3")},
				question: "q3",
				added:    []string{wrap("turn three")},
			},
			{
				name:     "thread start with context before the user message",
				items:    []string{`{"type":"message","role":"developer","content":"permissions"}`, item("first context"), user("environment"), item("second context"), user("q")},
				question: "q",
				added:    []string{wrap("first context"), wrap("second context")},
			},
			{
				name:     "thread start with context after the user message",
				items:    []string{user("q"), item("first context")},
				question: "q",
				added:    []string{wrap("first context")},
			},
			{
				name:     "context before and after the user message",
				items:    []string{user("q1"), item("old after"), assistant("a1"), item("now before"), user("q2"), item("now after")},
				question: "q2",
				added:    []string{wrap("now before"), wrap("now after")},
			},
			{
				name:  "only standalone items",
				items: []string{item("first"), item("second")},
				added: []string{wrap("first"), wrap("second")},
			},
			{
				name:  "continuation ending in paired tool outputs adds nothing",
				items: []string{item("turn context"), user("q"), call, result},
			},
			{
				name:  "continuation with parallel calls adds nothing",
				items: []string{user("q"), item("turn context"), call, call, result, `{"type":"custom_tool_call_output","call_id":"call_1","output":"patched"}`},
			},
			{
				name: "continuation adds only what arrived after the last model output",
				items: []string{
					user("q1"), assistant("a1"),
					`{"type":"function_call_output","namespace":"agent","name":"ide_context","output":"turn ide"}`, user("q2"),
					`{"type":"function_call_output","namespace":"agent","name":"goal_objective","output":"turn goal"}`,
					`{"type":"reasoning","summary":[]}`, call, result,
					item("after the tool call"),
				},
				added: []string{wrap("after the tool call")},
			},
			{
				name:  "new items between and after tool outputs",
				items: []string{user("q"), call, call, result, item("between"), result, item("after")},
				added: []string{wrap("between"), wrap("after")},
			},
			{
				name:  "an item from an earlier continuation is not added again",
				items: []string{user("q"), call, result, item("seen before"), call, result},
			},
			{
				name:     "a user message steering a tool loop",
				items:    []string{item("turn context"), user("q"), call, result, item("steer context"), user("steer")},
				question: "steer",
				added:    []string{wrap("steer context")},
			},
			{
				name: "without a user message the trailing run is the window",
				items: []string{
					item("old"), assistant("a"), call, result,
					`{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"tail term"}`,
					item("tail page"),
				},
				added: []string{"<external_terminal_info>tail term</external_terminal_info>", wrap("tail page")},
			},
			{
				name:     "a message without a type is a message",
				items:    []string{assistant("a"), item("typed"), `{"role":"developer","content":"note"}`, item("after note"), `{"role":"user","content":"untyped question"}`},
				question: "untyped question",
				added:    []string{wrap("typed"), wrap("after note")},
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
			{"empty object", `{}`},
			{"unknown type", `{"type":"future_item","role":"developer","content":"unknown"}`},
			{"string element", `"bare string element"`},
			{"number element", `42`},
			{"array element", `[{"role":"user","content":"nested"}]`},
			{"null element", `null`},
		} {
			cases = append(cases, windowCase{
				name:     "bounded by " + boundary.label,
				items:    []string{item("outside"), boundary.item, item("inside"), user("q")},
				question: "q",
				added:    []string{wrap("inside")},
			}, windowCase{
				name:  "bounded without a user message by " + boundary.label,
				items: []string{user("q"), item("outside"), boundary.item, item("inside")},
				added: []string{wrap("inside")},
			}, windowCase{
				name:  "closed at the end by " + boundary.label,
				items: []string{user("q"), item("outside"), boundary.item},
			})
		}
		for _, transparent := range []struct{ label, item string }{
			{"additional_tools", `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"lookup"}]}`},
			{"additional_tools shaped like an item", `{"type":"additional_tools","role":"developer","namespace":"agent","name":"browser_info","output":"declared","content":"declared"}`},
			{"compaction_trigger", `{"type":"compaction_trigger"}`},
			{"compaction_trigger shaped like an item", `{"type":"compaction_trigger","namespace":"agent","name":"browser_info","output":"triggered"}`},
		} {
			cases = append(cases, windowCase{
				name:     transparent.label + " right after the user message",
				items:    []string{user("q"), transparent.item, item("late")},
				question: "q",
				added:    []string{wrap("late")},
			}, windowCase{
				name:     transparent.label + " after a standalone item",
				items:    []string{user("q"), item("first"), transparent.item, item("second")},
				question: "q",
				added:    []string{wrap("first"), wrap("second")},
			}, windowCase{
				name:     transparent.label + " last after a standalone item",
				items:    []string{user("q"), item("page"), transparent.item},
				question: "q",
				added:    []string{wrap("page")},
			}, windowCase{
				name:     transparent.label + " last after the user message adds nothing itself",
				items:    []string{assistant("a"), user("q"), transparent.item},
				question: "q",
			}, windowCase{
				name:     transparent.label + " before the user message",
				items:    []string{item("page"), transparent.item, user("q")},
				question: "q",
				added:    []string{wrap("page")},
			}, windowCase{
				name:  transparent.label + " does not reopen an earlier turn",
				items: []string{item("old"), user("q"), call, result, transparent.item},
			})
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.1","input":[` + strings.Join(tc.items, ",") + `]}`)
				require.True(t, gjson.ValidBytes(body))
				semantic, keyword := fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
				require.Equal(t, tc.question, semantic.Text, "the semantic check never gets the items")
				require.Equal(t, normalizeContentModerationText(strings.Join(append([]string{tc.question}, tc.added...), "\n")), keyword)
			})
		}
	})

	t.Run("a string or object input adds nothing", func(t *testing.T) {
		for _, body := range [][]byte{
			[]byte(`{"input":"plain function_call_output text from \"agent\""}`),
			[]byte(`{"input":{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"object input"}}`),
		} {
			semantic, keyword := fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
			wantSemantic, wantKeyword := moderationBaseline(ContentModerationProtocolOpenAIResponses, body)
			require.Equal(t, wantSemantic, semantic)
			require.Equal(t, wantKeyword, keyword)
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
		require.Equal(t, "latest question", semantic.Text)

		// User text beyond the semantic limit does not push the items out either.
		body = []byte(`{"input":[` + fmt.Sprintf(`{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"after a long question"},{"role":"user","content":%q}`, long) + `]}`)
		_, keyword = fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
		require.Equal(t, long+" <external_browser_info>after a long question</external_browser_info>", keyword)
	})

	t.Run("chat native input uses the same recognizer", func(t *testing.T) {
		native := []byte(`{"model":"gpt-5.1","input":[
			{"type":"function_call_output","namespace":"agent","name":"untrusted_context","output":"chat context"},
			{"type":"function_call_output","namespace":"agent","name":"mystery_name","output":"chat unknown"}
		]}`)
		semantic, keyword := fcoModerationExtract(fcoModerationContext(active, native), ContentModerationProtocolOpenAIChat, native)
		require.Empty(t, semantic.Text)
		require.Equal(t, "<external_untrusted_context>chat context</external_untrusted_context>", keyword)

		continued := []byte(`{"model":"gpt-5.1","input":[
			{"type":"function_call_output","namespace":"agent","name":"untrusted_context","output":"earlier context"},
			{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"tool result"},
			{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"new term"}
		]}`)
		semantic, keyword = fcoModerationExtract(fcoModerationContext(active, continued), ContentModerationProtocolOpenAIChat, continued)
		require.Empty(t, semantic.Text)
		require.Equal(t, "<external_terminal_info>new term</external_terminal_info>", keyword, "chat input has the same trailing window")

		// With messages the request is not Responses shaped and input is not sent.
		withMessages := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"chat question"}],"input":[
			{"type":"function_call_output","namespace":"agent","name":"untrusted_context","output":"chat context"}
		]}`)
		semantic, keyword = fcoModerationExtract(fcoModerationContext(active, withMessages), ContentModerationProtocolOpenAIChat, withMessages)
		require.Equal(t, "chat question", semantic.Text)
		require.Equal(t, "chat question", keyword)
	})

	t.Run("other protocols add nothing", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page"}]}`)
		for _, protocol := range moderationComparisonProtocols {
			if protocol == ContentModerationProtocolOpenAIResponses || protocol == ContentModerationProtocolOpenAIChat {
				continue
			}
			_, keyword := fcoModerationExtract(fcoModerationContext(active, body), protocol, body)
			require.NotContains(t, keyword, "<external_", protocol)
		}
	})

	t.Run("the added text bypasses the reminder filter", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"<system-reminder>page reminder</system-reminder> rest"}]}`)
		semantic, keyword := fcoModerationExtract(fcoModerationContext(active, body), ContentModerationProtocolOpenAIResponses, body)
		require.Empty(t, semantic.Text)
		require.Equal(t, "<external_browser_info><system-reminder>page reminder</system-reminder> rest</external_browser_info>", keyword,
			"the outbound text is user text, so the reminder filter does not drop it")
	})

	t.Run("nothing is added when the snapshot disables rewriting", func(t *testing.T) {
		duplicate := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"dup page","output":"dup page"},{"role":"user","content":"hi"}]}`)
		escaped := []byte(`{"input":[{"type":"function_call_output","namespace":"\u0061gent","name":"browser_info","output":"escaped page"}]}`)
		require.False(t, NewOpenAIFCOCompatSnapshot(active, escaped).IngressMayCarryCandidates)
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
			{"no candidate marker at ingress", fcoModerationContext(active, escaped), escaped},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				for _, protocol := range []string{ContentModerationProtocolOpenAIResponses, ContentModerationProtocolOpenAIChat} {
					semantic, keyword := fcoModerationExtract(tc.ctx, protocol, tc.body)
					// These bodies have no messages, so Chat reads input like Responses.
					wantSemantic, wantKeyword := moderationBaseline(ContentModerationProtocolOpenAIResponses, tc.body)
					require.Equal(t, wantSemantic, semantic, "audit input must match the behavior without the fco extraction")
					require.Equal(t, wantKeyword, keyword)
				}
			})
		}
		// The duplicate flag comes from the ingress bytes: a body rebuilt later
		// without the duplicate still adds nothing.
		ctx := fcoModerationContext(active, duplicate)
		rebuilt := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"dup page"},{"role":"user","content":"hi"}]}`)
		_, keyword := fcoModerationExtract(ctx, ContentModerationProtocolOpenAIResponses, rebuilt)
		require.Equal(t, "hi", keyword)
	})

	// Items that spell type or namespace with JSON escapes are outside the
	// rewrite. A request whose only items are spelled that way gets no added
	// keyword text: it keeps what the user text extraction alone yields.
	t.Run("escaped type or namespace", func(t *testing.T) {
		for name, fields := range map[string]string{
			"namespace": `"type":"function_call_output","namespace":"\u0061gent"`,
			"type":      `"type":"function_call_\u006futput","namespace":"agent"`,
			"both":      `"type":"function_call_\u006futput","namespace":"\u0061gent"`,
		} {
			t.Run(name, func(t *testing.T) {
				escaped := `{` + fields + `,"name":"browser_info","output":"escaped page"}`
				for _, input := range []string{escaped, `{"role":"user","content":"question"},` + escaped, escaped + `,{"role":"user","content":"question"}`} {
					body := []byte(`{"input":[` + input + `]}`)
					for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
						_, keyword := fcoModerationExtract(fcoModerationContext(fcoModerationSettings(mode), body), ContentModerationProtocolOpenAIResponses, body)
						require.Equal(t, extractContentModerationKeywordText(ContentModerationProtocolOpenAIResponses, body), keyword, "%s %s", mode, body)
						require.NotContains(t, keyword, "escaped page")
					}
				}
			})
		}
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
		require.Empty(t, semantic.Text)
		require.Equal(t, "<external_alpha_name>alpha output</external_alpha_name>", keyword)
	})
}

func TestModerationFCOSharedRulesThroughCheck(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1","input":[
		{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"turn page with forbidden-marker"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"latest question"}]}
	]}`)
	active := fcoModerationContext(fcoModerationSettings(resilience.FCOCompatModeReactive), body)
	newService := func(t *testing.T, keywords []string, configure ...func(*ContentModerationConfig)) (*ContentModerationService, func() []string) {
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
		for _, set := range configure {
			set(cfg)
		}
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

	t.Run("keyword check sees an item that arrives after tool outputs", func(t *testing.T) {
		svc, inputs := newService(t, []string{"forbidden-marker"})
		const loop = `{"type":"message","role":"user","content":"run it"},
			{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"tool result"},
			{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"term with forbidden-marker"}`
		settings := fcoModerationSettings(resilience.FCOCompatModeReactive)
		arrived := []byte(`{"input":[` + loop + `]}`)
		require.True(t, check(t, svc, fcoModerationContext(settings, arrived), arrived).Blocked)

		continued := []byte(`{"input":[` + loop + `,
			{"type":"function_call","call_id":"call_2","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_2","output":"next result"}]}`)
		require.False(t, check(t, svc, fcoModerationContext(settings, continued), continued).Blocked, "the next continuation does not check it again")
		require.Empty(t, inputs(), "neither request calls semantic moderation")
	})

	t.Run("semantic check", func(t *testing.T) {
		svc, inputs := newService(t, nil)
		check(t, svc, active, body)
		require.Equal(t, []string{"latest question"}, inputs())
	})

	t.Run("items neither join nor trigger the semantic check", func(t *testing.T) {
		const page = `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page text"}`
		bodies := [][]byte{
			[]byte(`{"input":[` + page + `]}`),
			[]byte(`{"input":[` + page + `,{"role":"user","content":"question"}]}`),
			[]byte(`{"input":[{"role":"user","content":"question"},` + page + `]}`),
			[]byte(`{"input":[{"role":"user","content":"q"},{"type":"function_call","call_id":"c"},{"type":"function_call_output","call_id":"c","output":"r"},` + page + `]}`),
		}
		// Every route to semantic moderation: after a keyword miss, without a
		// keyword check, and asynchronously. The latter two ignore keywords.
		for name, configure := range map[string]func(*ContentModerationConfig){
			"keyword and api": func(cfg *ContentModerationConfig) { cfg.BlockedKeywords = []string{"never-matches"} },
			"api only": func(cfg *ContentModerationConfig) {
				cfg.BlockedKeywords = []string{"page text"}
				cfg.KeywordBlockingMode = ContentModerationKeywordModeAPIOnly
			},
			"observe": func(cfg *ContentModerationConfig) {
				cfg.BlockedKeywords = []string{"page text"}
				cfg.Mode = ContentModerationModeObserve
			},
		} {
			t.Run(name, func(t *testing.T) {
				for _, body := range bodies {
					want := []string{}
					if semantic := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body); !semantic.IsEmpty() {
						require.NotContains(t, semantic.Text, "page text")
						want = append(want, semantic.Text)
					}
					for _, mode := range []string{"", resilience.FCOCompatModeOff, resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
						svc, inputs := newService(t, nil, configure)
						ctx := context.Background()
						if mode != "" {
							ctx = fcoModerationContext(fcoModerationSettings(mode), body)
						}
						require.False(t, check(t, svc, ctx, body).Blocked)
						if name == "observe" {
							require.Equal(t, int64(len(want)), svc.asyncEnqueued.Load())
							require.Eventually(t, func() bool { return len(inputs()) == len(want) }, 5*time.Second, 10*time.Millisecond)
						}
						require.Equal(t, want, inputs(), "mode %q, body %s", mode, body)
					}
				}
			})
		}
	})
}

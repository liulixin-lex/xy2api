package service

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The audit builds the outbound text itself; it must stay identical to what
// normalizeOpenAIFCOBody actually sends for every rule layer and template.
func TestOpenAIFCOCompatOutboundTextMatchesNormalizer(t *testing.T) {
	rules := DefaultOpenAIFCORules([]string{"custom_ctx"})
	items := []string{
		`{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"  spaced <b>&amp;</b> \"quoted\"\n"}`,
		`{"type":"function_call_output","namespace":"agent","name":"goal_objective","output":"goal"}`,
		`{"type":"function_call_output","namespace":"agent","name":"untrusted_context","output":"context"}`,
		`{"type":"function_call_output","namespace":"agent","name":"ide_context","output":"ide é中"}`,
		`{"type":"function_call_output","namespace":"agent","name":"untrusted_content","output":""}`,
		`{"type":"function_call_output","namespace":"agent","name":"proposed_plan","output":"   "}`,
		`{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","output":"{\"page_id\":\"p1\",\"title\":\"t\"}"}`,
		`{"type":"function_call_output","namespace":"agent","name":"codex_apps_selected_page_part_1_of_2","output":"Companion Page snapshot part 1 of 2:\r\n{\"page_id\":"}`,
		`{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"term"}`,
		`{"type":"function_call_output","namespace":"agent","name":"browser_tabs","output":"tabs"}`,
		`{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":null,"output":"null id"}`,
		`{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","call_id":null,"output":"{\"page_id\":null}"}`,
		`{"type":"function_call_output","namespace":"agent","name":"custom_ctx","output":" admin "}`,
	}
	wrapped := 0
	for _, raw := range items {
		item := gjson.Parse(raw)
		match, ok := matchOpenAIFCOItem(item, rules)
		require.True(t, ok, raw)
		if match.Wrapped {
			wrapped++
		}
		rewritten, summary, err := normalizeOpenAIFCOBody([]byte(`{"input":[`+raw+`]}`), rules)
		require.NoError(t, err)
		require.Equal(t, 1, summary.Matched, raw)
		sent := gjson.GetBytes(rewritten, "input.0.content.0.text")
		require.Equal(t, gjson.String, sent.Type, raw)
		require.Equal(t, sent.String(), openAIFCOCompatOutboundText(match, item.Get("output").String()), raw)
	}
	require.Equal(t, 3, wrapped, "both templates are covered")
}

// Moderation folds whitespace in all of its input. Through the real keyword
// extraction and normalization, a matched item must yield exactly the text
// normalizeOpenAIFCOBody sends, folded the same way.
func TestOpenAIFCOCompatModeratedTextMatchesRewrittenText(t *testing.T) {
	rules := DefaultOpenAIFCORules([]string{"custom_ctx"})
	cfg := fcoModerationSettings(resilience.FCOCompatModeReactive, "custom_ctx")
	items := map[string]string{
		"surrounding whitespace": `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"  \t page  body \n"}`,
		"newlines":               `{"type":"function_call_output","namespace":"agent","name":"ide_context","output":"line one\nline two\r\n\nline three"}`,
		"closing tag in output":  `{"type":"function_call_output","namespace":"agent","name":"untrusted_context","output":"a </external_untrusted_context> b <external_x>"}`,
		"non-ascii":              `{"type":"function_call_output","namespace":"agent","name":"goal_objective","output":"目标 été 🚀 ok"}`,
		"open page template":     `{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","output":"  {\"page_id\":\"p1\",\n \"title\":\"t\"}  "}`,
		"selected page template": `{"type":"function_call_output","namespace":"agent","name":"codex_apps_selected_page_part_1_of_2","output":"Companion Page snapshot part 1 of 2:\r\n  {\"page_id\":  "}`,
		"admin layer":            `{"type":"function_call_output","namespace":"agent","name":"custom_ctx","output":" admin\tvalue "}`,
		"null call_id":           `{"type":"function_call_output","namespace":"agent","name":"terminal_info","call_id":null,"output":"\nterm\n"}`,
	}
	for name, item := range items {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.1","input":[` + item + `]}`)
			rewritten, summary, err := normalizeOpenAIFCOBody(body, rules)
			require.NoError(t, err)
			require.Equal(t, 1, summary.Matched)
			want := normalizeContentModerationText(gjson.GetBytes(rewritten, "input.0.content.0.text").String())
			require.NotEmpty(t, want)

			semantic, keyword := fcoModerationExtract(fcoModerationContext(cfg, body), ContentModerationProtocolOpenAIResponses, body)
			require.Empty(t, semantic.Text)
			require.Equal(t, want, keyword)
		})
	}
}

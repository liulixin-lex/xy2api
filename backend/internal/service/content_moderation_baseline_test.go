package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var moderationComparisonProtocols = []string{
	ContentModerationProtocolOpenAIResponses,
	ContentModerationProtocolOpenAIChat,
	ContentModerationProtocolAnthropicMessages,
	ContentModerationProtocolGemini,
	ContentModerationProtocolOpenAIImages,
	ContentModerationProtocolTypeSafeSystemOne,
	"unknown",
}

// The semantic input of every request, and the keyword text without added
// standalone items, are exactly what the baseline extracts.
func TestExtractContentModerationInputMatchesBaseline(t *testing.T) {
	items := []string{
		`{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page context"}`,
		`{"type":"function_call_output","namespace":"agent","name":"terminal_info","call_id":null,"output":"terminal context"}`,
		`{"type":"function_call_output","namespace":"agent","name":"ide_context","output":"<system-reminder>ide</system-reminder>"}`,
		`{"type":"function_call_output","call_id":"call_1","output":"tool result"}`,
		`{"type":"custom_tool_call_output","call_id":"call_2","content":"output in content"}`,
		`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}`,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}`,
		`{"role":"user","content":"<system-reminder>reminder</system-reminder>"}`,
		`{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}`,
		`{"type":"message","role":"developer","content":"developer note"}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`,
		`{"type":"input_text","text":"bare text"}`,
		`{"type":"reasoning","summary":[]}`,
		`{"type":"additional_tools","role":"developer","tools":[]}`,
		`{"type":"compaction_trigger"}`,
		`"bare string"`,
	}
	bodies := []string{
		``,
		`{`,
		`{}`,
		`{"input":"plain text"}`,
		`{"input":{"role":"user","content":"object question"}}`,
		`{"input":` + items[0] + `}`,
		`{"prompt":"draw","contents":[{"role":"user","parts":[{"text":"gemini question"}]}],"state":"state"}`,
	}
	// Every input array of up to three of the items, with and without messages.
	var add func(chosen []string)
	add = func(chosen []string) {
		input := `"input":[` + strings.Join(chosen, ",") + `]`
		bodies = append(bodies, `{`+input+`}`, `{"messages":[{"role":"user","content":"chat question"}],`+input+`}`)
		if len(chosen) == 3 {
			return
		}
		for _, item := range items {
			add(append(chosen, item))
		}
	}
	add(nil)

	for _, protocol := range moderationComparisonProtocols {
		t.Run(protocol, func(t *testing.T) {
			for _, body := range bodies {
				raw := []byte(body)
				require.Equal(t, baselineExtractModerationInput(protocol, raw, true), ExtractContentModerationInput(protocol, raw), "semantic input of %s", body)
				require.Equal(t, baselineExtractModerationInput(protocol, raw, false).Text, openAIFCOModeration{}.keywordText(protocol, raw), "keyword text of %s", body)
			}
		})
	}
}

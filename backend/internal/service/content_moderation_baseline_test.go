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

type moderationWindowKind int

const (
	windowBoundary moderationWindowKind = iota // ends the trailing window
	windowOther                                // in the window, never extracted from it
	windowUserText                             // in the window, extracted if it is the last of its kind
)

// Compared with the baseline, extraction differs in exactly two ways:
//   - a Responses input whose last item is not user text yields the last user
//     text item in the trailing window, as if that item were the last one;
//   - Chat without messages extracts input like Responses.
//
// Everything else is identical. The expectation is built from the labels below
// and the baseline, not from the production helpers.
func TestExtractContentModerationInputComparedWithBaseline(t *testing.T) {
	type comparisonItem struct {
		raw string
		// lastIsText: the baseline extracts the item when it is the last one.
		lastIsText bool
		window     moderationWindowKind
	}
	items := []comparisonItem{
		{`{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}`, true, windowUserText},
		{`{"role":"user","content":"<system-reminder>reminder</system-reminder>"}`, true, windowUserText},
		{`{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}`, true, windowUserText},
		{`{"type":"message","content":"no role"}`, true, windowUserText},
		{`{"role":"user","content":" "}`, false, windowOther},
		{`{"type":"message","role":"developer","content":"developer note"}`, false, windowOther},
		{`{"role":"system","content":"system note"}`, false, windowOther},
		{`{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page context"}`, false, windowOther},
		{`{"type":"function_call_output","call_id":"call_1","output":"tool result"}`, false, windowOther},
		{`{"type":"custom_tool_call_output","call_id":"call_2","content":"output in content"}`, true, windowOther},
		{`{"type":"input_text","text":"bare text"}`, true, windowBoundary},
		{`{"content":"no type or role"}`, true, windowBoundary},
		{`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`, false, windowBoundary},
		{`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}`, false, windowBoundary},
		{`{"type":"reasoning","summary":[]}`, false, windowBoundary},
		{`{"type":"additional_tools","role":"developer","tools":[]}`, false, windowOther},
		{`{"type":"compaction_trigger"}`, false, windowOther},
		{`"bare string"`, false, windowBoundary},
		{`null`, false, windowBoundary},
	}
	const messages = `"messages":[{"role":"user","content":"chat question"}],`
	type comparisonCase struct {
		body        string
		asIfBody    string // the body whose baseline extraction is expected
		hasMessages bool
	}
	var cases []comparisonCase
	for _, body := range []string{
		``,
		`{`,
		`{}`,
		`{"input":null}`,
		`{"input":"plain text"}`,
		`{"input":{"role":"user","content":"object question"}}`,
		`{"input":{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page context"}}`,
		`{"prompt":"draw","contents":[{"role":"user","parts":[{"text":"gemini question"}]}],"state":"state"}`,
	} {
		cases = append(cases, comparisonCase{body: body, asIfBody: body})
	}
	cases = append(cases, comparisonCase{body: `{"messages":null,"input":"plain text"}`, asIfBody: `{"messages":null,"input":"plain text"}`, hasMessages: true})

	// Every input array of up to three of the items, with and without messages.
	var add func(chosen []comparisonItem)
	add = func(chosen []comparisonItem) {
		raws := make([]string, len(chosen))
		for i, item := range chosen {
			raws[i] = item.raw
		}
		input := `"input":[` + strings.Join(raws, ",") + `]`
		asIf := input
		if n := len(chosen); n > 0 && !chosen[n-1].lastIsText {
			for i := n - 1; i >= 0 && chosen[i].window != windowBoundary; i-- {
				if chosen[i].window == windowUserText {
					asIf = `"input":[` + chosen[i].raw + `]`
					break
				}
			}
		}
		cases = append(cases,
			comparisonCase{body: `{` + input + `}`, asIfBody: `{` + asIf + `}`},
			comparisonCase{body: `{` + messages + input + `}`, asIfBody: `{` + messages + asIf + `}`, hasMessages: true})
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
			for _, tc := range cases {
				asProtocol, asBody := protocol, tc.body
				switch protocol {
				case ContentModerationProtocolOpenAIResponses, "unknown":
					asBody = tc.asIfBody
				case ContentModerationProtocolOpenAIChat:
					if !tc.hasMessages {
						asProtocol, asBody = ContentModerationProtocolOpenAIResponses, tc.asIfBody
					}
				}
				raw, expected := []byte(tc.body), []byte(asBody)
				semantic, keyword := ExtractContentModerationInput(protocol, raw), openAIFCOModeration{}.keywordText(protocol, raw)
				require.Equal(t, baselineExtractModerationInput(asProtocol, expected, true), semantic, "semantic input of %s", tc.body)
				require.Equal(t, baselineExtractModerationInput(asProtocol, expected, false).Text, keyword, "keyword text of %s", tc.body)
				if protocol != ContentModerationProtocolTypeSafeSystemOne { // System One moderates every field
					require.NotContains(t, keyword+semantic.Text, "page context", "the output of a tool item is never extracted: %s", tc.body)
				}
			}
		})
	}
}

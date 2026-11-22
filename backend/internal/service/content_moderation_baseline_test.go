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
	"",
}

type moderationWindowKind int

const (
	windowBoundary moderationWindowKind = iota // ends the trailing window
	windowOther                                // in the window, never extracted from it
	windowMachine                              // like windowOther, and never Chat user text
	windowUserText                             // in the window, extracted if it is the last of its kind
)

// Compared with the baseline, extraction differs in exactly two ways:
//   - a Responses input whose last item is not user text yields the last
//     message with an explicit user role in the trailing window, as if that
//     item were the last one;
//   - Chat without messages extracts input like Responses, except that a tool
//     output or transparent item is never user text there.
//
// Everything else is identical, unknown protocols included. The expectation is built from the labels below
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
		{`{"type":"message","content":"no role"}`, true, windowOther},
		{`{"role":"user","content":" "}`, false, windowOther},
		{`{"type":"message","role":"developer","content":"developer note"}`, false, windowOther},
		{`{"role":"system","content":"system note"}`, false, windowOther},
		{`{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page context"}`, false, windowMachine},
		{`{"type":"function_call_output","call_id":"call_1","output":"tool result"}`, false, windowMachine},
		{`{"type":"custom_tool_call_output","call_id":"call_2","content":"output in content"}`, true, windowMachine},
		{`{"type":"custom_tool_call_output","role":"user","content":"output with a user role"}`, true, windowMachine},
		{`{"type":"additional_tools","role":"user","content":"declaration"}`, true, windowMachine},
		{`{"type":"item_reference","id":"msg_1"}`, false, windowBoundary},
		{`{}`, false, windowBoundary},
		{`{"type":"input_text","text":"bare text"}`, true, windowBoundary},
		{`{"content":"no type or role"}`, true, windowBoundary},
		{`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`, false, windowBoundary},
		{`{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}`, false, windowBoundary},
		{`{"type":"reasoning","summary":[]}`, false, windowBoundary},
		{`{"type":"additional_tools","role":"developer","tools":[]}`, false, windowMachine},
		{`{"type":"compaction_trigger"}`, false, windowMachine},
		{`"bare string"`, false, windowBoundary},
		{`null`, false, windowBoundary},
	}
	const messages = `"messages":[{"role":"user","content":"chat question"}],`
	type comparisonCase struct {
		body string
		// The bodies whose baseline Responses extraction is expected.
		responses, chat string
		hasMessages     bool
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
		cases = append(cases, comparisonCase{body: body, responses: body, chat: body})
	}
	for _, item := range items {
		body, chat := `{"input":`+item.raw+`}`, `{}`
		if item.window != windowMachine {
			chat = body
		}
		cases = append(cases, comparisonCase{body: body, responses: body, chat: chat})
	}
	const nullMessages = `{"messages":null,"input":"plain text"}`
	cases = append(cases, comparisonCase{body: nullMessages, responses: nullMessages, hasMessages: true})

	// Every input array of up to three of the items, with and without messages.
	var add func(chosen []comparisonItem)
	add = func(chosen []comparisonItem) {
		raws := make([]string, len(chosen))
		for i, item := range chosen {
			raws[i] = item.raw
		}
		input := `"input":[` + strings.Join(raws, ",") + `]`
		fromWindow := `"input":[]`
		for i := len(chosen) - 1; i >= 0 && chosen[i].window != windowBoundary; i-- {
			if chosen[i].window == windowUserText {
				fromWindow = `"input":[` + chosen[i].raw + `]`
				break
			}
		}
		responses, chat := fromWindow, fromWindow
		if n := len(chosen); n > 0 && chosen[n-1].lastIsText {
			responses = input
			if chosen[n-1].window != windowMachine {
				chat = input
			}
		}
		cases = append(cases,
			comparisonCase{body: `{` + input + `}`, responses: `{` + responses + `}`, chat: `{` + chat + `}`},
			comparisonCase{body: `{` + messages + input + `}`, responses: `{` + messages + responses + `}`, hasMessages: true})
		if len(chosen) == 3 {
			return
		}
		for _, item := range items {
			add(append(chosen, item))
		}
	}
	add(nil)

	for _, protocol := range moderationComparisonProtocols {
		t.Run("protocol="+protocol, func(t *testing.T) {
			for _, tc := range cases {
				asProtocol, asBody := protocol, tc.body
				switch protocol {
				case ContentModerationProtocolOpenAIResponses:
					asBody = tc.responses
				case ContentModerationProtocolOpenAIChat:
					if !tc.hasMessages {
						asProtocol, asBody = ContentModerationProtocolOpenAIResponses, tc.chat
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

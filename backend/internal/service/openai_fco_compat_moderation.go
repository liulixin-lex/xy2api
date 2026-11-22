package service

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// openAIFCOCompatOutboundText is the user text a matched standalone
// function_call_output becomes when it is rewritten. It wraps the original
// output unchanged: no trimming and no escaping.
func openAIFCOCompatOutboundText(match FCOCompatMatch, output string) string {
	if match.Wrapped {
		return "<external_" + match.Name + ">\n" + output + "\n</external_" + match.Name + ">"
	}
	return "<external_" + match.Name + ">" + output + "</external_" + match.Name + ">"
}

// openAIFCOModeration lets content moderation see the standalone
// function_call_output items an outbound attempt may send as user text. The
// zero value is disabled and adds nothing.
//
// Moderation covers the matched items in the current-turn window of the input
// the entry point received, the same boundary as for user messages: earlier
// turns were moderated when they were current. Items that only appear later,
// through a WebSocket history replay or a protocol conversion after
// moderation, may be rewritten without being moderated, just as historical
// user messages are sent without being moderated again.
type openAIFCOModeration struct {
	rules   FCOCompatRules
	enabled bool
}

// openAIFCOModerationFromContext reads the snapshot the entry point stored. It
// is disabled when there is no snapshot, when the global mode is off (which
// includes the resilience master switch being off), or when the client body
// carried a duplicate member, because no outbound attempt rewrites then. It
// is also disabled when the body cannot carry a candidate at all, so ordinary
// requests are not parsed a second time.
// Moderation runs before an account is selected, so account overrides do not
// apply. It never falls back to the current settings.
func openAIFCOModerationFromContext(ctx context.Context) openAIFCOModeration {
	snapshot, ok := OpenAIFCOCompatSnapshotFromContext(ctx)
	if !ok || !snapshot.IngressMayCarryCandidates || snapshot.IngressHasDuplicateMembers || snapshot.GlobalMode() == resilience.FCOCompatModeOff {
		return openAIFCOModeration{}
	}
	return openAIFCOModeration{rules: snapshot.Rules, enabled: true}
}

// texts returns the outbound text of every item the rules match within the
// current-turn window of a Responses input array, once per item and in input
// order. A string or object input adds nothing.
func (m openAIFCOModeration) texts(input gjson.Result) []string {
	if !m.enabled || !input.IsArray() {
		return nil
	}
	items := input.Array()
	start, end := responsesCurrentTurnWindow(items)
	var texts []string
	for _, item := range items[start:end] {
		if match, ok := matchOpenAIFCOItem(item, m.rules); ok {
			texts = append(texts, openAIFCOCompatOutboundText(match, item.Get("output").String()))
		}
	}
	return texts
}

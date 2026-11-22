package service

import (
	"context"
	"strings"

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

// openAIFCOModeration lets the keyword check see the standalone
// function_call_output items an outbound attempt may send as user text. The
// zero value is disabled and adds nothing.
//
// Only the keyword check reads the output text added here. It is context the
// client generates, which semantic moderation flags falsely, so it neither
// joins the semantic input nor causes a semantic call.
//
// The keyword check covers the matched items in the trailing window of the
// input the entry point received, the items that are new in this request.
// Items that only appear later, through a WebSocket history replay or a
// protocol conversion after moderation, may be rewritten without being
// checked, just as historical user messages are sent without being moderated
// again.
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

// keywordText returns the text the keyword check inspects: the client text it
// always inspects, then the outbound text of the matched items. Only Responses
// input is rewritten, which includes a Chat request with input and no
// messages. The added texts bypass the reminder filter and are not truncated.
func (m openAIFCOModeration) keywordText(protocol string, body []byte) string {
	text := extractContentModerationKeywordText(protocol, body)
	if !m.enabled || !gjson.ValidBytes(body) {
		return text
	}
	switch protocol {
	case ContentModerationProtocolOpenAIResponses:
	case ContentModerationProtocolOpenAIChat:
		if gjson.GetBytes(body, "messages").Exists() {
			return text
		}
	default:
		return text
	}
	added := m.texts(gjson.GetBytes(body, "input"))
	if len(added) == 0 {
		return text
	}
	return normalizeContentModerationText(strings.Join(append([]string{text}, added...), "\n"))
}

// texts returns the outbound text of every item the rules match within the
// trailing window of a Responses input array, once per item and in input
// order. A string or object input adds nothing.
func (m openAIFCOModeration) texts(input gjson.Result) []string {
	if !m.enabled || !input.IsArray() {
		return nil
	}
	var texts []string
	for _, item := range responsesTrailingWindow(input.Array()) {
		if match, ok := matchOpenAIFCOItem(item, m.rules); ok {
			texts = append(texts, openAIFCOCompatOutboundText(match, item.Get("output").String()))
		}
	}
	return texts
}

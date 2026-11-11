package apicompat

import (
	"bytes"
	"encoding/json"
	"regexp"
)

const ClaudeCodeEnvelopeV2Identity = "You are Claude Code, Anthropic's official CLI for Claude."

var claudeCodeBillingV2 = regexp.MustCompile(`^x-anthropic-billing-header: cc_version=[0-9]+\.[0-9]+\.[0-9]+\.[0-9a-f]{3}; cc_entrypoint=cli;$`)

// NormalizeClaudeCodeEnvelopeV2 is opt-in. It normalizes only exact, standalone
// leading billing/identity blocks to [billing, identity, user blocks...].
// The caller supplies a precomputed billing header; this helper neither derives
// identity nor changes user text, metadata, fingerprints or account settings.
// Unknown envelope versions and blocks with extra fields are never discarded.
func NormalizeClaudeCodeEnvelopeV2(system json.RawMessage, billingHeader string) (json.RawMessage, error) {
	if !claudeCodeBillingV2.MatchString(billingHeader) {
		return nil, unsupportedConversion("billing_header", "expected an exact Claude Code v2 billing header")
	}
	var blocks []json.RawMessage
	if len(normalizedRawJSON(system)) > 0 {
		var text string
		if json.Unmarshal(system, &text) == nil {
			block, _ := json.Marshal(map[string]string{"type": "text", "text": text})
			blocks = append(blocks, block)
		} else if json.Unmarshal(system, &blocks) != nil {
			return nil, unsupportedConversion("system", "expected text or a text-block array")
		}
	}
	for _, raw := range blocks {
		block, ok := rawJSONObject(raw)
		var text string
		if !ok || rawString(block["type"]) != "text" || len(normalizedRawJSON(block["text"])) == 0 || json.Unmarshal(block["text"], &text) != nil {
			return nil, unsupportedConversion("system", "expected text blocks")
		}
	}
	prefix := 0
	for prefix < len(blocks) {
		block, _ := rawJSONObject(blocks[prefix])
		text := rawString(block["text"])
		if text != ClaudeCodeEnvelopeV2Identity && !claudeCodeBillingV2.MatchString(text) {
			break
		}
		if len(block) != 2 {
			return nil, unsupportedConversion("system", "cannot normalize envelope blocks carrying additional fields")
		}
		prefix++
	}
	for _, raw := range blocks[prefix:] {
		block, _ := rawJSONObject(raw)
		text := rawString(block["text"])
		if text == ClaudeCodeEnvelopeV2Identity || claudeCodeBillingV2.MatchString(text) {
			return nil, unsupportedConversion("system", "standalone envelope blocks must form a leading prefix")
		}
	}
	billing, _ := json.Marshal(map[string]string{"type": "text", "text": billingHeader})
	identity, _ := json.Marshal(map[string]string{"type": "text", "text": ClaudeCodeEnvelopeV2Identity})
	out, err := json.Marshal(append([]json.RawMessage{billing, identity}, blocks[prefix:]...))
	if bytes.Equal(out, system) {
		return append(json.RawMessage(nil), system...), nil
	}
	return out, err
}

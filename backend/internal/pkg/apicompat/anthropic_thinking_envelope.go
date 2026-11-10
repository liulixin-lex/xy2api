package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeAnthropicThinkingEnvelope recognizes only this bridge's explicit
// envelope. It never interprets arbitrary provider encrypted_content.
func DecodeAnthropicThinkingEnvelope(envelope string) (AnthropicContentBlock, bool, error) {
	var block AnthropicContentBlock
	if !strings.HasPrefix(envelope, anthropicThinkingEnvelopePrefix) {
		return block, false, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(envelope, anthropicThinkingEnvelopePrefix))
	if err != nil {
		return block, true, fmt.Errorf("invalid Anthropic thinking envelope: %w", err)
	}
	if err = json.Unmarshal(raw, &block); err != nil {
		return block, true, fmt.Errorf("invalid Anthropic thinking block: %w", err)
	}
	if (block.Type != "thinking" || block.Signature == "") && (block.Type != "redacted_thinking" || block.Data == "") {
		return block, true, fmt.Errorf("invalid Anthropic signed thinking block")
	}
	return block, true, nil
}

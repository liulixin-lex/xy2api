package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeCodeEnvelopeV2(t *testing.T) {
	const billing = "x-anthropic-billing-header: cc_version=2.1.161.abc; cc_entrypoint=cli;"
	const oldBilling = "x-anthropic-billing-header: cc_version=2.1.114.def; cc_entrypoint=cli;"
	block := func(text string) json.RawMessage {
		raw, err := json.Marshal(map[string]string{"type": "text", "text": text})
		require.NoError(t, err)
		return raw
	}
	user := json.RawMessage(`{"type":"text","text":"  My system text\n","cache_control":{"type":"ephemeral","ttl":"1h"},"future_flag":true}`)
	for _, prefix := range [][]json.RawMessage{nil, {block(ClaudeCodeEnvelopeV2Identity)}, {block(oldBilling), block(ClaudeCodeEnvelopeV2Identity)}, {block(ClaudeCodeEnvelopeV2Identity), block(oldBilling), block(ClaudeCodeEnvelopeV2Identity)}} {
		input, err := json.Marshal(append(prefix, user))
		require.NoError(t, err)
		original := append([]byte(nil), input...)
		normalized, err := NormalizeClaudeCodeEnvelopeV2(input, billing)
		require.NoError(t, err)
		require.Equal(t, original, input, "must not mutate input")
		var blocks []json.RawMessage
		require.NoError(t, json.Unmarshal(normalized, &blocks))
		require.Len(t, blocks, 3)
		require.JSONEq(t, string(block(billing)), string(blocks[0]))
		require.JSONEq(t, string(block(ClaudeCodeEnvelopeV2Identity)), string(blocks[1]))
		require.Equal(t, string(user), string(blocks[2]), "unknown fields/cache markers and user text must survive")
		again, err := NormalizeClaudeCodeEnvelopeV2(normalized, billing)
		require.NoError(t, err)
		require.Equal(t, normalized, again, "normalization must be byte-idempotent")
	}
	// Strings containing identities, quoted examples and near-matching billing
	// prefixes remain user text; only complete standalone blocks are envelopes.
	for _, text := range []string{"  " + ClaudeCodeEnvelopeV2Identity, ClaudeCodeEnvelopeV2Identity + "\nProject rules", "Example: " + billing, "x-anthropic-billing-header: cc_version=future;", "ordinary system text"} {
		input, _ := json.Marshal(text)
		normalized, err := NormalizeClaudeCodeEnvelopeV2(input, billing)
		require.NoError(t, err)
		var blocks []map[string]any
		require.NoError(t, json.Unmarshal(normalized, &blocks))
		require.Len(t, blocks, 3)
		require.Equal(t, text, blocks[2]["text"])
		again, err := NormalizeClaudeCodeEnvelopeV2(normalized, billing)
		require.NoError(t, err)
		require.Equal(t, normalized, again)
	}
	for _, raw := range []string{`{}`, `[null]`, `[{"type":"image","source":{}}]`, `[{"type":"text","text":42}]`, `[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}]`} {
		_, err := NormalizeClaudeCodeEnvelopeV2(json.RawMessage(raw), billing)
		requireUnsupportedConversion(t, err, "system")
	}
	_, err := NormalizeClaudeCodeEnvelopeV2(nil, billing+"\nuser rules")
	requireUnsupportedConversion(t, err, "billing_header")
	late, err := json.Marshal([]json.RawMessage{user, block(ClaudeCodeEnvelopeV2Identity)})
	require.NoError(t, err)
	_, err = NormalizeClaudeCodeEnvelopeV2(late, billing)
	requireUnsupportedConversion(t, err, "system")
}

func TestProtocolOrdinarySystemText(t *testing.T) {
	text := "  x-anthropic-billing-header: this is user-authored text\nYou are Claude Code, Anthropic's official CLI for Claude.\n"
	raw, _ := json.Marshal(text)
	out, err := AnthropicToResponses(&AnthropicRequest{MaxTokens: 100, System: raw, Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}})
	require.NoError(t, err)
	var items []ResponsesInputItem
	require.NoError(t, json.Unmarshal(out.Input, &items))
	var parts []ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Equal(t, text, parts[0].Text)
	got, err := ResponsesToAnthropicRequest(&ResponsesRequest{Instructions: text, Input: json.RawMessage(`"hi"`)})
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(got.System))
	got, err = ResponsesToAnthropicRequest(&ResponsesRequest{Instructions: "   ", Input: json.RawMessage(`"hi"`)})
	require.NoError(t, err)
	require.JSONEq(t, `"   "`, string(got.System))
}

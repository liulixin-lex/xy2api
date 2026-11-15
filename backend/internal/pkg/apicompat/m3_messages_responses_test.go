package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestM3ForeignReasoningEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, signature string
		keep            bool
	}{
		{"v1", "ccswitch-openai-reasoning-v1:eyJ0ZXh0IjoiYSJ9", false},
		{"v1 damaged", "ccswitch-openai-reasoning-v1:broken", false},
		{"v1 whitespace", " \tccswitch-openai-reasoning-v1:data\n", false},
		{"empty", " \t", false},
		{"gAAAA", "gAAAA-ciphertext", false},
		{"provider", " provider-signature ", true},
		{"unknown version", "ccswitch-openai-reasoning-v2:data", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := []AnthropicContentBlock{
				{Type: "thinking", Thinking: "thought", Signature: tc.signature},
				{Type: "text", Text: "answer"},
				{Type: "tool_use", ID: "call_a", Name: "f", Input: json.RawMessage(`{"x":1}`)},
			}
			raw, err := json.Marshal(blocks)
			require.NoError(t, err)
			items, err := anthropicAssistantToResponses(raw)
			require.NoError(t, err)
			if tc.keep {
				require.Len(t, items, 3)
				require.Equal(t, "reasoning", items[0].Type)
				require.Equal(t, strings.TrimSpace(tc.signature), items[0].EncryptedContent)
				items = items[1:]
			}
			require.Len(t, items, 2)
			require.Equal(t, "message", items[0].Type)
			require.JSONEq(t, `[{"type":"output_text","text":"answer"}]`, string(items[0].Content))
			require.Equal(t, "function_call", items[1].Type)
			require.Equal(t, "call_a", items[1].CallID)
			require.Equal(t, `{"x":1}`, items[1].Arguments)
		})
	}
}

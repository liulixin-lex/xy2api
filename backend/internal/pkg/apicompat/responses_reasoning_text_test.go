//go:build unit

package apicompat

import (
	"encoding/json"
	"testing"
)

func TestFoundationReasoningText(t *testing.T) {
	for _, tc := range []struct {
		item ResponsesOutput
		want string
	}{
		{ResponsesOutput{Type: "reasoning", Summary: []ResponsesSummary{{Type: "summary_text", Text: " a "}, {Type: "text", Text: "ignore"}, {Type: "summary_text", Text: "b"}}, Content: []ResponsesContentPart{{Type: "reasoning_text", Text: "fallback"}}}, " a b"},
		{ResponsesOutput{Type: "reasoning", Content: []ResponsesContentPart{{Type: "reasoning_text", Text: "a"}, {Type: "text", Text: "ignore"}, {Type: "reasoning_text", Text: "b"}}}, "ab"},
		{ResponsesOutput{Type: "reasoning", EncryptedContent: "opaque"}, ""},
		{ResponsesOutput{Type: "message", Summary: []ResponsesSummary{{Type: "summary_text", Text: "x"}}}, ""},
	} {
		before, _ := json.Marshal(tc.item)
		if got := ResponsesOutputReasoningText(tc.item); got != tc.want {
			t.Fatal(got, tc.want)
		}
		after, _ := json.Marshal(tc.item)
		if string(before) != string(after) {
			t.Fatal("mutated input")
		}
	}
}

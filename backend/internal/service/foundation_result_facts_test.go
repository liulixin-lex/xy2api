//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFoundationUsageEvidenceCarriers(t *testing.T) {
	raw := 12
	usage := OpenAIUsage{InputTokensPresent: true, CacheCreation5mTokensPresent: true, CacheCreation1hTokensPresent: true, CacheCreation5mTokens: 0, CacheCreation1hTokens: 0, UsageIncomplete: true, UsageInconsistent: true, RawPromptTokens: &raw, RawCacheReadTokens: &raw}
	result := OpenAIForwardResult{Usage: usage, ResponseFactsKnown: true, UsagePresent: true, UsageSource: "upstream_sse_terminal", TerminalSeen: true, TerminalValid: true, ResponseBodyKind: "sse"}
	if result.Usage.RawPromptTokens == nil || *result.Usage.RawPromptTokens != 12 || !result.Usage.CacheCreation5mTokensPresent || result.Usage.CacheCreationInputTokensPresent {
		t.Fatal(result)
	}
	encoded, err := json.Marshal(result.Usage)
	if err != nil || strings.Contains(string(encoded), "Present") || strings.Contains(string(encoded), "Raw") || strings.Contains(string(encoded), "Incomplete") || strings.Contains(string(encoded), "5m") {
		t.Fatal(string(encoded), err)
	}
	if (OpenAIForwardResult{}).ResponseFactsKnown || (ForwardResult{}).ResponseFactsKnown {
		t.Fatal("unknown results became known")
	}
}

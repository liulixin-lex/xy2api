//go:build unit

package service

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Request boundaries must retain the attempt context through detached streaming
// contexts. The passthrough writer must inject only after its ParsedRequest sync.
func TestL03ProductionRequestBoundaryRegistry(t *testing.T) {
	for _, name := range []string{"gateway_forward.go", "gateway_forward_as_chat_completions.go", "gateway_forward_as_responses.go"} {
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		text := string(raw)
		require.Contains(t, text, "ctx = s.beginCacheMetadataAttempt(ctx, c, parsed, account)")
	}
	raw, err := os.ReadFile("gateway_upstream_request.go")
	require.NoError(t, err)
	text := string(raw)
	require.Less(t, strings.Index(text, "body = s.applyCacheMetadata(ctx, c, account, body, enableMPT)"), strings.Index(text, "body = s.applyClaudeCacheFallback"))
	raw, err = os.ReadFile("gateway_anthropic_passthrough.go")
	require.NoError(t, err)
	text = string(raw)
	inject := strings.Index(text, "s.applyCacheMetadataPassthrough(")
	require.Greater(t, inject, strings.Index(text, "input.Parsed.ReplaceBody(wireBody)"))
	require.Less(t, inject, strings.Index(text, "s.providerTraceEngine.captureProviderCacheTrace("))
	raw, err = os.ReadFile("cache_anthropic_metadata.go")
	require.NoError(t, err)
	text = string(raw)
	require.NotContains(t, text, "ProviderCacheTraceSessionDigest(")
	require.NotContains(t, text, ".GenerateSessionHash(")
}

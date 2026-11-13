//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// FINAL §12 uses raw input buckets, before billing can rewrite cache usage.
func TestSchedUsagePreservesAllInputBuckets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage *OpenAIUsage
		want  *SchedAttemptUsage
	}{
		{"unknown", nil, nil},
		{"no input evidence", &OpenAIUsage{}, nil},
		{"uncached", &OpenAIUsage{InputTokens: 1000}, &SchedAttemptUsage{PromptTokens: 1000, InputTokens: 1000}},
		{"read and write", &OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 300, CacheCreationInputTokens: 100}, &SchedAttemptUsage{PromptTokens: 1000, InputTokens: 600, CacheReadTokens: 300, CacheCreationTokens: 100}},
		{"all cached", &OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 700, CacheCreationInputTokens: 300}, &SchedAttemptUsage{PromptTokens: 1000, CacheReadTokens: 700, CacheCreationTokens: 300}},
		{"cache evidence without total", &OpenAIUsage{CacheReadInputTokens: 300, CacheCreationInputTokens: 100}, &SchedAttemptUsage{CacheReadTokens: 300, CacheCreationTokens: 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, schedUsage(tc.usage))
		})
	}
}

func TestSchedCaptureUsageKeepsRawBucketsThroughTerminal(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.completed","response":{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":300,"cache_write_tokens":100}}}}`,
		`{"usage":{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":300,"cache_write_tokens":100}}}`,
	} {
		a, _ := newClassificationCapture()
		a.observe([]byte(payload))
		want := &SchedAttemptUsage{PromptTokens: 1000, InputTokens: 600, CacheReadTokens: 300, CacheCreationTokens: 100}
		require.Equal(t, want, a.usage)
		// A terminal result with no reported usage must not erase prior evidence.
		a.finish(&OpenAIForwardResult{}, nil)
		require.Equal(t, want, a.usage)
	}
	// Non-streaming forwarding supplies the same normalized usage on the result.
	a, _ := newClassificationCapture()
	a.finish(&OpenAIForwardResult{Usage: OpenAIUsage{
		InputTokens: 1000, CacheReadInputTokens: 300, CacheCreationInputTokens: 100,
	}}, nil)
	require.Equal(t, &SchedAttemptUsage{PromptTokens: 1000, InputTokens: 600, CacheReadTokens: 300, CacheCreationTokens: 100}, a.usage)
}

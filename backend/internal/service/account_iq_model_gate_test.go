package service

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestIQGateOnlyBlocksCanonicalModel(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"public": "gpt-6-astra"}}, Extra: map[string]any{"model_rate_limits": map[string]any{"iq:gpt-6-astra": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Minute).Format(time.RFC3339)}}}}
	require.True(t, a.isModelRateLimitedWithContext(nil, "public"))
	require.False(t, a.isModelRateLimitedWithContext(nil, "gpt-6.1-sol"))
}

//go:build unit

package resilience

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGeminiRecoveryHints(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		body string
		wait time.Duration
	}{
		{`{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"1200s"}]}}`, 20 * time.Minute},
		{`[{"error":{"code":429,"details":[{"metadata":{"quotaResetDelay":"1.5s"}}]}}]`, 1500 * time.Millisecond},
		{`{"error":{"message":"Please retry in 2.1s"}}`, 2100 * time.Millisecond},
		{`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"-1s"}]}}`, 0},
		{`{"request":{"error":{"details":[{"metadata":{"quotaResetDelay":"100h"}}]}}}`, 0},
		{`{"error":{"details":[{"@type":"wrong","retryDelay":"10s"}]}}`, 0},
	} {
		hint, ok := ParseUpstreamRecoveryHint(nil, []byte(tc.body), now)
		require.Equal(t, tc.wait > 0, ok, tc.body)
		require.Equal(t, tc.wait, hint.Delay)
	}
	hint, ok := ParseUpstreamRecoveryHint(http.Header{"Retry-After": {"60"}}, []byte(`{"error":{"details":[{"metadata":{"quotaResetDelay":"39s"}}]}}`), now)
	require.True(t, ok)
	require.Equal(t, time.Minute, hint.Delay)
}

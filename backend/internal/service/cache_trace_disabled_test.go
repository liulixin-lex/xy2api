//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// tools/cache-trace/check_disabled.py reruns this production-path matrix and
// TestCacheTraceDisabledPipelineSuccess with panic stubs at every block-chain
// entry, P0a JSON access, and (once wired) loss classifier. No product hook is
// needed and the stubs cannot add overhead to the ordinary executable.
func TestCacheTraceDisabledForwardEntryStub(t *testing.T) {
	svc := compatStreamService(nil, 2, 30, 1)
	require.False(t, svc.cfg.Gateway.CacheTrace.Enabled)
	require.False(t, svc.cfg.Gateway.OpenAICache.DiagnosticsEnabled)
	for _, tc := range traceForwardCases() {
		_, c, _, _, _ := traceForwardRun(t, tc, nil)
		require.Nil(t, cacheTraceFromGin(c))
	}
	for _, tc := range traceExtraCases() {
		c, _, _, _, _ := traceExtraRun(t, tc, nil, nil)
		require.Nil(t, cacheTraceFromGin(c))
	}
}

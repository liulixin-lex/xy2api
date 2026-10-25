//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// A disabled production request must never create a trace or an async job.
func TestCacheTraceDisabledForwardEntryStub(t *testing.T) {
	for _, tc := range traceForwardCases() {
		_, c, _, _, _ := traceForwardRun(t, tc, nil)
		require.Nil(t, cacheTraceFromGin(c))
	}
	for _, tc := range traceExtraCases() {
		c, _, _, _, _ := traceExtraRun(t, tc, nil, nil)
		require.Nil(t, cacheTraceFromGin(c))
	}
}

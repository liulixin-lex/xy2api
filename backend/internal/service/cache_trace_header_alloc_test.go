package service

import (
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestCacheTraceHeaderBitsNoAllocations(t *testing.T) {
	h := http.Header{}
	for _, name := range cacheTraceHeaders {
		h.Set(name, "private-header-marker")
	}
	require.Equal(t, uint16(1<<len(cacheTraceHeaders)-1), cacheTraceHeaderBits(h))
	require.Zero(t, testing.AllocsPerRun(1000, func() { _ = cacheTraceHeaderBits(h) }))
}
func BenchmarkCacheTraceHeaderBits(b *testing.B) {
	h := http.Header{"Session_id": {"private"}, "X-Codex-Turn-State": {"private"}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cacheTraceHeaderBits(h)
	}
}

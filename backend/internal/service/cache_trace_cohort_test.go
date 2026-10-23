package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheTraceSessionCohortPreservesExistingSampling(t *testing.T) {
	for _, n := range []int{0, 1, 16, 32, 63, 64, 65, 128} {
		for i := 0; i < 100; i++ {
			session := strings.Repeat(fmt.Sprintf("%x", i), n)
			if len(session) > n {
				session = session[:n]
			}
			require.Equal(t, sha256SumCacheCohort(session), cacheTraceSessionCohort(session))
		}
	}
	session := strings.Repeat("a", 64)
	require.Zero(t, testing.AllocsPerRun(1000, func() { _ = cacheTraceSessionCohort(session) }))
}

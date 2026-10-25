//go:build unit

package service

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestL01CohortDomainVectors(t *testing.T) {
	// Fixed vectors bind the rollout to its published domain. The trace helper
	// deliberately has no such prefix; changing it would rebucket observation.
	for _, tc := range []struct {
		name, session string
		want          int
	}{
		{name: "empty", want: 10},
		{name: "routing_v2", session: "v2:0123456789abcdef", want: 25},
		{name: "routing_legacy", session: "0123456789abcdef", want: 58},
		{name: "domain_in_session_is_data", session: "l01-cohort|v2:0123456789abcdef", want: 97},
		{name: "stack_last", session: strings.Repeat("s", 64), want: 12},
		{name: "fallback_first", session: strings.Repeat("s", 65), want: 58},
		{name: "utf8", session: "会话/é/🙂", want: 81},
		{name: "embedded_nul", session: "a\x00b", want: 96},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openAIUpstreamIdentityCohort(tc.session))
			traceDigest := sha256.Sum256([]byte(tc.session))
			require.Equal(t, int(traceDigest[0])*100/256, sha256SumCacheCohort(tc.session), "trace assignment must retain its original algorithm")
		})
	}
}

func TestL01CohortDomainAndBoundaryConsistency(t *testing.T) {
	// Exercise both the former compiler conversion boundary and our stack-to-
	// fallback boundary. The prefix must be present exactly once on both paths,
	// including SHA-256 padding/block boundaries after the 11-byte domain.
	for _, size := range []int{0, 1, 16, 19, 32, 33, 44, 45, 46, 52, 53, 54, 63, 64, 65, 116, 117, 118, 128, 1024, 65536} {
		t.Run(fmt.Sprintf("bytes=%d", size), func(t *testing.T) {
			seed := strings.Repeat("s", size)
			digest := sha256.Sum256([]byte("l01-cohort|" + seed))
			want := int(digest[0]) * 100 / 256
			require.Equal(t, want, openAIUpstreamIdentityCohort(seed))
			if size <= 64 {
				got := -1
				require.Zero(t, testing.AllocsPerRun(100, func() { got = openAIUpstreamIdentityCohort(seed) }), "normal routing identities must not allocate")
				require.Equal(t, want, got)
			}
		})
	}
}

func TestL01CohortIndependentOfTraceSampling(t *testing.T) {
	const keys = 100000
	deciles := make([][]int, 10)
	for row := range deciles {
		deciles[row] = make([]int, 10)
	}
	for i := 0; i < keys; i++ {
		// Deterministic, unique synthetic identities in the production v2 form.
		// Compare the real functions rather than two reimplemented references.
		session := fmt.Sprintf("v2:%016x", i)
		traceBucket := sha256SumCacheCohort(session)
		l01Bucket := openAIUpstreamIdentityCohort(session)
		require.GreaterOrEqual(t, traceBucket, 0)
		require.Less(t, traceBucket, 100)
		require.GreaterOrEqual(t, l01Bucket, 0)
		require.Less(t, l01Bucket, 100)
		deciles[traceBucket/10][l01Bucket/10]++
	}
	t.Run("all_deciles", func(t *testing.T) {
		// Pearson independence on a 10x10 table has 81 degrees of freedom.
		// 160 is a conservative regression threshold (tail probability < 1e-6),
		// not a claim that finite samples prove cryptographic independence.
		requireL01CohortIndependentTable(t, deciles, keys, 160)
	})
	t.Run("trace_10_percent_l01_50_percent", func(t *testing.T) {
		// Also check the operational failure mode directly: a 10% observation
		// sample must include both arms of a 50% L-01 rollout. The old shared
		// hash makes the sampled/control cell empty and fails this table.
		arms := [][]int{make([]int, 2), make([]int, 2)}
		for row := range deciles {
			for col, count := range deciles[row] {
				sampled, treatment := 0, 0
				if row == 0 {
					sampled = 1
				}
				if col < 5 {
					treatment = 1
				}
				arms[sampled][treatment] += count
			}
		}
		// One degree of freedom; 25 has tail probability about 5.7e-7.
		requireL01CohortIndependentTable(t, arms, keys, 25)
	})
}

func requireL01CohortIndependentTable(t *testing.T, counts [][]int, total, maxChiSquare int) {
	t.Helper()
	rows, cols := make([]int, len(counts)), make([]int, len(counts[0]))
	observed := 0
	for row := range counts {
		for col, count := range counts[row] {
			rows[row] += count
			cols[col] += count
			observed += count
		}
	}
	require.Equal(t, total, observed, "the full deterministic key corpus must be counted")
	chiSquare := 0.0
	for row := range counts {
		for col, count := range counts[row] {
			// Use observed marginals: the 256-to-100 bucket mapping is slightly
			// nonuniform, which must not be mistaken for dependence.
			expected := float64(rows[row]) * float64(cols[col]) / float64(total)
			require.GreaterOrEqual(t, expected, 5.0, "Pearson approximation requires populated cells")
			delta := float64(count) - expected
			chiSquare += delta * delta / expected
		}
	}
	t.Logf("L01_COHORT_INDEPENDENCE keys=%d table=%v chi_square=%.6f threshold=%d", total, counts, chiSquare, maxChiSquare)
	require.Less(t, chiSquare, float64(maxChiSquare), "L-01 cohort must not be coupled to trace sampling")
}

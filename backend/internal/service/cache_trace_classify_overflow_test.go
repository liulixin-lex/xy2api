package service

import (
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestCacheTraceDensityOverflowKeepsValidSentinel(t *testing.T) {
	in := cacheTraceClassificationInput{UsageObserved: true, UsagePromptPresent: true,
		UsageFields: 1, ChainSampled: true, PromptTokens: math.MaxInt / 2, BytesOut: 1,
		ForkRef: "none", ForkPrecision: "none", ForkKind: "none"}
	got := classifyCacheTrace(in, 1024)
	require.Equal(t, -1, got.TokensPerByteMilli)
	require.Equal(t, "cold", got.LossClass)
}

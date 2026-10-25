package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCachePolicyMaxStaleSecondsDefaultAndRange(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 900, cfg.Gateway.CachePolicy.MaxStaleSeconds)
	for _, value := range []int{0, 60, 900, 86400} {
		cfg.Gateway.CachePolicy.MaxStaleSeconds = value
		require.NoError(t, cfg.Validate(), value)
	}
	for _, value := range []int{-1, 1, 59, 86401} {
		cfg.Gateway.CachePolicy.MaxStaleSeconds = value
		err = cfg.Validate()
		require.ErrorContains(t, err, "gateway.cache_policy.max_stale_seconds", value)
	}
}

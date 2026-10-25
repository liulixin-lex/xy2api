package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheRoutingL07CapsDefaultsAndRanges(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, CacheRoutingConfig{L07MaxConsecutiveCap: 5, L07MaxHoldSecondsCap: 300, L07GroupKeepRatioCapPct: 20, L07GuardErrorDeltaPP: 5, L07GuardPauseSeconds: 600}, cfg.Gateway.CacheRouting)

	for _, tc := range []struct {
		name   string
		mutate func(*CacheRoutingConfig)
		ok     bool
	}{
		{"zero keeps defaults", func(c *CacheRoutingConfig) { *c = CacheRoutingConfig{} }, true},
		{"lowered caps", func(c *CacheRoutingConfig) {
			c.L07MaxConsecutiveCap, c.L07MaxHoldSecondsCap, c.L07GroupKeepRatioCapPct = 1, 10, 1
		}, true},
		{"consecutive above contract", func(c *CacheRoutingConfig) { c.L07MaxConsecutiveCap = 6 }, false},
		{"hold below contract", func(c *CacheRoutingConfig) { c.L07MaxHoldSecondsCap = 5 }, false},
		{"ratio above contract", func(c *CacheRoutingConfig) { c.L07GroupKeepRatioCapPct = 21 }, false},
		{"guard delta", func(c *CacheRoutingConfig) { c.L07GuardErrorDeltaPP = 101 }, false},
		{"guard pause", func(c *CacheRoutingConfig) { c.L07GuardPauseSeconds = 30 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load()
			require.NoError(t, err)
			tc.mutate(&cfg.Gateway.CacheRouting)
			err = cfg.Validate()
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "gateway.cache_routing.")
			}
		})
	}
}

//go:build unit

package repository

import (
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedQualityPolicyDoesNotReadLegacyFields(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			raw := fmt.Sprintf(`{"version":9,"groups":{"7":{"enabled":%t,"max_error_rate":0.9}}}`, enabled)
			before := raw
			policies, version, err := decodeSchedQualityPolicies(raw)
			require.NoError(t, err)
			require.Equal(t, int64(9), version)
			record := policies[7]
			require.Equal(t, int64(7), record.GroupID)
			require.Equal(t, version, record.Version)
			require.Equal(t, schedquality.DefaultPolicy(), record.Policy)
			require.Equal(t, before, raw, "conversion never rewrites persisted input")
		})
	}
}

func TestSchedQualityPolicyReadCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want schedquality.Policy
	}{
		{
			name: "legacy error line is raised to minimum",
			raw:  `{"version":4,"groups":{"7":{"v4":{"enabled":true,"ttft_seconds":12,"max_error_rate":0.01,"min_cache_ratio":0.8,"takeover_idle_seconds":0}}}}`,
			want: schedquality.Policy{Enabled: true, TTFTSeconds: 12, MaxErrorRate: .05, MinCacheRatio: .8, TakeoverIdleSeconds: 0},
		},
		{
			name: "malformed field preserves valid siblings",
			raw:  `{"version":4,"groups":{"7":{"v4":{"enabled":"bad","ttft_seconds":12,"max_error_rate":0.8,"min_cache_ratio":0.8,"takeover_idle_seconds":0}}}}`,
			want: schedquality.Policy{TTFTSeconds: 12, MaxErrorRate: .8, MinCacheRatio: .8, TakeoverIdleSeconds: 0},
		},
		{
			name: "invalid numeric fields are retained",
			raw:  `{"version":4,"groups":{"7":{"v4":{"enabled":true,"ttft_seconds":-1,"max_error_rate":0.8,"min_cache_ratio":1.2,"takeover_idle_seconds":300}}}}`,
			want: schedquality.Policy{Enabled: true, TTFTSeconds: -1, MaxErrorRate: .8, MinCacheRatio: 1.2, TakeoverIdleSeconds: 300},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policies, version, err := decodeSchedQualityPolicies(tc.raw)
			require.NoError(t, err)
			require.Equal(t, int64(4), version)
			require.Equal(t, tc.want, policies[7].Policy)
		})
	}
}

//go:build unit

package repository

import (
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedQualityPolicyPreV3Defaults(t *testing.T) {
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
			require.True(t, record.FromV3)
			require.Equal(t, schedquality.Policy{Enabled: enabled, TTFTSeconds: 15, MaxErrorRate: .10, MinCacheRatio: .50, TakeoverIdleSeconds: 0}, record.Policy)
			require.Equal(t, before, raw, "conversion never rewrites persisted input")
		})
	}
}

package repository

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupQualityV3PolicyStoragePreservesLegacyBytes(t *testing.T) {
	raw := `{"version":9,"groups":{"7":{"enabled":true,"max_error_rate":0.1700,"max_warm_ttft_p90_ms":31000,"fast_warm_ttft_p90_ms":9000,"pinned_account_ids":[9, 1, 9],"unknown": { "keep": true }},"8":{"enabled":false,"pinned_account_ids":null}},"extra": [ 1, 2 ]}`
	var current, next service.GroupQualityGatePolicies
	require.NoError(t, json.Unmarshal([]byte(raw), &current))
	require.NoError(t, json.Unmarshal([]byte(raw), &next))
	next.Version++
	p := next.Groups["7"]
	p.V3 = &service.GroupQualityGateV3Policy{MaxTTFTP90Seconds: 15, MaxErrorRate: .1, MinCacheKeepRate: .5}
	p.Enabled, p.PinnedAccountIDs = false, []int64{999}
	next.Groups["7"] = p
	next.Groups["9"] = service.GroupQualityGatePolicy{Enabled: true, V3: p.V3}
	saved, err := patchGroupQualityPolicies(raw, current, next)
	require.NoError(t, err)
	for _, fragment := range []string{`"max_error_rate":0.1700`, `"pinned_account_ids":[9, 1, 9]`, `"unknown": { "keep": true }`, `"8":{"enabled":false,"pinned_account_ids":null}`, `"extra": [ 1, 2 ]`} {
		require.Contains(t, saved, fragment)
	}
	var result service.GroupQualityGatePolicies
	require.NoError(t, json.Unmarshal([]byte(saved), &result))
	require.True(t, result.Groups["7"].Enabled)
	require.Equal(t, []int64{9, 1, 9}, result.Groups["7"].PinnedAccountIDs)
	require.False(t, result.Groups["9"].Enabled)
	require.Equal(t, 10, result.Version)
	var old struct {
		Groups map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"groups"`
	}
	require.NoError(t, json.Unmarshal([]byte(saved), &old))
	require.False(t, old.Groups["9"].Enabled)
	// Missing groups must become a numeric-keyed object, never an array.
	saved, err = patchGroupQualityPolicies(`{"version":0}`, service.GroupQualityGatePolicies{}, service.GroupQualityGatePolicies{Version: 1, Groups: map[string]service.GroupQualityGatePolicy{"7": p}})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(saved), &result))
	require.NotNil(t, result.Groups["7"].V3)
}

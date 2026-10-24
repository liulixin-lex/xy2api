//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCachePolicyM31LegacyFillWithoutWriteOrRevisionChange(t *testing.T) {
	repo := newPolicyTestStore()
	const raw = `{"rev":9,"spec":{"mode":"off","account_ids":[],"rollout_percent":0}}`
	repo.rows[cachePolicySettings[3]] = raw
	s := NewCachePolicyService(repo, policyTestConfig())
	require.NoError(t, s.Refresh(context.Background()))
	p := s.Snapshot().UpstreamIdentity()
	require.Equal(t, 2, p.MaxInflightPerKey)
	require.EqualValues(t, 9, p.Revision())
	view, err := s.Get(context.Background(), "upstream_identity")
	require.NoError(t, err)
	require.Equal(t, 2, view.Stored.Spec["max_inflight_per_key"])
	require.Equal(t, raw, repo.rows[cachePolicySettings[3]], "read fills in memory only")
	v, err := s.Validate(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 9, `{"max_inflight_per_key":4}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, []CachePolicyDiff{{Field: "max_inflight_per_key", Before: 2, After: 4}}, v.Diff)
	require.Equal(t, "enable", v.Direction)
	require.Equal(t, raw, repo.rows[cachePolicySettings[3]])
}

func TestCachePolicyM31InflightRangeDirectionAndHistory(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	for _, tc := range []struct {
		value           string
		valid           bool
		code, direction string
	}{
		{"1", true, "", "disable"}, {"2", true, "", "disable"}, {"16", true, "", "enable"},
		{"0", false, "out_of_range", "disable"}, {"17", false, "out_of_range", "disable"},
		{"null", false, "malformed_body", "disable"}, {"1.5", false, "malformed_body", "disable"}, {`"2"`, false, "malformed_body", "disable"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			v, err := s.Validate(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, `{"max_inflight_per_key":`+tc.value+`}`), nil)
			require.NoError(t, err)
			require.Equal(t, tc.valid, v.Valid)
			require.Equal(t, tc.direction, v.Direction)
			if !tc.valid {
				require.Empty(t, v.Diff)
				require.Equal(t, "max_inflight_per_key", v.Errors[0].Field)
				require.Equal(t, tc.code, v.Errors[0].Code)
				if tc.code == "out_of_range" {
					require.Equal(t, map[string]int{"min": 1, "max": 16}, v.Errors[0].Limit)
				}
			}
		})
	}
	require.True(t, cachePolicyHistoryField("upstream_identity", "max_inflight_per_key"))
}

func TestCachePolicyM31AllAccountsWarningIgnoresMasterAndRollout(t *testing.T) {
	for _, master := range []bool{false, true} {
		for _, rollout := range []int{0, 100} {
			for _, ids := range []string{"[]", "[101]"} {
				repo := newPolicyTestStore()
				b, err := json.Marshal(map[string]any{"rev": 1, "spec": map[string]any{"enabled": master}})
				require.NoError(t, err)
				repo.rows[cachePolicySettings[0]] = string(b)
				var scope []int64
				require.NoError(t, json.Unmarshal([]byte(ids), &scope))
				b, err = json.Marshal(map[string]any{"rev": 7, "spec": map[string]any{"mode": "uuid_fill", "rollout_percent": rollout, "account_ids": scope}})
				require.NoError(t, err)
				repo.rows[cachePolicySettings[3]] = string(b)
				s := NewCachePolicyService(repo, policyTestConfig())
				require.NoError(t, s.Refresh(context.Background()))
				view, err := s.Get(context.Background(), "upstream_identity")
				require.NoError(t, err)
				if ids == "[]" {
					require.Contains(t, view.Effective.Warnings, "l01_all_accounts_unverified")
				} else {
					require.NotContains(t, view.Effective.Warnings, "l01_all_accounts_unverified")
				}
			}
		}
	}
}

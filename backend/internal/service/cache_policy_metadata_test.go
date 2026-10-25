//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type l03PolicyAccounts struct {
	rows         []Account
	gets, lists  int
	requested    []int64
	fail, forbid bool
}

func (r *l03PolicyAccounts) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	if r.forbid {
		panic("disable account query")
	}
	r.gets++
	r.requested = append(r.requested, ids...)
	if r.fail {
		return nil, errors.New("PRIVATE_ACCOUNT_ERROR")
	}
	out := []*Account{}
	for i := range r.rows {
		for _, id := range ids {
			if r.rows[i].ID == id {
				out = append(out, &r.rows[i])
			}
		}
	}
	return out, nil
}
func (r *l03PolicyAccounts) ListWithFilters(_ context.Context, p pagination.PaginationParams, platform, kind, _, _ string, _ int64, _ string) ([]Account, *pagination.PaginationResult, error) {
	if r.forbid {
		panic("disable account candidates")
	}
	r.lists++
	if r.fail {
		return nil, nil, errors.New("PRIVATE_ACCOUNT_ERROR")
	}
	out := []Account{}
	for _, a := range r.rows {
		if a.Platform == platform && a.Type == kind {
			out = append(out, a)
		}
	}
	start := min(p.Offset(), len(out))
	end := min(start+p.Limit(), len(out))
	return out[start:end], &pagination.PaginationResult{Total: int64(len(out))}, nil
}
func l03PolicyAccountRows() []Account {
	return []Account{
		{ID: 501, Name: "relay-claude-a", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}},
		{ID: 502, Name: "relay-claude-b", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay2.invalid"}},
		{ID: 503, Name: "official", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://API.Anthropic.com:443"}},
		{ID: 504, Name: "oauth", Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{ID: 505, Name: "other", Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}
}

func TestCachePolicyL03AccountValidationAndDirection(t *testing.T) {
	for _, tc := range []struct{ patch, code string }{
		{`[999]`, "unknown_account"}, {`[505]`, "wrong_platform"}, {`[503]`, "official_endpoint"}, {`[504]`, "not_apikey"}, {`[501,501]`, "duplicate"}, {`[501]`, ""},
	} {
		t.Run(tc.patch, func(t *testing.T) {
			repo := &l03PolicyAccounts{rows: l03PolicyAccountRows()}
			s, _, _ := m4PolicyFixture(t, WithCachePolicyAccounts(repo))
			v, err := s.Validate(context.Background(), "anthropic_identity", policyWrite(t, fmt.Sprintf(`{"expected_rev":0,"spec":{"metadata_account_ids":%s}}`, tc.patch)), nil)
			require.NoError(t, err)
			require.Equal(t, tc.code == "", v.Valid)
			if tc.code != "duplicate" {
				require.Equal(t, "enable", v.Direction)
			}
			if tc.code != "" {
				require.Contains(t, v.Errors, CachePolicyFieldError{Field: "metadata_account_ids", Code: tc.code})
			}
			if tc.code == "duplicate" {
				require.Empty(t, v.Diff)
				require.Zero(t, repo.gets)
			} else {
				require.Len(t, v.Diff, 1)
			}
		})
	}
}

func TestCachePolicyL03OnlyNewAccountIDsValidated(t *testing.T) {
	repo := &l03PolicyAccounts{rows: l03PolicyAccountRows()}
	s, store, _ := m4PolicyFixture(t, WithCachePolicyAccounts(repo))
	old := `{"rev":7,"spec":{"metadata_account_ids":[999],"anchor_group_ids":[31],"rollout_percent":50}}`
	store.rows["cache_anthropic_identity_policy"] = old
	require.NoError(t, s.Refresh(context.Background()))
	v, err := s.Validate(context.Background(), "anthropic_identity", policyWrite(t, `{"expected_rev":7,"spec":{"metadata_account_ids":[999,501]}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, []int64{501}, repo.requested)
	repo.forbid = true
	v, err = s.Validate(context.Background(), "anthropic_identity", policyWrite(t, `{"expected_rev":7,"spec":{"metadata_account_ids":[]}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, "disable", v.Direction)
	require.False(t, v.Confirm.ReasonRequired)
}

func TestCachePolicyL03DisableAndWarningScope(t *testing.T) {
	repo := &l03PolicyAccounts{rows: l03PolicyAccountRows()}
	s, store, groups := m4PolicyFixture(t, WithCachePolicyAccounts(repo), WithCacheAnthropicObservability())
	store.rows["cache_anthropic_identity_policy"] = `{"rev":1,"spec":{"anchor_group_ids":[31],"metadata_group_ids":[31],"metadata_account_ids":[501],"rollout_percent":50,"metadata_rollout_percent":20}}`
	require.NoError(t, s.Refresh(context.Background()))
	repo.fail = true
	view, err := s.Get(context.Background(), "anthropic_identity")
	require.NoError(t, err)
	require.Equal(t, []string{cachePolicyAccountOptionsUnavailable}, view.Effective.Warnings)
	for _, field := range view.Schema {
		if field["ref"] == "group" {
			require.NotEmpty(t, field["options"])
		} else if field["field"] == "metadata_account_ids" {
			require.Empty(t, field["options"])
		}
	}
	repo.fail = false
	groups.failPage = 1
	view, err = s.Get(context.Background(), "anthropic_identity")
	require.NoError(t, err)
	require.Equal(t, []string{cachePolicyGroupOptionsUnavailable}, view.Effective.Warnings)
	for _, field := range view.Schema {
		if field["ref"] == "group" {
			require.Empty(t, field["options"])
		} else if field["field"] == "metadata_account_ids" {
			require.Len(t, field["options"], 2)
		}
	}
	beforeGets, beforeLists, beforeGroups := repo.gets, repo.lists, groups.calls
	repo.forbid = true
	groups.forbid = true
	view, _, err = s.Update(context.Background(), "anthropic_identity", policyWrite(t, `{"expected_rev":1,"spec":{"metadata_account_ids":[]}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, []string{cachePolicyAccountOptionsUnavailable, cachePolicyGroupOptionsUnavailable}, view.Effective.Warnings)
	require.Equal(t, beforeGets, repo.gets)
	require.Equal(t, beforeLists, repo.lists)
	require.Equal(t, beforeGroups, groups.calls)
}

func TestCachePolicyL03DefaultsCapabilityHistoryAndPublication(t *testing.T) {
	repo := &l03PolicyAccounts{forbid: true}
	s, store, _ := m4PolicyFixture(t, WithCachePolicyAccounts(repo), WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L04: true}))
	raw := `{"rev":2,"spec":{"anchor_group_ids":[31],"rollout_percent":50}}`
	store.rows["cache_anthropic_identity_policy"] = raw
	require.NoError(t, s.Refresh(context.Background()))
	require.Equal(t, raw, store.rows["cache_anthropic_identity_policy"])
	require.Equal(t, []int64{}, s.Snapshot().entries[4].envelope.Spec["metadata_account_ids"])
	require.Zero(t, repo.gets)
	require.Zero(t, repo.lists)
	require.True(t, cachePolicyHistoryField("anthropic_identity", "metadata_account_ids"))
	v, err := s.Validate(context.Background(), "anthropic_identity", policyWrite(t, `{"expected_rev":2,"spec":{"metadata_account_ids":[501]}}`), nil)
	require.NoError(t, err)
	require.False(t, v.Valid)
	require.Contains(t, v.Errors, CachePolicyFieldError{Field: "metadata_account_ids", Code: "unsupported_in_build"})
}

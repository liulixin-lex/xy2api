//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type cacheOptionPageRepo struct {
	rows      []Account
	pages     []pagination.PaginationParams
	errPage   int
	panicList bool
	t         *testing.T
}

func (r *cacheOptionPageRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
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

func (r *cacheOptionPageRepo) ListWithFilters(_ context.Context, p pagination.PaginationParams, platform, kind, status, search string, groupID int64, privacy string) ([]Account, *pagination.PaginationResult, error) {
	if r.panicList {
		panic("account option lookup entered request snapshot path")
	}
	require.Equal(r.t, PlatformOpenAI, platform)
	require.Equal(r.t, AccountTypeAPIKey, kind)
	require.Empty(r.t, status, "inactive/unschedulable accounts remain configuration candidates")
	require.Empty(r.t, search)
	require.Zero(r.t, groupID)
	require.Empty(r.t, privacy)
	require.Equal(r.t, "id", p.SortBy)
	require.Equal(r.t, pagination.SortOrderAsc, p.SortOrder)
	require.Equal(r.t, 500, p.PageSize)
	r.pages = append(r.pages, p)
	if p.Page == r.errPage {
		return nil, nil, errors.New("synthetic account options read failure")
	}
	start := min(p.Offset(), len(r.rows))
	end := min(start+p.Limit(), len(r.rows))
	return r.rows[start:end], &pagination.PaginationResult{Total: int64(len(r.rows))}, nil
}

func TestCachePolicyAccountOptionsEligibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  *Account
		want bool
	}{
		{name: "nil"},
		{name: "apikey", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, want: true},
		{name: "inactive", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: "disabled", Schedulable: false}, want: true},
		{name: "oauth", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
		{name: "setup", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}},
		{name: "grok", row: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}},
		{name: "anthropic", row: &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}},
		{name: "implicit_platform", row: &Account{Type: AccountTypeAPIKey}},
		{name: "passthrough", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_passthrough": true}}},
		{name: "legacy_passthrough", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_oauth_passthrough": true}}},
		{name: "explicit_false_overrides_legacy", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_passthrough": false, "openai_oauth_passthrough": true}}, want: true},
		{name: "malformed_passthrough_uses_existing_accessor", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_passthrough": "true"}}, want: true},
		// The existing fingerprint accessor is explicitly OAuth-only. Do not
		// invent a stricter apikey rule by inspecting raw extra fields.
		{name: "apikey_fingerprint_field_is_inert", row: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"codex_fingerprint_mode": "full"}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, cachePolicyUpstreamAccountEligible(tc.row)) })
	}
}

func TestCachePolicyAccountOptionsFilteredPagination(t *testing.T) {
	for _, tc := range []struct{ excluded, eligible, pages, want int }{
		{0, 0, 1, 0}, {0, 1, 1, 1}, {0, 500, 1, 500}, {0, 501, 1, 500},
		{600, 1, 2, 1}, {600, 501, 3, 500},
	} {
		t.Run(fmt.Sprintf("excluded_%d/eligible_%d", tc.excluded, tc.eligible), func(t *testing.T) {
			repo := &cacheOptionPageRepo{t: t}
			for i := 1; i <= tc.excluded+tc.eligible; i++ {
				a := Account{ID: int64(i), Name: fmt.Sprintf("synthetic-%d", i), Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: "disabled"}
				if i <= tc.excluded {
					a.Extra = map[string]any{"openai_passthrough": true}
				}
				repo.rows = append(repo.rows, a)
			}
			s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(repo))
			options, err := s.upstreamIdentityAccountOptions(context.Background())
			require.NoError(t, err)
			require.NotNil(t, options, "zero candidates encode as [], never null")
			require.Len(t, options, tc.want)
			require.Len(t, repo.pages, tc.pages)
			for i, option := range options {
				require.EqualValues(t, tc.excluded+i+1, option.ID)
				require.Equal(t, PlatformOpenAI, option.Platform)
			}
		})
	}
}

func TestCachePolicyAccountOptionsViewsAndPrivacy(t *testing.T) {
	repo := &cacheOptionPageRepo{t: t, rows: []Account{
		{ID: 102, Name: "inactive-choice", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: "disabled", Credentials: map[string]any{"api_key": "synthetic-credential-never-expose"}, Extra: map[string]any{"private": "synthetic-extra-never-expose"}},
		{ID: 101, Name: "first-choice", Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}}
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(repo))
	ctx := context.Background()
	get, err := s.Get(ctx, "upstream_identity")
	require.NoError(t, err)
	list, err := s.List(ctx, "synthetic-options")
	require.NoError(t, err)
	put, _, err := s.Update(ctx, "upstream_identity", upstreamPolicyWrite(t, 0, `{"account_ids":[101]}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	for _, view := range []CachePolicyView{get, list.Policies[3], put} {
		encoded, err := json.Marshal(view.Schema[1])
		require.NoError(t, err)
		require.JSONEq(t, `{"field":"account_ids","type":"id_list","ref":"account","options":[{"id":101,"name":"first-choice","platform":"openai"},{"id":102,"name":"inactive-choice","platform":"openai"}]}`, string(encoded))
		require.NotContains(t, string(encoded), "synthetic-credential-never-expose")
		require.NotContains(t, string(encoded), "synthetic-extra-never-expose")
		require.NotContains(t, string(encoded), "is_fallback")
	}
}

func TestCachePolicyAccountOptionsReadFailureDoesNotCommit(t *testing.T) {
	repo := &cacheOptionPageRepo{t: t, errPage: 1}
	store := newPolicyTestStore()
	s := NewCachePolicyService(store, policyTestConfig(), WithCachePolicyAccounts(repo))
	before := s.Snapshot()
	_, err := s.Get(context.Background(), "upstream_identity")
	require.ErrorContains(t, err, "synthetic account options read failure")
	_, err = s.List(context.Background(), "synthetic-options")
	require.Error(t, err)
	_, _, err = s.Update(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill"}`), nil, CachePolicyActor{})
	require.Error(t, err)
	require.Same(t, before, s.Snapshot())
	require.Empty(t, store.rows, "failed options lookup must not commit a policy")
	// Invalid values retain their field errors without querying options first.
	calls := len(repo.pages)
	_, _, err = s.Update(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, `{"header_only_uuid":true}`), nil, CachePolicyActor{})
	var policyErr *CachePolicyError
	require.ErrorAs(t, err, &policyErr)
	require.Equal(t, "CACHE_POLICY_UNSUPPORTED", policyErr.Reason)
	require.Len(t, repo.pages, calls)
}

func TestCachePolicyAccountOptionsStayOffRequestPath(t *testing.T) {
	repo := &cacheOptionPageRepo{t: t, panicList: true}
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(repo))
	require.NotPanics(t, func() {
		require.Zero(t, testing.AllocsPerRun(1000, func() {
			p := s.Snapshot().UpstreamIdentity()
			if p.Enabled() || !p.AllowsAccount(101) {
				panic("default request policy changed")
			}
		}))
	})
	require.Empty(t, repo.pages)
}

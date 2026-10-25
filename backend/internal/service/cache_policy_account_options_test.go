//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type cacheOptionPageRepo struct {
	rows      []Account
	getCalls  int
	getErr    error
	panicGet  bool
	pages     []pagination.PaginationParams
	errPage   int
	panicList bool
	t         *testing.T
}

func (r *cacheOptionPageRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.getCalls++
	if r.panicGet {
		panic("account validation must not run for rollback")
	}
	if r.getErr != nil {
		return nil, r.getErr
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

func (r *cacheOptionPageRepo) ListWithFilters(_ context.Context, p pagination.PaginationParams, platform, kind, status, search string, groupID int64, privacy string) ([]Account, *pagination.PaginationResult, error) {
	if r.panicList {
		panic("account option lookup entered request snapshot path")
	}
	if platform == PlatformGrok {
		// L-17 grok.account_ids candidates: one Grok-only query, all types.
		require.Empty(r.t, kind)
		require.Empty(r.t, status)
		require.Equal(r.t, "id", p.SortBy)
		require.Equal(r.t, 500, p.PageSize)
		grok := []Account{}
		for _, row := range r.rows {
			if row.Platform == PlatformGrok {
				grok = append(grok, row)
			}
		}
		return grok, &pagination.PaginationResult{Total: int64(len(grok))}, nil
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
		return nil, nil, errors.New("ACCOUNT_OPTIONS_SECRET_ERROR_MARKER")
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
	put, _, err := s.Update(ctx, "upstream_identity", upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","account_ids":[101]}`), nil, CachePolicyActor{})
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

func TestCachePolicyAccountOptionsReadFailureDegrades(t *testing.T) {
	for _, failedPage := range []int{1, 2} {
		for _, operation := range []string{"get", "list", "enable_put"} {
			t.Run(fmt.Sprintf("page_%d/%s", failedPage, operation), func(t *testing.T) {
				var captured bytes.Buffer
				previousLogger := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, nil)))
				t.Cleanup(func() { slog.SetDefault(previousLogger) })
				repo := &cacheOptionPageRepo{t: t, errPage: failedPage}
				// Page one includes a real eligible result, but not enough to
				// reach the cap. A page-two error must discard that partial list.
				for id := 1; id <= 500; id++ {
					account := Account{ID: int64(id), Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
					if id != 101 {
						account.Extra = map[string]any{"openai_passthrough": true}
					}
					repo.rows = append(repo.rows, account)
				}
				store := newPolicyTestStore()
				store.rows[cachePolicySettings[0]] = `{"rev":1,"spec":{"enabled":true}}`
				mode := "uuid_fill"
				if operation == "enable_put" {
					mode = "off"
				}
				store.rows[cachePolicySettings[3]] = fmt.Sprintf(`{"rev":3,"spec":{"mode":%q,"account_ids":[101],"rollout_percent":50}}`, mode)
				s := NewCachePolicyService(store, policyTestConfig(), WithCachePolicyAccounts(repo))
				require.NoError(t, s.Refresh(context.Background()))
				before := s.Snapshot()
				stored := store.rows[cachePolicySettings[3]]
				var view CachePolicyView
				var err error
				switch operation {
				case "get":
					view, err = s.Get(context.Background(), "upstream_identity")
				case "list":
					var list CachePolicyList
					list, err = s.List(context.Background(), "synthetic-options")
					require.Len(t, list.Policies, 6)
					view = list.Policies[3]
					for _, policy := range list.Policies {
						if policy.Key != "upstream_identity" {
							require.NotContains(t, policy.Effective.Warnings, cachePolicyAccountOptionsUnavailable)
						}
					}
				case "enable_put":
					view, _, err = s.Update(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 3, `{"mode":"uuid_fill"}`), nil, CachePolicyActor{})
				}
				require.NoError(t, err)
				require.Equal(t, []cachePolicyAccountOption{}, view.Schema[1]["options"])
				require.Equal(t, []string{"observability_off", cachePolicyAccountOptionsUnavailable}, view.Effective.Warnings)
				require.Equal(t, "on", view.Effective.State)
				require.True(t, *view.Effective.Active)
				require.Empty(t, view.Effective.Blockers)
				require.Len(t, repo.pages, failedPage)
				if operation == "enable_put" {
					require.EqualValues(t, 4, view.Stored.Rev)
					require.EqualValues(t, 4, s.Snapshot().UpstreamIdentity().Revision())
					require.NotEqual(t, stored, store.rows[cachePolicySettings[3]])
					require.Equal(t, 1, repo.getCalls, "enable keeps mandatory account validation")
				} else {
					require.EqualValues(t, 3, view.Stored.Rev)
					require.Same(t, before, s.Snapshot())
					require.Equal(t, stored, store.rows[cachePolicySettings[3]])
					require.Zero(t, repo.getCalls)
				}
				encoded, err := json.Marshal(view)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "ACCOUNT_OPTIONS_SECRET_ERROR_MARKER")
				require.NotContains(t, captured.String(), "ACCOUNT_OPTIONS_SECRET_ERROR_MARKER")
				// Picker degradation is response-only: preview and snapshots do
				// not acquire this warning, and invalid input still rejects.
				preview, err := s.Validate(context.Background(), "upstream_identity", upstreamPolicyWrite(t, int(view.Stored.Rev), `{"mode":"off"}`), nil)
				require.NoError(t, err)
				require.NotContains(t, preview.Warnings, cachePolicyAccountOptionsUnavailable)
				queries := len(repo.pages)
				_, _, err = s.Update(context.Background(), "upstream_identity", upstreamPolicyWrite(t, int(view.Stored.Rev), `{"header_only_uuid":true}`), nil, CachePolicyActor{})
				var policyErr *CachePolicyError
				require.ErrorAs(t, err, &policyErr)
				require.Equal(t, "CACHE_POLICY_UNSUPPORTED", policyErr.Reason)
				require.Len(t, repo.pages, queries)
			})
		}
	}
}

func TestCachePolicyAccountOptionsEmptyHasNoUnavailableWarning(t *testing.T) {
	repo := &cacheOptionPageRepo{t: t}
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(repo))
	view, err := s.Get(context.Background(), "upstream_identity")
	require.NoError(t, err)
	require.Equal(t, []cachePolicyAccountOption{}, view.Schema[1]["options"])
	require.NotContains(t, view.Effective.Warnings, cachePolicyAccountOptionsUnavailable)
}

func TestCachePolicyAccountOptionsStayOffRequestPath(t *testing.T) {
	repo := &cacheOptionPageRepo{t: t, panicList: true, panicGet: true}
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
	require.Zero(t, repo.getCalls)
}

func TestCachePolicyAccountOptionsDisableSkipsAllAccountQueries(t *testing.T) {
	for _, fault := range []string{"error", "panic"} {
		for _, tc := range []struct{ name, priorIDs, spec string }{
			{"mode_off", "[101,102]", `{"mode":"off"}`},
			{"lower_rollout", "[101,102]", `{"rollout_percent":10}`},
			{"narrow_scope", "[101,102]", `{"account_ids":[101]}`},
			{"all_to_subset", "[]", `{"account_ids":[999]}`},
			{"whole_object_off", "[101,102]", `{"mode":"off","account_ids":[101,102],"rollout_percent":50}`},
			{"unchanged", "[101,102]", `{"rollout_percent":50}`},
		} {
			t.Run(fault+"/"+tc.name, func(t *testing.T) {
				repo := &cacheOptionPageRepo{t: t, getErr: errors.New("synthetic validation repository unavailable"), errPage: 1,
					panicGet: fault == "panic", panicList: fault == "panic"}
				store := newPolicyTestStore()
				store.rows[cachePolicySettings[3]] = fmt.Sprintf(`{"rev":7,"spec":{"mode":"uuid_fill","account_ids":%s,"rollout_percent":50}}`, tc.priorIDs)
				s := NewCachePolicyService(store, policyTestConfig(), WithCachePolicyAccounts(repo))
				require.NoError(t, s.Refresh(context.Background()))
				request := policyWrite(t, fmt.Sprintf(`{"expected_rev":7,"spec":%s}`, tc.spec))
				require.NotPanics(t, func() {
					validation, err := s.Validate(context.Background(), "upstream_identity", request, nil)
					require.NoError(t, err)
					require.True(t, validation.Valid)
					require.Equal(t, "disable", validation.Direction)
					require.NotContains(t, validation.Warnings, cachePolicyAccountOptionsUnavailable)
					require.False(t, validation.Confirm.ReasonRequired)
					require.False(t, validation.Confirm.CheckboxRequired)
					view, diff, err := s.Update(context.Background(), "upstream_identity", request, nil, CachePolicyActor{})
					require.NoError(t, err)
					require.EqualValues(t, 8, view.Stored.Rev)
					require.Equal(t, validation.Diff, diff)
					require.Equal(t, []cachePolicyAccountOption{}, view.Schema[1]["options"])
					require.Equal(t, cachePolicyAccountOptionsUnavailable, view.Effective.Warnings[len(view.Effective.Warnings)-1])
					require.EqualValues(t, 8, s.Snapshot().UpstreamIdentity().Revision())
				})
				require.Zero(t, repo.getCalls)
				require.Empty(t, repo.pages)
			})
		}
	}
}

func TestCachePolicyAccountOptionsDisableRetainsFormatAndRevisionChecks(t *testing.T) {
	repo := &cacheOptionPageRepo{t: t, panicGet: true, panicList: true}
	store := newPolicyTestStore()
	store.rows[cachePolicySettings[3]] = `{"rev":7,"spec":{"mode":"uuid_fill","account_ids":[101,102],"rollout_percent":50}}`
	s := NewCachePolicyService(store, policyTestConfig(), WithCachePolicyAccounts(repo))
	require.NoError(t, s.Refresh(context.Background()))
	original := store.rows[cachePolicySettings[3]]
	require.NotPanics(t, func() {
		stale := policyWrite(t, `{"expected_rev":6,"spec":{"mode":"off"}}`)
		validation, err := s.Validate(context.Background(), "upstream_identity", stale, nil)
		require.NoError(t, err)
		require.True(t, validation.Valid)
		require.True(t, validation.RevStale)
		_, _, err = s.Update(context.Background(), "upstream_identity", stale, nil, CachePolicyActor{})
		var failure *CachePolicyError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, 409, failure.Status)
		for _, spec := range []string{`{"mode":"off","account_ids":[-1]}`, `{"rollout_percent":-1}`, `{"account_ids":[101,101]}`} {
			request := policyWrite(t, fmt.Sprintf(`{"expected_rev":7,"spec":%s}`, spec))
			validation, err = s.Validate(context.Background(), "upstream_identity", request, nil)
			require.NoError(t, err)
			require.False(t, validation.Valid)
			_, _, err = s.Update(context.Background(), "upstream_identity", request, nil, CachePolicyActor{})
			require.ErrorAs(t, err, &failure)
			require.Equal(t, 400, failure.Status)
		}
	})
	require.Equal(t, original, store.rows[cachePolicySettings[3]])
	require.EqualValues(t, 7, s.Snapshot().UpstreamIdentity().Revision())
	require.Zero(t, repo.getCalls)
	require.Empty(t, repo.pages)
}

func TestCachePolicyAccountOptionsMixedStillValidatesAccounts(t *testing.T) {
	repo := &cacheOptionPageRepo{t: t, getErr: errors.New("synthetic required account validation unavailable"), panicList: true}
	store := newPolicyTestStore()
	store.rows[cachePolicySettings[3]] = `{"rev":7,"spec":{"mode":"uuid_fill","account_ids":[101,102],"rollout_percent":50}}`
	s := NewCachePolicyService(store, policyTestConfig(), WithCachePolicyAccounts(repo))
	require.NoError(t, s.Refresh(context.Background()))
	request := upstreamPolicyWrite(t, 7, `{"mode":"off","account_ids":[101,103]}`)
	_, err := s.Validate(context.Background(), "upstream_identity", request, nil)
	require.ErrorIs(t, err, repo.getErr)
	_, _, err = s.Update(context.Background(), "upstream_identity", request, nil, CachePolicyActor{})
	require.ErrorIs(t, err, repo.getErr)
	require.Equal(t, 2, repo.getCalls)
	require.Empty(t, repo.pages)
	require.EqualValues(t, 7, s.Snapshot().UpstreamIdentity().Revision())
}

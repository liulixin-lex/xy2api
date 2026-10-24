//go:build unit

package routes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cacheM3OptionAccounts struct{}

func (cacheM3OptionAccounts) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	out := []*service.Account{}
	for _, id := range ids {
		switch id {
		case 101, 102, 103:
			out = append(out, &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
		case 201:
			out = append(out, &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
		}
	}
	return out, nil
}
func (cacheM3OptionAccounts) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _, _, _, _ string, _ int64, _ string) ([]service.Account, *pagination.PaginationResult, error) {
	return []service.Account{
		{ID: 101, Name: "relay-a-01", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey},
		{ID: 102, Name: "relay-b-01", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: "disabled"},
		{ID: 103, Name: "relay-c-01", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey},
		{ID: 201, Name: "excluded-oauth", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth},
		{ID: 202, Name: "excluded-passthrough", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: map[string]any{"openai_passthrough": true}},
	}, &pagination.PaginationResult{Total: 5}, nil
}

type cacheM3FaultAccounts struct {
	cacheM3OptionAccounts
	failOptions   bool
	forbidQueries bool
	getCalls      int
	listCalls     int
}

func (r *cacheM3FaultAccounts) GetByIDs(ctx context.Context, ids []int64) ([]*service.Account, error) {
	r.getCalls++
	if r.forbidQueries {
		panic("rollback must not query accounts")
	}
	return r.cacheM3OptionAccounts.GetByIDs(ctx, ids)
}

func (r *cacheM3FaultAccounts) ListWithFilters(ctx context.Context, p pagination.PaginationParams, platform, kind, status, search string, group int64, privacy string) ([]service.Account, *pagination.PaginationResult, error) {
	r.listCalls++
	if r.forbidQueries {
		panic("rollback must not enumerate account options")
	}
	if r.failOptions {
		return nil, nil, errors.New("ACCOUNT_OPTIONS_HTTP_SECRET_ERROR_MARKER")
	}
	return r.cacheM3OptionAccounts.ListWithFilters(ctx, p, platform, kind, status, search, group, privacy)
}

func TestCachePolicyM3ContractReplay(t *testing.T) {
	root := "testdata/cache_policies_contract/m3m4"
	manifest, err := os.ReadFile(filepath.Join(root, "SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	require.Len(t, hashes, 22)
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		// M4's four full list fixtures replace the M3 build's unavailable cards.
		if !strings.HasPrefix(name, "m3_list_") {
			names = append(names, name)
		}
	}
	require.Len(t, names, 17)
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			require.Equal(t, hashes[name], fmt.Sprintf("%x", sha256.Sum256(raw)))
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			expected := contractMap(t, f.Response)
			data, _ := f.Response["data"].(map[string]any)
			repo := &cacheRouteStore{rows: map[string]string{service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`}}
			auditRepo := &cacheRouteAudit{}
			audit := service.NewAuditLogService(auditRepo, nil)
			accounts := &cacheM3FaultAccounts{
				failOptions:   name == "m3_list_account_options_unavailable.json" || name == "m3_get_account_options_unavailable.json",
				forbidQueries: name == "m3_put_disable_skip_account_options.json" || name == "m3_validate_disable_ids_unverified.json",
			}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
			// Confirm/preview fixtures assume master on. List fixtures below
			// replace all supported envelopes with their published exact state.
			repo.rows["cache_feature_master"] = `{"rev":1,"spec":{"enabled":true}}`
			repo.rows["cache_observability_policy"] = `{"rev":1,"spec":{"enabled":true}}`
			putSetting := func(key string, rev int64, spec map[string]any) {
				b, err := json.Marshal(map[string]any{"rev": rev, "spec": spec})
				require.NoError(t, err)
				repo.rows[key] = string(b)
			}
			switch {
			case name == "m3_get_account_options_unavailable.json":
				b, err := json.Marshal(data)
				require.NoError(t, err)
				var view service.CachePolicyView
				require.NoError(t, json.Unmarshal(b, &view))
				seedPolicy(t, repo, view)
				repo.rows["cache_observability_policy"] = `{"rev":0,"spec":{"enabled":false}}`
			case name == "m3_put_disable_skip_account_options.json":
				b, err := json.Marshal(data)
				require.NoError(t, err)
				var view service.CachePolicyView
				require.NoError(t, json.Unmarshal(b, &view))
				view.Stored.Rev--
				view.Stored.Spec["mode"] = "uuid_fill"
				seedPolicy(t, repo, view)
			case strings.HasPrefix(name, "m3_validate_"):
				rev := int64(data["current_rev"].(float64))
				spec := map[string]any{"mode": "off", "account_ids": []int64{}, "rollout_percent": 0}
				if rev > 0 {
					spec = map[string]any{"mode": "uuid_fill", "account_ids": []int64{101, 102}, "rollout_percent": 50}
				}
				for _, entry := range data["diff"].([]any) {
					diff := entry.(map[string]any)
					spec[diff["field"].(string)] = diff["before"]
				}
				for _, warning := range data["warnings"].([]any) {
					if warning == "observability_off" {
						repo.rows["cache_observability_policy"] = `{"rev":0,"spec":{"enabled":false}}`
					}
				}
				putSetting("cache_upstream_identity_policy", rev, spec)
			case strings.HasPrefix(name, "m3_put_"):
				var request struct {
					ExpectedRev int64 `json:"expected_rev"`
				}
				require.NoError(t, json.Unmarshal(f.Request.Body, &request))
				putSetting("cache_upstream_identity_policy", request.ExpectedRev, map[string]any{"mode": "off"})
			case name == "m3_history.json":
				require.NotNil(t, f.Fixture)
				now = f.Fixture.Now
				stored, err := json.Marshal(map[string]any{"rev": f.Fixture.Stored.Rev, "updated_at": f.Fixture.Stored.UpdatedAt, "spec": map[string]any{"mode": "off"}})
				require.NoError(t, err)
				repo.rows["cache_upstream_identity_policy"] = string(stored)
				present := map[int64]bool{}
				oldest := now
				for _, rawItem := range data["items"].([]any) {
					b, err := json.Marshal(rawItem)
					require.NoError(t, err)
					var item service.CachePolicyHistoryItem
					require.NoError(t, json.Unmarshal(b, &item))
					var uid *int64
					email := ""
					if item.Actor != nil {
						uid, email = &item.Actor.UserID, item.Actor.Email
					}
					row := &service.AuditLog{CreatedAt: item.At, Action: item.Action, Method: "PUT", ActorUserID: uid, ActorEmail: email, StatusCode: item.StatusCode, Extra: map[string]any{}}
					if item.At.Before(oldest) {
						oldest = item.At
					}
					if item.RevAfter != nil {
						present[*item.RevAfter] = true
						row.Extra["rev_after"], row.Extra["rev_before"] = *item.RevAfter, *item.RevBefore
					}
					row.Extra["changed_fields"] = strings.Join(item.ChangedFields, ",")
					b, err = json.Marshal(map[string]any{"reason": item.Reason})
					require.NoError(t, err)
					row.RequestBody = string(b)
					auditRepo.rows = append(auditRepo.rows, row)
				}
				for _, rev := range f.Fixture.Revisions {
					if !present[rev] {
						auditRepo.rows = append(auditRepo.rows, &service.AuditLog{CreatedAt: oldest.Add(-time.Duration(100-rev) * time.Minute), Action: "admin.cache.policies.upstream_identity.update", Method: "PUT", StatusCode: 200, Extra: map[string]any{"rev_after": rev, "rev_before": rev - 1}})
					}
				}
			}
			policies := service.NewCachePolicyService(repo, cfg, service.WithCacheNorthstar(), service.WithCachePolicyAccounts(accounts), service.WithCacheM4Policies(&cacheM4Groups{}), service.WithCachePolicyClock(func() time.Time { return now }))
			require.NoError(t, policies.Refresh(context.Background()))
			router := cacheTestRouter(policies, audit, nil)
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, f.Status, status)
			require.NotContains(t, fmt.Sprint(got), "ACCOUNT_OPTIONS_HTTP_SECRET_ERROR_MARKER")
			if accounts.forbidQueries {
				require.Zero(t, accounts.getCalls)
				require.Zero(t, accounts.listCalls)
			}
			if accounts.failOptions {
				require.Equal(t, 1, accounts.listCalls)
			}
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got, "compare every field, including options and JSON-string metadata.errors")
		})
	}
}

func TestCachePolicyM3EnablePutOptionsFailure(t *testing.T) {
	// Keep the success fixture exact except for the two fields affected by
	// optional candidate lookup failure. Commit, state and audit still succeed.
	raw, err := os.ReadFile("testdata/cache_policies_contract/m3m4/m3_put_200.json")
	require.NoError(t, err)
	var f cacheContract
	require.NoError(t, json.Unmarshal(raw, &f))
	expected := contractMap(t, f.Response)
	data := expected["data"].(map[string]any)
	data["effective"].(map[string]any)["warnings"] = []any{"account_options_unavailable"}
	data["schema"].([]any)[1].(map[string]any)["options"] = []any{}
	b, err := json.Marshal(f.Response["data"])
	require.NoError(t, err)
	var prior service.CachePolicyView
	require.NoError(t, json.Unmarshal(b, &prior))
	prior.Stored.Rev--
	prior.Stored.Spec["mode"] = "off"
	repo := &cacheRouteStore{rows: map[string]string{
		"cache_feature_master":       `{"rev":1,"spec":{"enabled":true}}`,
		"cache_observability_policy": `{"rev":1,"spec":{"enabled":true}}`,
	}}
	seedPolicy(t, repo, prior)
	cfg := &config.Config{}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	accounts := &cacheM3FaultAccounts{failOptions: true}
	policies := service.NewCachePolicyService(repo, cfg, service.WithCachePolicyAccounts(accounts), service.WithCacheM4Policies(&cacheM4Groups{}))
	require.NoError(t, policies.Refresh(context.Background()))
	auditRows := &cacheRouteAudit{}
	audit := service.NewAuditLogService(auditRows, nil)
	status, got := cacheCall(t, cacheTestRouter(policies, audit, nil), f.Request.Method, f.Request.Path, f.Request.Body)
	require.Equal(t, 200, status)
	normalizePolicyTimes(t, got)
	normalizePolicyTimes(t, expected)
	require.Equal(t, expected, got)
	require.NotContains(t, fmt.Sprint(got), "ACCOUNT_OPTIONS_HTTP_SECRET_ERROR_MARKER")
	require.Equal(t, 1, accounts.getCalls)
	require.Equal(t, 1, accounts.listCalls)
	require.EqualValues(t, 4, policies.Snapshot().UpstreamIdentity().Revision())
	require.True(t, policies.Snapshot().UpstreamIdentity().Enabled())
	audit.Start()
	audit.Stop()
	require.Len(t, auditRows.rows, 1)
	require.Equal(t, "admin.cache.policies.upstream_identity.update", auditRows.rows[0].Action)
	require.Equal(t, 200, auditRows.rows[0].StatusCode)
	require.EqualValues(t, 4, auditRows.rows[0].Extra["rev_after"])
}

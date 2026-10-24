//go:build unit

package routes

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cachePolicyUpstreamAccounts struct{}

func (cachePolicyUpstreamAccounts) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	accounts := []*service.Account{}
	for _, id := range ids {
		switch id {
		case 101:
			accounts = append(accounts, &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
		case 103:
			accounts = append(accounts, &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
		}
	}
	return accounts, nil
}

func TestCachePolicyUpstreamHTTPValidationConfirmationAndAudit(t *testing.T) {
	// Pin the account scope before enabling the policy. Narrowing the scope in
	// the same request as enabling the mode is correctly classified as mixed.
	repo := &cacheRouteStore{rows: map[string]string{
		"cache_upstream_identity_policy": `{"rev":0,"spec":{"account_ids":[101]}}`,
	}}
	cfg := &config.Config{}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	policies := service.NewCachePolicyService(repo, cfg, service.WithCachePolicyAccounts(cachePolicyUpstreamAccounts{}))
	require.NoError(t, policies.Refresh(context.Background()))
	auditRows := &cacheRouteAudit{}
	audit := service.NewAuditLogService(auditRows, nil)
	router := cacheTestRouter(policies, audit, nil)
	path := "/api/v1/admin/cache/policies/upstream_identity"
	status, body := cacheCall(t, router, "GET", path, nil)
	require.Equal(t, 200, status)
	require.True(t, body["data"].(map[string]any)["available"].(bool))
	for _, unsupported := range []string{"northstar", "anthropic_identity", "routing"} {
		status, body = cacheCall(t, router, "GET", "/api/v1/admin/cache/policies/"+unsupported, nil)
		require.Equal(t, 404, status)
		require.Equal(t, "CACHE_POLICY_NOT_IN_BUILD", body["reason"])
	}
	for _, tc := range []struct{ ids, code string }{{`[999]`, "unknown_account"}, {`[103]`, "wrong_platform"}, {`[101,101]`, "duplicate"}} {
		status, body = cacheCall(t, router, "POST", path+"/validate", []byte(`{"expected_rev":0,"spec":{"account_ids":`+tc.ids+`}}`))
		require.Equal(t, 200, status)
		validation := body["data"].(map[string]any)
		require.False(t, validation["valid"].(bool))
		require.Equal(t, tc.code, validation["errors"].([]any)[0].(map[string]any)["code"])
	}
	request := []byte(`{"expected_rev":0,"spec":{"mode":"uuid_fill","account_ids":[101],"rollout_percent":50}}`)
	status, body = cacheCall(t, router, "POST", path+"/validate", request)
	require.Equal(t, 200, status)
	validation := body["data"].(map[string]any)
	require.True(t, validation["valid"].(bool))
	require.Equal(t, "enable", validation["direction"])
	require.True(t, validation["confirm"].(map[string]any)["reason_required"].(bool))
	require.True(t, validation["confirm"].(map[string]any)["checkbox_required"].(bool))
	require.Equal(t, "masked_by_master", validation["effective_preview"].(map[string]any)["state"])
	status, body = cacheCall(t, router, "PUT", path, request)
	require.Equal(t, 422, status)
	require.Equal(t, "CACHE_POLICY_CONFIRM_REQUIRED", body["reason"])
	status, body = cacheCall(t, router, "PUT", path, []byte(`{"expected_rev":0,"spec":{"mode":"uuid_fill","account_ids":[101],"rollout_percent":50},"reason":"synthetic confirmation","confirm":{"checkbox":true}}`))
	require.Equal(t, 200, status)
	require.Equal(t, "masked_by_master", body["data"].(map[string]any)["effective"].(map[string]any)["state"])
	for _, spec := range []string{`{"header_only_uuid":true}`, `{"forward_session_headers":"normalize"}`} {
		status, body = cacheCall(t, router, "PUT", path, []byte(`{"expected_rev":1,"spec":`+spec+`}`))
		require.Equal(t, 400, status)
		require.Equal(t, "CACHE_POLICY_UNSUPPORTED", body["reason"])
	}
	status, body = cacheCall(t, router, "PUT", "/api/v1/admin/cache/policies/master", []byte(`{"expected_rev":0,"spec":{"enabled":true},"reason":"synthetic master confirmation","confirm":{"checkbox":true}}`))
	require.Equal(t, 200, status)
	require.True(t, policies.Snapshot().UpstreamIdentity().Enabled())
	require.True(t, policies.Snapshot().UpstreamIdentity().AllowsAccount(101))
	require.False(t, policies.Snapshot().UpstreamIdentity().AllowsAccount(103))
	audit.Start()
	audit.Stop()
	seen := 0
	for _, row := range auditRows.rows {
		if row.Action != "admin.cache.policies.upstream_identity.update" || row.StatusCode != 200 {
			continue
		}
		seen++
		require.Equal(t, "upstream_identity", row.Extra["policy_key"])
		require.EqualValues(t, 0, row.Extra["rev_before"])
		require.EqualValues(t, 1, row.Extra["rev_after"])
		for _, forbidden := range []string{"credentials", "session_digest", "origin_group_id"} {
			require.NotContains(t, row.RequestBody, forbidden)
		}
	}
	require.Equal(t, 1, seen)
}

//go:build unit

package operatoraccess

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogKeysAreWellFormed(t *testing.T) {
	keys := Keys()
	require.Len(t, keys, 509)
	require.True(t, sort.StringsAreSorted(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		parts := strings.SplitN(key, " ", 2)
		require.Len(t, parts, 2)
		require.Contains(t, []string{"GET", "POST", "PUT", "PATCH", "DELETE"}, parts[0])
		require.True(t, strings.HasPrefix(parts[1], "/"))
		require.False(t, seen[key])
		seen[key] = true
	}
	require.Equal(t, "GET /accounts/:id", Key("GET", AdminPrefix+"/accounts/:id"))
	require.Equal(t, "GET /api/v1/pages", Key("GET", "/api/v1/pages"))
	require.Equal(t, "GET /api/v1/administer/x", Key("GET", "/api/v1/administer/x"))
}
func TestCatalogEntriesAreConsistent(t *testing.T) {
	targets := strings.Fields("user_path_id user_path_user_id user_body_id user_body_ids apikey_owner sub_owner sub_body_owners order_owner group_rate_rows group_rpm_rows group_members")
	fields := strings.Fields("user_create user_update user_batch_no_all user_bind_identity user_replace_group apikey_group user_search group_create group_update group_delete channel_create channel_update monitor_create monitor_update monitor_template_update monitor_template_apply account_create account_update account_bulk_update account_oauth_apply account_import_address account_url_probe proxy_create proxy_update proxy_delete redeem_safe_rows redeem_batch_rows promo_search subscription_assign subscription_update affiliate_config risk_config ops_alert_config redeem_search codex_ticket_proxy redeem_create_and_redeem sub_bulk_action aff_rate ops_runtime_alert")
	redactions := strings.Fields("account api_key redeem_code promo_code order_code proxy_password monitor monitor_template audit_body ops_upstream_url")
	for _, key := range Keys() {
		t.Run(key, func(t *testing.T) {
			e, ok := Lookup(key)
			require.True(t, ok)
			if e.Kind != KindModule {
				require.Contains(t, []Kind{KindSuperOnly, KindAnyAdmin}, e.Kind)
				require.Empty(t, e.Module)
				require.Equal(t, LevelNone, e.Level)
				require.Empty(t, e.AnyOf)
				require.Empty(t, e.Flags)
				require.Empty(t, e.Target)
				require.Empty(t, e.Fields)
			} else {
				require.Contains(t, Modules, e.Module)
				require.Contains(t, []Level{LevelView, LevelManage}, e.Level)
			}
			if len(e.AnyOf) > 0 {
				require.Equal(t, LevelView, e.Level)
			}
			for _, m := range e.AnyOf {
				require.Contains(t, Modules, m)
			}
			for _, f := range e.Flags {
				require.Contains(t, Flags, f)
			}
			if e.Target != "" {
				require.Contains(t, targets, string(e.Target))
			}
			if e.Fields != "" {
				require.Contains(t, fields, string(e.Fields))
			}
			for _, r := range e.Redact {
				require.Contains(t, redactions, string(r))
			}
		})
	}
}
func TestCatalogHasPRAAndPRBRows(t *testing.T) {
	for _, key := range []string{MeAccessKey, "GET /users/:id/operator-permissions", "PUT /users/:id/operator-permissions", "GET /accounts/upstream-balances", DownloadTicketKey, "POST /accounts/iq-check/run"} {
		_, ok := Lookup(key)
		require.True(t, ok, key)
	}
	e, _ := Lookup("POST /accounts/:id/revert-proxy-fallback")
	require.Equal(t, KindSuperOnly, e.Kind)
	e, _ = Lookup("DELETE /proxies/:id")
	require.Equal(t, FieldRule("proxy_delete"), e.Fields)
}
func TestCatalogMatchesApprovedTable(t *testing.T) {
	var rows []struct {
		Method string
		Path   string
		Module Module
		Level  string
		AnyOf  []Module `json:"any_of"`
		Flags  []Flag
		Target TargetRule
		Fields FieldRule
		Redact []Redaction
		WS     bool
	}
	raw, err := os.ReadFile("testdata/pra_catalog.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &rows))
	require.Len(t, rows, 509)
	keys := []string{}
	used := map[FieldRule]bool{}
	for _, row := range rows {
		key := row.Method + " " + row.Path
		keys = append(keys, key)
		used[row.Fields] = true
		t.Run(key, func(t *testing.T) {
			e, ok := Lookup(key)
			require.True(t, ok)
			want := Entry{Module: row.Module, AnyOf: row.AnyOf, Flags: row.Flags, Target: row.Target, Fields: row.Fields, Redact: row.Redact, WS: row.WS}
			switch row.Level {
			case "super_only":
				want.Kind = KindSuperOnly
			case "any_admin":
				want.Kind = KindAnyAdmin
			default:
				want.Kind = KindModule
				var valid bool
				want.Level, valid = ParseLevel(row.Level)
				require.True(t, valid)
			}
			require.Equal(t, want.Kind, e.Kind)
			require.Equal(t, want.Module, e.Module)
			require.Equal(t, want.Level, e.Level)
			require.ElementsMatch(t, want.AnyOf, e.AnyOf)
			require.ElementsMatch(t, want.Flags, e.Flags)
			require.Equal(t, want.Target, e.Target)
			require.Equal(t, want.Fields, e.Fields)
			require.ElementsMatch(t, want.Redact, e.Redact)
			require.Equal(t, want.WS, e.WS)
		})
	}
	require.ElementsMatch(t, keys, Keys())
	var skip []FieldRule
	raw, err = os.ReadFile("testdata/pra_service_enforced.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &skip))
	actual := []FieldRule{}
	for id, enabled := range ServiceEnforcedFields {
		require.True(t, enabled)
		require.True(t, used[id], string(id))
		actual = append(actual, id)
	}
	require.Len(t, skip, 13)
	require.ElementsMatch(t, skip, actual)
}

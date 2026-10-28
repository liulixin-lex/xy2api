//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/liulixin-lex/xy2api/ent"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
)

type operatorRuleCase struct {
	kind, body string
	reason     string
}

// Explicit cases make catalog additions fail until their behavior is exercised.
var operatorRuleCases = map[string][]operatorRuleCase{
	"account_import_address":   {{"allow", `{}`, ""}, {"deny", `{"credentials":{"base_url":"https://203.0.113.10"}}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN"}, {"boundary", `{"credentials":null}`, ""}},
	"account_url_probe":        {{"allow", `{}`, ""}, {"deny", `{"base_url":"https://203.0.113.10"}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN"}, {"boundary", `{"base_url":""}`, ""}},
	"aff_rate":                 {{"allow", `{}`, ""}, {"deny", `{"clear_rebate_rate":false}`, "ADMIN_PERMISSION_DENIED"}, {"boundary", `{"aff_rebate_rate_percent":null}`, "ADMIN_PERMISSION_DENIED"}},
	"channel_create":           {{"allow", `{}`, ""}, {"deny", `{"group_ids":[1]}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"group_ids":[]}`, ""}},
	"channel_update":           {{"allow", `{}`, ""}, {"deny", `{"group_ids":[9]}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"group_ids":[2,1,1]}`, ""}},
	"codex_ticket_proxy":       {{"allow", `{}`, ""}, {"deny", `{"proxy_url":"http://203.0.113.10"}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"proxy_url":""}`, ""}},
	"monitor_create":           {{"allow", `{}`, ""}, {"deny", `{"extra_headers":{"x":"******"}}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"account_id":null}`, ""}},
	"monitor_update":           {{"allow", `{}`, ""}, {"deny", `{"account_id":4}`, "ADMIN_PERMISSION_DENIED"}, {"boundary", `{"account_id":0}`, ""}},
	"monitor_template_apply":   {{"allow", `{}`, ""}, {"deny", `{}`, "SUPER_ADMIN_REQUIRED"}, {"boundary", `{}`, ""}},
	"monitor_template_update":  {{"allow", `{}`, ""}, {"deny", `{"extra_headers":4}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"extra_headers":{"x-secret":"******"}}`, ""}},
	"ops_runtime_alert":        {{"allow", `{}`, ""}, {"deny", `{"distributed_lock":{"enabled":true}}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"distributed_lock":null}`, ""}},
	"promo_search":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"proxy_create":             {{"allow", `{"host":"203.0.113.10"}`, ""}, {"deny", `{"host":"127.0.0.1"}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"host":"203.0.113.10","proxies":[]}`, ""}},
	"proxy_delete":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"ids":[]}`, ""}},
	"proxy_update":             {{"allow", `{"name":"ok"}`, ""}, {"deny", `{"port":9090}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN"}, {"boundary", `{"port":8080,"host":"203.0.113.10"}`, ""}},
	"redeem_batch_rows":        {{"allow", `{"ids":[10]}`, ""}, {"deny", `{"ids":[12]}`, "SUPER_ADMIN_REQUIRED"}, {"boundary", `{"ids":[10,10]}`, ""}},
	"redeem_create_and_redeem": {{"allow", `{"code":"new"}`, ""}, {"deny", `{"code":" new "}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"type":""}`, ""}},
	"redeem_safe_rows":         {{"allow", `{"ids":[10]}`, ""}, {"deny", `{"ids":[11]}`, "SUPER_ADMIN_REQUIRED"}, {"boundary", `{"ids":[10,10]}`, ""}},
	"redeem_search":            {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"risk_config":              {{"allow", `{}`, ""}, {"deny", `{"api_key":"new"}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"clear_api_key":false}`, ""}},
	"sub_bulk_action":          {{"allow", `{"action":"revoke"}`, ""}, {"deny", `{"action":"restore"}`, "ADMIN_PERMISSION_DENIED"}, {"boundary", `{"action":"extend"}`, ""}},
	"user_batch_no_all":        {{"allow", `{"all":false}`, ""}, {"deny", `{"all":true}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{"all":null}`, ""}},
	"account_create":           {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"account_update":           {{"allow", `{}`, ""}, {"deny", `{}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"account_bulk_update":      {{"allow", `{}`, ""}, {"deny", `{}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"account_oauth_apply":      {{"allow", `{}`, ""}, {"deny", `{}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"user_create":              {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"user_update":              {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"user_bind_identity":       {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"user_replace_group":       {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"apikey_group":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"group_create":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"group_update":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_FIELD_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"group_delete":             {{"allow", `{}`, ""}, {"deny", `{}`, "GROUP_NOT_EMPTY"}, {"boundary", `{}`, ""}},
	"user_search":              {{"allow", `{}`, ""}, {"deny", `{}`, ""}, {"boundary", `{}`, ""}},
	"user_path_id":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "USER_NOT_FOUND"}},
	"user_path_user_id":        {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "USER_NOT_FOUND"}},
	"user_body_id":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "ADMIN_FIELD_FORBIDDEN"}},
	"user_body_ids":            {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"apikey_owner":             {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "OPERATOR_ACCESS_UNAVAILABLE"}},
	"sub_owner":                {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "SUBSCRIPTION_NOT_FOUND"}},
	"sub_body_owners":          {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"order_owner":              {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "OPERATOR_ACCESS_UNAVAILABLE"}},
	"group_rate_rows":          {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"group_rpm_rows":           {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, ""}},
	"attribute_value_owners":   {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "OPERATOR_ACCESS_UNAVAILABLE"}},
	"group_members":            {{"allow", `{}`, ""}, {"deny", `{}`, "ADMIN_TARGET_FORBIDDEN"}, {"boundary", `{}`, "OPERATOR_ACCESS_UNAVAILABLE"}},
}

func TestEveryRuleHasAllowDenyAndBoundaryCases(t *testing.T) {
	rules := map[string]bool{}
	for _, key := range operatoraccess.Keys() {
		entry, _ := operatoraccess.Lookup(key)
		if entry.Target != "" {
			rules[string(entry.Target)] = true
		}
		if entry.Fields != "" {
			rules[string(entry.Fields)] = true
		}
	}
	require.Len(t, operatorRuleCases, len(rules))
	for id := range rules {
		cases, ok := operatorRuleCases[id]
		require.True(t, ok, id)
		seen := map[string]bool{}
		for _, tc := range cases {
			seen[tc.kind] = true
			t.Run(id+"/"+tc.kind, func(t *testing.T) {
				err := runOperatorRuleCase(t, id, tc)
				if tc.reason == "" {
					require.NoError(t, err)
				} else {
					require.Equal(t, tc.reason, infraerrors.Reason(err))
				}
			})
		}
		for _, kind := range []string{"allow", "deny", "boundary"} {
			require.True(t, seen[kind], id+"/"+kind)
		}
	}
}
func runOperatorRuleCase(t *testing.T, id string, tc operatorRuleCase) error {
	t.Helper()
	if operatoraccess.ServiceEnforcedFields[operatoraccess.FieldRule(id)] {
		return operatorServiceRuleCase(t, id, tc.kind)
	}
	f := newOperatorGuardFixture(t)
	r := guardRequest(tc.body)
	ctx := context.Background()
	if id == "user_search" {
		// This rule restricts a repository predicate instead of rejecting HTTP.
		// The integration test verifies its SQL behavior; exercise the exact
		// context predicate used by that repository for all three case kinds.
		if tc.kind == "boundary" {
			require.False(t, operatoraccess.HideAPIKeySearch(ctx))
		} else {
			ctx = operatoraccess.WithScope(ctx, f.sc)
			if tc.kind == "allow" {
				f.sc.Perms.Flags[operatoraccess.FlagDataExport] = true
			}
			require.True(t, operatoraccess.HideAPIKeySearch(ctx))
		}
		return nil
	}
	switch id {
	case "attribute_value_owners":
		f.query.owners = []int64{1}
		if tc.kind == "deny" {
			f.query.owners = []int64{1, 2}
		}
		if tc.kind == "boundary" {
			f.query.err = errors.New("query unavailable")
		}
		return f.svc.CheckTarget(ctx, f.sc, operatoraccess.TargetRule(id), r)
	case "promo_search", "redeem_search":
		if tc.kind == "deny" {
			r.Query = url.Values{"sort_by": {" CoDe "}}
		}
		if tc.kind == "boundary" {
			r.Query = url.Values{"search": {" "}}
		}
	case "proxy_create":
		f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
		r.Key = "POST /proxies"
	case "proxy_delete":
		if tc.kind == "deny" {
			f.query.used[10] = true
		}
		if tc.kind == "boundary" {
			r.Params = nil
		}
	case "redeem_safe_rows":
		r.Params = nil
	case "monitor_template_apply":
		if tc.kind != "deny" {
			f.template.row.ExtraHeaders = nil
		}
	case "sub_bulk_action":
		if tc.kind == "boundary" {
			f.sc.Perms.Flags[operatoraccess.FlagUsersBalanceAdjust] = true
		}
	case "ops_runtime_alert":
		stored, err := f.svc.opsService.GetOpsAlertRuntimeSettings(ctx)
		require.NoError(t, err)
		if tc.kind == "boundary" {
			raw, err := json.Marshal(map[string]any{"distributed_lock": stored.DistributedLock})
			require.NoError(t, err)
			r.Body = raw
		}
	}
	if _, ok := map[string]bool{"user_path_id": true, "user_path_user_id": true, "user_body_id": true, "user_body_ids": true, "apikey_owner": true, "sub_owner": true, "sub_body_owners": true, "order_owner": true, "group_rate_rows": true, "group_rpm_rows": true, "group_members": true}[id]; ok {
		return operatorTargetRuleCase(t, f, id, tc.kind)
	}
	_, err := f.svc.CheckFields(ctx, f.sc, operatoraccess.FieldRule(id), r)
	return err
}
func operatorServiceRuleCase(t *testing.T, id, kind string) error {
	t.Helper()
	s, _, groups := operatorUserFixture()
	ctx := operatorRulesContext(s)
	deny := kind == "deny"
	boundary := kind == "boundary"
	switch id {
	case "account_create":
		in := &CreateAccountInput{}
		if deny {
			in.GroupIDs = []int64{1}
		}
		if boundary {
			in.GroupIDs = []int64{}
		}
		return s.operatorAccountCreateRules(ctx, in)
	case "account_update":
		in := &UpdateAccountInput{}
		if deny {
			in.ProxyID = operatorPtr(int64(2))
		}
		if boundary {
			in.Credentials = map[string]any{"base_url": ""}
		}
		return s.operatorAccountUpdateRules(ctx, operatorStoredAccount(), in)
	case "account_bulk_update":
		in := &BulkUpdateAccountsInput{}
		if deny {
			in.ProxyID = operatorPtr(int64(2))
		}
		if boundary {
			in.Credentials = map[string]any{}
		}
		return s.operatorAccountBulkRules(ctx, in)
	case "account_oauth_apply":
		a := operatorStoredAccount()
		a.Type = "oauth"
		extra := map[string]any{}
		if deny {
			extra["custom_base_url"] = "https://evil.example"
		}
		return OperatorCheckOAuthApply(ctx, a, "oauth", nil, extra)
	case "user_create":
		in := &CreateUserInput{}
		if deny {
			in.Balance = operatorPtr(5.0)
		}
		if boundary {
			in.Balance = operatorPtr(0.0)
		}
		return s.operatorUserCreateRules(ctx, in)
	case "user_update":
		in := &UpdateUserInput{}
		if deny {
			in.Balance = operatorPtr(0.0)
		}
		if boundary {
			in.AllowedGroups = &[]int64{}
		}
		return s.operatorUserUpdateRules(ctx, 1, in)
	case "user_bind_identity":
		in := AdminBindAuthIdentityInput{}
		if deny {
			in.Metadata = map[string]any{"source": "forged"}
		}
		if boundary {
			in.Metadata = map[string]any{}
		}
		return s.operatorBindIdentityRules(ctx, in)
	case "user_replace_group":
		id := int64(1)
		if deny {
			id = 2
		}
		if boundary {
			id = 3
		}
		return s.operatorReplaceGroupRules(ctx, 1, id)
	case "apikey_group":
		s.apiKeyRepo = &operatorGuardAPIKeys{key: &APIKey{UserID: 1}}
		groupID := operatorPtr(int64(1))
		if deny {
			groupID = operatorPtr(int64(2))
		}
		if boundary {
			groupID = nil
		}
		return s.operatorAPIKeyGroupRules(ctx, 10, groupID)
	case "group_create":
		in := &CreateGroupInput{IsExclusive: true}
		if deny {
			in.IsExclusive = false
		}
		if boundary {
			in.FallbackGroupID = operatorPtr(int64(0))
		}
		return s.operatorGroupCreateRules(ctx, in)
	case "group_update":
		groups.rows[1].RateMultiplier = 1
		in := &UpdateGroupInput{}
		if deny {
			in.RateMultiplier = operatorPtr(2.0)
		}
		if boundary {
			in.RateMultiplier = operatorPtr(1.0)
		}
		return s.operatorGroupUpdateRules(ctx, 1, in)
	case "group_delete":
		s.emptyGroupDeleteRepo = groups
		if deny {
			groups.notEmpty = true
		}
		if boundary {
			ctx = operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust)
		}
		return s.DeleteGroup(ctx, 1)
	case "user_search":
		if kind == "allow" {
			require.False(t, operatoraccess.HideAPIKeySearch(context.Background()))
		} else {
			require.True(t, operatoraccess.HideAPIKeySearch(ctx))
		}
		return nil
	}
	t.Fatalf("missing service runner for %s", id)
	return nil
}
func operatorTargetRuleCase(t *testing.T, f *operatorGuardFixture, id, kind string) error {
	t.Helper()
	ctx := context.Background()
	r := guardRequest("{}")
	userID := int64(1)
	if kind == "deny" {
		userID = 2
	}
	switch id {
	case "user_path_id", "user_path_user_id":
		if kind == "boundary" {
			userID = 999
		}
		r.Params = map[string]string{"id": stringInt(userID), "user_id": stringInt(userID)}
	case "user_body_id":
		if kind == "boundary" {
			userID = 0
		}
		r.Body = []byte(`{"user_id":` + stringInt(userID) + `}`)
	case "user_body_ids":
		r.Body = []byte(`{"user_ids":[` + stringInt(userID) + `]}`)
		if kind == "boundary" {
			r.Body = []byte(`{"user_ids":[]}`)
		}
	case "apikey_owner":
		f.svc.apiKeyRepo = &operatorGuardAPIKeys{key: &APIKey{UserID: userID}}
		if kind == "boundary" {
			f.svc.apiKeyRepo = nil
		}
	case "sub_owner", "sub_body_owners":
		f.svc.subscriptionRepo = &operatorGuardSubscriptions{rows: map[int64]*UserSubscription{10: {UserID: userID}}}
		r.Body = []byte(`{"subscription_ids":[10]}`)
		if kind == "boundary" {
			if id == "sub_owner" {
				r.Params["id"] = "999"
			} else {
				r.Body = []byte(`{"subscription_ids":[]}`)
			}
		}
	case "group_members":
		f.query.protected = kind == "deny"
		if kind == "boundary" {
			f.svc.queryRepo = nil
		}
	case "group_rate_rows", "group_rpm_rows":
		r.Body = []byte(`{"entries":[{"user_id":` + stringInt(userID) + `}]}`)
		if kind == "boundary" {
			r.Body = []byte(`{"entries":[]}`)
		}
		f.svc.userGroupRateRepo = &operatorGuardRates{}
	case "order_owner":
		if kind == "boundary" {
			f.svc.paymentService = nil
			break
		}
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()); require.NoError(t, mock.ExpectationsWereMet()) })
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(10, userID))
		mock.ExpectClose()
		f.svc.paymentService = &PaymentService{entClient: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}

	}
	return f.svc.CheckTarget(ctx, f.sc, operatoraccess.TargetRule(id), r)
}
func stringInt(id int64) string { raw, _ := json.Marshal(id); return string(raw) }

// Pin the public error shape at the service boundary; HTTP-only failures are
// also exercised by TestAdminPermissionCanonicalAndBodyLimit and
// TestAdminPermissionRedactsJSONOnly.
func TestOperatorErrorContracts(t *testing.T) {
	s, _, _ := operatorTestService()
	_, invalid := s.PutPermissions(context.Background(), 1, 5, []byte(`{"modules":{"accounts":"none"}}`))
	actorService := &adminServiceImpl{requireActor: true}
	_, _, actor := actorService.operatorScope(context.Background())
	for _, tc := range []struct {
		err      error
		reason   string
		status   int
		metadata map[string]string
	}{
		{operatorNeedsFlag(operatoraccess.FlagUsersBalanceAdjust), "ADMIN_PERMISSION_DENIED", 403, map[string]string{"required": "users.balance_adjust"}},
		{operatorTargetError(), "ADMIN_TARGET_FORBIDDEN", 403, nil},
		{operatorFieldError("code"), "ADMIN_FIELD_FORBIDDEN", 400, map[string]string{"field": "code"}},
		{operatorPrivateAddress("host"), "ADMIN_FIELD_FORBIDDEN", 400, map[string]string{"field": "host", "cause": "private_address"}},
		{invalid, "ADMIN_PERMISSION_INVALID", 400, nil},
		{operatorNewKey("api_key"), "ACCOUNT_API_KEY_REQUIRED", 400, map[string]string{"field": "api_key"}},
		{operatorSuperOnly(), "SUPER_ADMIN_REQUIRED", 403, nil},
		{operatorNetworkError("proxy_id", "refreshable_account"), "OPERATOR_NETWORK_CHANGE_FORBIDDEN", 403, map[string]string{"field": "proxy_id", "cause": "refreshable_account"}},
		{operatorUnavailable(), "OPERATOR_ACCESS_UNAVAILABLE", 503, nil},
		{actor, "ADMIN_ACTOR_MISSING", 503, nil},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			require.Equal(t, tc.status, infraerrors.Code(tc.err))
			require.Equal(t, tc.reason, infraerrors.Reason(tc.err))
			require.Equal(t, tc.metadata, infraerrors.FromError(tc.err).Metadata)
		})
	}
}

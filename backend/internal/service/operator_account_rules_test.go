//go:build unit

package service

import (
	"context"
	"errors"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func operatorRulesContext(s *adminServiceImpl, flags ...operatoraccess.Flag) context.Context {
	svc := &OperatorAccessService{adminService: s, userRepo: s.userRepo, apiKeyRepo: s.apiKeyRepo}
	sc := &operatoraccess.Scope{UserID: 7, Guard: svc, Perms: operatoraccess.Permissions{Flags: map[operatoraccess.Flag]bool{}}}
	for _, f := range flags {
		sc.Perms.Flags[f] = true
	}
	return operatoraccess.WithScope(context.Background(), sc)
}
func operatorRuleError(t *testing.T, err error, reason, field, cause string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.Reason(err))
	var app *infraerrors.ApplicationError
	require.ErrorAs(t, err, &app)
	if field != "" {
		require.Equal(t, field, app.Metadata["field"])
	}
	if cause != "" {
		require.Equal(t, cause, app.Metadata["cause"])
	}
}
func operatorPtr[T any](v T) *T { return &v }
func operatorStoredAccount() *Account {
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://8.8.8.8", "api_key": "stored", "header_overrides": map[string]any{"x-relay": "stored-header"}}, Extra: map[string]any{"custom_base_url": "https://8.8.8.8", "cache_ttl_override_enabled": true}, GroupIDs: []int64{1}}
}
func TestOperatorAccountNetworkChangeRequiresNewKey(t *testing.T) {
	s := &adminServiceImpl{}
	ctx := operatorRulesContext(s, operatoraccess.FlagAccountsUpstreamAddress)
	a := operatorStoredAccount()
	in := &UpdateAccountInput{ProxyID: operatorPtr(int64(2))}
	operatorRuleError(t, s.operatorAccountUpdateRules(ctx, a, in), "ACCOUNT_API_KEY_REQUIRED", "credentials.api_key", "")
	in.Credentials = map[string]any{"api_key": "new"}
	require.NoError(t, s.operatorAccountUpdateRules(ctx, a, in))
}
func TestOperatorNetworkChangeRequiresEverySecret(t *testing.T) {
	for _, key := range SensitiveCredentialKeys {
		t.Run(key, func(t *testing.T) {
			s := &adminServiceImpl{}
			a := operatorStoredAccount()
			a.Credentials[key] = "stored-secret"
			in := &UpdateAccountInput{ProxyID: operatorPtr(int64(2)), Credentials: map[string]any{"api_key": "new"}}
			if key == "api_key" {
				in.Credentials[key] = "stored-secret"
			}
			ctx := operatorRulesContext(s, operatoraccess.FlagAccountsUpstreamAddress)
			operatorRuleError(t, s.operatorAccountUpdateRules(ctx, a, in), "ACCOUNT_API_KEY_REQUIRED", "credentials."+key, "")
			for _, v := range []any{nil, "", "new-secret"} {
				in.Credentials[key] = v
				require.NoError(t, s.operatorAccountUpdateRules(ctx, a, in))
			}
		})
	}
}

func TestOperatorAccountMaskedURLRoundTrip(t *testing.T) {
	s := &adminServiceImpl{}
	a := operatorStoredAccount()
	a.Credentials["base_url"] = "https://relay-user:relay-pass@relay.example/v1"
	a.Credentials["api_base_urls"] = []any{"https://user:pass@one.example/v1", "https://two.example/v1"}
	ctx := operatorRulesContext(s, operatoraccess.FlagAccountsUpstreamAddress)
	in := &UpdateAccountInput{Credentials: map[string]any{
		"base_url":      operatoraccess.StripURLUserinfo(a.Credentials["base_url"].(string)),
		"api_base_urls": []any{"https://***@one.example/v1", "https://two.example/v1"},
	}}
	require.NoError(t, s.operatorAccountUpdateRules(ctx, a, in))
	require.Equal(t, a.Credentials["base_url"], in.Credentials["base_url"])
	require.Equal(t, a.Credentials["api_base_urls"], in.Credentials["api_base_urls"])
	in = &UpdateAccountInput{Credentials: map[string]any{"base_url": "https://***@other.example/v1"}}
	operatorRuleError(t, s.operatorAccountUpdateRules(ctx, a, in), "ADMIN_FIELD_FORBIDDEN", "credentials.base_url", "")
}
func TestOperatorCannotChangeNetworkOfRefreshableAccount(t *testing.T) {
	for _, typ := range OperatorRefreshableTypes {
		for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagAccountsUpstreamAddress}} {
			for _, which := range []string{"proxy_id", "type", "api_protocol", "account_mode", "aws_region", "base_url"} {
				s := &adminServiceImpl{}
				a := operatorStoredAccount()
				a.Type = typ
				in := &UpdateAccountInput{Credentials: map[string]any{"api_key": "new"}}
				switch which {
				case "proxy_id":
					in.ProxyID = operatorPtr(int64(2))
				case "type":
					in.Type = "oauth"
					if typ == "oauth" {
						in.Type = "setup-token"
					}
				default:
					in.Credentials[which] = map[string]string{"api_protocol": "responses", "account_mode": "chatgpt", "aws_region": "eu-central-1", "base_url": "https://1.1.1.1"}[which]
				}
				err := s.operatorAccountUpdateRules(operatorRulesContext(s, flags...), a, in)
				operatorRuleError(t, err, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "", "refreshable_account")
			}
		}
	}
}
func TestOperatorAddressWritesFollowUpstreamAddressFlag(t *testing.T) {
	s := &adminServiceImpl{}
	for _, path := range OperatorAddressKeys {
		for _, create := range []bool{true, false} {
			cred, extra := map[string]any{}, map[string]any{}
			m, k := operatorMapKey(path, cred, extra)
			m[k] = "https://1.1.1.1"
			if strings.HasSuffix(k, "_enabled") {
				m[k] = true
			}
			ctx := operatorRulesContext(s)
			var err error
			if create {
				err = s.operatorAccountCreateRules(ctx, &CreateAccountInput{Credentials: cred, Extra: extra})
			} else {
				err = s.operatorAccountUpdateRules(ctx, operatorStoredAccount(), &UpdateAccountInput{Credentials: cred, Extra: extra})
			}
			operatorRuleError(t, err, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", path, "address")
		}
	}
	for _, raw := range []string{"https://127.0.0.1", "https://10.0.0.1", "https://169.254.169.254", "http://metadata.google.internal"} {
		ctx := operatorRulesContext(s, operatoraccess.FlagAccountsUpstreamAddress)
		operatorRuleError(t, s.operatorAccountCreateRules(ctx, &CreateAccountInput{Credentials: map[string]any{"base_url": raw}}), "ADMIN_FIELD_FORBIDDEN", "credentials.base_url", "private_address")
		operatorRuleError(t, s.operatorAccountUpdateRules(ctx, operatorStoredAccount(), &UpdateAccountInput{Credentials: map[string]any{"base_url": raw, "api_key": "new"}}), "ADMIN_FIELD_FORBIDDEN", "credentials.base_url", "private_address")
	}
	ctx := operatorRulesContext(s, operatoraccess.FlagAccountsUpstreamAddress)
	require.NoError(t, s.operatorAccountCreateRules(ctx, &CreateAccountInput{Credentials: map[string]any{"base_url": "https://1.1.1.1"}}))
	require.NoError(t, s.operatorAccountUpdateRules(ctx, operatorStoredAccount(), &UpdateAccountInput{Credentials: map[string]any{"base_url": "https://1.1.1.1", "api_key": "new"}}))
	operatorRuleError(t, s.operatorAccountCreateRules(operatorRulesContext(s), &CreateAccountInput{ProxyID: operatorPtr(int64(1))}), "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "proxy_id", "address")
}
func TestOperatorAccountKeepsStoredAddressWithoutFlag(t *testing.T) {
	s := &adminServiceImpl{}
	for _, v := range []any{nil, ""} {
		a := operatorStoredAccount()
		in := &UpdateAccountInput{Credentials: map[string]any{"base_url": v}, Extra: map[string]any{"custom_base_url": v}}
		require.NoError(t, s.operatorAccountUpdateRules(operatorRulesContext(s), a, in))
		require.Equal(t, a.Credentials["base_url"], in.Credentials["base_url"])
		require.Equal(t, a.Extra["custom_base_url"], in.Extra["custom_base_url"])
		require.Equal(t, true, in.Extra["cache_ttl_override_enabled"])
	}
}
func TestOperatorAccountHeaderOverrideSentinelKeepsStored(t *testing.T) {
	s := &adminServiceImpl{}
	in := &UpdateAccountInput{Credentials: map[string]any{"header_overrides": map[string]any{"X-Relay": operatoraccess.HeaderMask}}}
	require.NoError(t, s.operatorAccountUpdateRules(operatorRulesContext(s), operatorStoredAccount(), in))
	require.Equal(t, "stored-header", in.Credentials["header_overrides"].(map[string]any)["X-Relay"])
}
func TestOperatorAccountDropsHeaderValuesOnAcceptedNetworkChange(t *testing.T) {
	s := &adminServiceImpl{}
	for _, headers := range []any{nil, map[string]any{"x-relay": operatoraccess.HeaderMask, "x-new": "new-header"}} {
		in := &UpdateAccountInput{ProxyID: operatorPtr(int64(2)), Credentials: map[string]any{"api_key": "new"}}
		if headers != nil {
			in.Credentials["header_overrides"] = headers
		}
		require.NoError(t, s.operatorAccountUpdateRules(operatorRulesContext(s, operatoraccess.FlagAccountsUpstreamAddress), operatorStoredAccount(), in))
		out := in.Credentials["header_overrides"].(map[string]any)
		require.NotContains(t, out, "x-relay")
		if headers != nil {
			require.Equal(t, "new-header", out["x-new"])
		}
	}
}
func TestOperatorAccountNewAPITokenTreatedLikeAPIKey(t *testing.T) {
	s := &adminServiceImpl{}
	a := operatorStoredAccount()
	a.Credentials["newapi_access_token"] = "stored-token"
	in := &UpdateAccountInput{ProxyID: operatorPtr(int64(2)), Credentials: map[string]any{"api_key": "new"}}
	ctx := operatorRulesContext(s, operatoraccess.FlagAccountsUpstreamAddress)
	operatorRuleError(t, s.operatorAccountUpdateRules(ctx, a, in), "ACCOUNT_API_KEY_REQUIRED", "credentials.newapi_access_token", "")
	in.Credentials["newapi_access_token"] = "new-token"
	require.NoError(t, s.operatorAccountUpdateRules(ctx, a, in))
}
func TestOperatorAccountRejectsInjectedAWSRegion(t *testing.T) {
	s := &adminServiceImpl{}
	for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagAccountsUpstreamAddress}} {
		ctx := operatorRulesContext(s, flags...)
		for _, raw := range []string{"x@evil.example/", "us-east-1@192.0.2.5:8443/#"} {
			operatorRuleError(t, s.operatorAccountCreateRules(ctx, &CreateAccountInput{Credentials: map[string]any{"aws_region": raw}}), "ADMIN_FIELD_FORBIDDEN", "credentials.aws_region", "invalid_region")
			for _, typ := range []string{"bedrock", "apikey", "oauth"} {
				a := operatorStoredAccount()
				a.Type = typ
				operatorRuleError(t, s.operatorAccountUpdateRules(ctx, a, &UpdateAccountInput{Type: "bedrock", Credentials: map[string]any{"aws_region": raw, "api_key": "new"}}), "ADMIN_FIELD_FORBIDDEN", "credentials.aws_region", "invalid_region")
			}
		}
	}
	require.NoError(t, s.operatorAccountCreateRules(operatorRulesContext(s), &CreateAccountInput{Credentials: map[string]any{"aws_region": "eu-central-1"}}))
}
func TestOperatorTokenRefreshRoutesStillPass(t *testing.T) {
	s := &adminServiceImpl{}
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		for _, typ := range OperatorRefreshableTypes {
			a := operatorStoredAccount()
			a.Platform = platform
			a.Type = typ
			ctx := operatorRulesContext(s)
			in := &UpdateAccountInput{Type: typ, Credentials: map[string]any{"access_token": "fresh", "refresh_token": "fresh-refresh"}}
			require.NoError(t, OperatorCheckOAuthApply(ctx, a, typ, in.Credentials, nil))
			require.NoError(t, s.operatorAccountUpdateRules(ctx, a, in))
		}
	}
}
func TestOperatorAccountBillingExtrasNeedFlag(t *testing.T) {
	s := &adminServiceImpl{}
	for _, key := range OperatorAccountBillingExtraKeys {
		ctx := operatorRulesContext(s)
		operatorRuleError(t, s.operatorAccountCreateRules(ctx, &CreateAccountInput{Extra: map[string]any{key: true}}), "ADMIN_FIELD_FORBIDDEN", "extra."+key, "")
		a := operatorStoredAccount()
		a.Extra[key] = false
		operatorRuleError(t, s.operatorAccountUpdateRules(ctx, a, &UpdateAccountInput{Extra: map[string]any{key: true}}), "ADMIN_FIELD_FORBIDDEN", "extra."+key, "")
		operatorRuleError(t, s.operatorAccountBulkRules(ctx, &BulkUpdateAccountsInput{Extra: map[string]any{key: true}}), "ADMIN_FIELD_FORBIDDEN", "extra."+key, "")
		require.NoError(t, s.operatorAccountCreateRules(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust), &CreateAccountInput{Extra: map[string]any{key: true}}))
	}
}
func TestOperatorAccountPlacementRules(t *testing.T) {
	s := &adminServiceImpl{}
	ctx := operatorRulesContext(s)
	in := &CreateAccountInput{}
	require.NoError(t, s.operatorAccountCreateRules(ctx, in))
	require.True(t, in.SkipDefaultGroupBind)
	operatorRuleError(t, s.operatorAccountCreateRules(ctx, &CreateAccountInput{GroupIDs: []int64{1}}), "ADMIN_FIELD_FORBIDDEN", "group_ids", "")
	a := operatorStoredAccount()
	require.NoError(t, s.operatorAccountUpdateRules(ctx, a, &UpdateAccountInput{GroupIDs: &[]int64{1, 1}}))
	operatorRuleError(t, s.operatorAccountUpdateRules(ctx, a, &UpdateAccountInput{GroupIDs: &[]int64{}}), "ADMIN_FIELD_FORBIDDEN", "group_ids", "")
}
func TestOperatorBulkUpdateRules(t *testing.T) {
	s := &adminServiceImpl{}
	for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagAccountsUpstreamAddress}} {
		ctx := operatorRulesContext(s, flags...)
		for _, path := range OperatorNetworkIdentityKeys {
			cred, extra := map[string]any{}, map[string]any{}
			m, k := operatorMapKey(path, cred, extra)
			if m == nil {
				continue
			}
			m[k] = nil
			operatorRuleError(t, s.operatorAccountBulkRules(ctx, &BulkUpdateAccountsInput{Credentials: cred, Extra: extra}), "OPERATOR_NETWORK_CHANGE_FORBIDDEN", path, "")
		}
		operatorRuleError(t, s.operatorAccountBulkRules(ctx, &BulkUpdateAccountsInput{ProxyID: operatorPtr(int64(0))}), "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "proxy_id", "bulk")
	}
	operatorRuleError(t, s.operatorAccountBulkRules(operatorRulesContext(s), &BulkUpdateAccountsInput{Credentials: map[string]any{"header_overrides": map[string]any{"x": operatoraccess.HeaderMask}}}), "ADMIN_FIELD_FORBIDDEN", "header_overrides", "")
	require.NoError(t, s.operatorAccountBulkRules(operatorRulesContext(s), &BulkUpdateAccountsInput{}))
}

type operatorAccountReadRepo struct {
	AccountRepository
	account *Account
	reads   int
	err     error
}

func (r *operatorAccountReadRepo) GetByID(context.Context, int64) (*Account, error) {
	r.reads++
	return r.account, r.err
}
func TestOperatorAccountExtraRules(t *testing.T) {
	r := &operatorAccountReadRepo{account: operatorStoredAccount()}
	s := &adminServiceImpl{accountRepo: r}
	for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagAccountsUpstreamAddress}} {
		ctx := operatorRulesContext(s, flags...)
		require.NoError(t, s.operatorAccountExtraRules(ctx, 1, map[string]any{"ordinary": true}))
		operatorRuleError(t, s.operatorAccountExtraRules(ctx, 1, map[string]any{"custom_base_url": "https://1.1.1.1"}), "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "extra.custom_base_url", "address")
		require.NoError(t, s.operatorAccountExtraRules(ctx, 1, map[string]any{"custom_base_url": r.account.Extra["custom_base_url"]}))
	}
	operatorRuleError(t, s.operatorAccountExtraRules(operatorRulesContext(s), 1, map[string]any{"cache_ttl_override_enabled": false}), "ADMIN_FIELD_FORBIDDEN", "extra.cache_ttl_override_enabled", "")
	r.err = ErrAccountNotFound
	require.ErrorIs(t, s.operatorAccountExtraRules(operatorRulesContext(s), 1, map[string]any{"custom_base_url": "https://8.8.8.8"}), ErrAccountNotFound)
	r.err = errors.New("database unavailable")
	operatorRuleError(t, s.operatorAccountExtraRules(operatorRulesContext(s), 1, map[string]any{"custom_base_url": "https://8.8.8.8"}), "OPERATOR_ACCESS_UNAVAILABLE", "", "")
}
func TestOperatorCheckOAuthApply(t *testing.T) {
	s := &adminServiceImpl{}
	a := operatorStoredAccount()
	a.Type = "oauth"
	ctx := operatorRulesContext(s)
	operatorRuleError(t, OperatorCheckOAuthApply(ctx, a, "setup-token", nil, nil), "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "type", "refreshable_account")
	for _, path := range OperatorNetworkIdentityKeys {
		cred, extra := map[string]any{}, map[string]any{}
		m, k := operatorMapKey(path, cred, extra)
		if m == nil {
			continue
		}
		m[k] = nil
		operatorRuleError(t, OperatorCheckOAuthApply(ctx, a, "oauth", cred, extra), "OPERATOR_NETWORK_CHANGE_FORBIDDEN", path, "")
	}
	require.NoError(t, OperatorCheckOAuthApply(ctx, a, "oauth", map[string]any{"access_token": "new"}, nil))
}
func operatorCheckGetterKeys(t *testing.T, file string, include func(string) bool, approved []string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	require.NoError(t, err)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !include(fn.Name.Name) {
			continue
		}
		if fn.Name.Name != "bedrockRuntimeRegion" {
			if fn.Recv == nil {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			id, ok := star.X.(*ast.Ident)
			if !ok || id.Name != "Account" {
				continue
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			key, prefix := "", ""
			switch x := n.(type) {
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && len(x.Args) > 0 && (sel.Sel.Name == "GetCredential" || sel.Sel.Name == "GetExtraString") {
					if lit, ok := x.Args[0].(*ast.BasicLit); ok {
						key, _ = strconv.Unquote(lit.Value)
						prefix = "credentials."
						if sel.Sel.Name == "GetExtraString" {
							prefix = "extra."
						}
					}
				}
			case *ast.IndexExpr:
				if sel, ok := x.X.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Credentials" || sel.Sel.Name == "Extra") {
					if lit, ok := x.Index.(*ast.BasicLit); ok {
						key, _ = strconv.Unquote(lit.Value)
						prefix = "credentials."
						if sel.Sel.Name == "Extra" {
							prefix = "extra."
						}
					}
				}
			}
			if key != "" && key != "aws_force_global" {
				require.Contains(t, approved, prefix+key, fn.Name.Name)
			}
			return true
		})
	}
}
func TestOperatorNetworkKeysCoverBaseURLGetters(t *testing.T) {
	include := func(name string) bool {
		return strings.Contains(name, "BaseURL") || strings.Contains(name, "APIProtocol") || strings.Contains(name, "AccountMode") || name == "bedrockRuntimeRegion"
	}
	operatorCheckGetterKeys(t, "account.go", include, OperatorNetworkIdentityKeys)
	operatorCheckGetterKeys(t, "bedrock_request.go", include, OperatorNetworkIdentityKeys)
}
func TestOperatorBillingExtraKeysMatchGetters(t *testing.T) {
	keys := []string{}
	for _, k := range OperatorAccountBillingExtraKeys {
		keys = append(keys, "extra."+k)
	}
	operatorCheckGetterKeys(t, "account.go", func(name string) bool { return strings.Contains(name, "Billing") || strings.Contains(name, "CacheTTL") }, keys)
}

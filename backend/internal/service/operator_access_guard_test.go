//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
)

type operatorGuardUsers struct {
	UserRepository
	rows  map[int64]*User
	reads int
	err   error
}

func (r *operatorGuardUsers) GetByIDIncludeDeleted(_ context.Context, id int64) (*User, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	if row := r.rows[id]; row != nil {
		return row, nil
	}
	return nil, ErrUserNotFound
}

type operatorGuardAPIKeys struct {
	APIKeyRepository
	key *APIKey
	err error
}

func (r *operatorGuardAPIKeys) GetByID(context.Context, int64) (*APIKey, error) { return r.key, r.err }

type operatorGuardSubscriptions struct {
	UserSubscriptionRepository
	rows map[int64]*UserSubscription
	err  error
}

func (r *operatorGuardSubscriptions) GetByIDIncludeDeleted(_ context.Context, id int64) (*UserSubscription, error) {
	if r.err != nil {
		return nil, r.err
	}
	row := r.rows[id]
	if row == nil {
		return nil, infraerrors.NotFound("SUBSCRIPTION_NOT_FOUND", "missing")
	}
	return row, nil
}

type operatorGuardRates struct {
	UserGroupRateRepository
	rows []UserGroupRateEntry
	err  error
}

func (r *operatorGuardRates) GetByGroupID(context.Context, int64) ([]UserGroupRateEntry, error) {
	return r.rows, r.err
}

type operatorGuardQuery struct {
	protected bool
	used      map[int64]bool
	queried   []int64
	err       error
}

func (r *operatorGuardQuery) GroupHasProtectedMembers(context.Context, int64, int64) (bool, error) {
	return r.protected, r.err
}
func (r *operatorGuardQuery) ProxyIDsUsedByAccounts(_ context.Context, ids []int64) ([]int64, error) {
	r.queried = append([]int64(nil), ids...)
	out := []int64{}
	for _, id := range ids {
		if r.used[id] {
			out = append(out, id)
		}
	}
	return out, r.err
}

type operatorGuardProxies struct {
	ProxyRepository
	rows []Proxy
	err  error
}

func (r *operatorGuardProxies) ListAllForFallback(context.Context) ([]Proxy, error) {
	return r.rows, r.err
}

type operatorGuardAdmin struct {
	AdminService
	proxy *Proxy
	err   error
}

func (r *operatorGuardAdmin) GetProxy(context.Context, int64) (*Proxy, error) { return r.proxy, r.err }

type operatorGuardChannel struct {
	ChannelRepository
	row *Channel
	err error
}

func (r *operatorGuardChannel) GetByID(context.Context, int64) (*Channel, error) { return r.row, r.err }

type operatorGuardMonitor struct {
	ChannelMonitorRepository
	row *ChannelMonitor
	err error
}

func (r *operatorGuardMonitor) GetByID(context.Context, int64) (*ChannelMonitor, error) {
	if r.row == nil {
		return nil, r.err
	}
	row := *r.row
	return &row, r.err
}

type operatorGuardTemplate struct {
	ChannelMonitorRequestTemplateRepository
	row *ChannelMonitorRequestTemplate
	err error
}

func (r *operatorGuardTemplate) GetByID(context.Context, int64) (*ChannelMonitorRequestTemplate, error) {
	return r.row, r.err
}

type operatorGuardRedeem struct {
	RedeemCodeRepository
	rows   map[int64]*RedeemCode
	byCode *RedeemCode
	err    error
}

func (r *operatorGuardRedeem) GetByID(_ context.Context, id int64) (*RedeemCode, error) {
	if r.err != nil {
		return nil, r.err
	}
	row := r.rows[id]
	if row == nil {
		return nil, ErrRedeemCodeNotFound
	}
	return row, nil
}
func (r *operatorGuardRedeem) GetByCode(context.Context, string) (*RedeemCode, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.byCode == nil {
		return nil, ErrRedeemCodeNotFound
	}
	return r.byCode, nil
}

type operatorGuardSettings struct {
	SettingRepository
	values map[string]string
	err    error
}

func (r *operatorGuardSettings) GetValue(_ context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	v, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (r *operatorGuardSettings) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return r.err
}

type operatorGuardFixture struct {
	svc      *OperatorAccessService
	sc       *operatoraccess.Scope
	users    *operatorGuardUsers
	query    *operatorGuardQuery
	proxies  *operatorGuardProxies
	admin    *operatorGuardAdmin
	channel  *operatorGuardChannel
	monitor  *operatorGuardMonitor
	template *operatorGuardTemplate
	redeem   *operatorGuardRedeem
	settings *operatorGuardSettings
}

func newOperatorGuardFixture(t *testing.T) *operatorGuardFixture {
	t.Helper()
	f := &operatorGuardFixture{}
	f.sc = &operatoraccess.Scope{UserID: 9, Perms: operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{}, Flags: map[operatoraccess.Flag]bool{}}}
	f.users = &operatorGuardUsers{rows: map[int64]*User{1: {ID: 1, Role: RoleUser}, 2: {ID: 2, Role: RoleAdmin}, 3: {ID: 3, Role: RoleOperator}, 9: {ID: 9, Role: RoleUser}}}
	f.query = &operatorGuardQuery{used: map[int64]bool{}}
	f.proxies = &operatorGuardProxies{}
	f.admin = &operatorGuardAdmin{proxy: &Proxy{ID: 10, Host: "203.0.113.10", Protocol: "http", Port: 8080, Status: StatusActive, FallbackMode: FallbackModeNone, Password: "stored-password"}}
	f.channel = &operatorGuardChannel{row: &Channel{ID: 10, GroupIDs: []int64{1, 2}, Status: StatusActive, BillingModelSource: BillingModelSourceRequested, ModelMapping: map[string]map[string]string{"openai": {"a": "b"}}}}
	f.monitor = &operatorGuardMonitor{row: &ChannelMonitor{ID: 10, Endpoint: "https://203.0.113.10", Provider: "openai", APIMode: "responses", ExtraHeaders: map[string]string{"x-secret": "stored", "x-omitted": "old"}}}
	f.template = &operatorGuardTemplate{row: &ChannelMonitorRequestTemplate{ID: 10, ExtraHeaders: map[string]string{"x-secret": "stored"}}}
	f.redeem = &operatorGuardRedeem{rows: map[int64]*RedeemCode{10: {ID: 10, Type: "balance", Status: "unused"}, 11: {ID: 11, Type: "balance", Status: "used"}, 12: {ID: 12, Type: "admin_balance", Status: "unused"}}}
	f.settings = &operatorGuardSettings{values: map[string]string{}}
	webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{})
	t.Cleanup(func() { webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{}) })
	f.svc = &OperatorAccessService{userRepo: f.users, queryRepo: f.query, proxyRepo: f.proxies, adminService: f.admin, channelService: &ChannelService{repo: f.channel}, monitorService: &ChannelMonitorService{repo: f.monitor}, templateService: &ChannelMonitorRequestTemplateService{repo: f.template}, redeemRepo: f.redeem, settingService: &SettingService{settingRepo: f.settings}, moderationService: &ContentModerationService{settingRepo: f.settings}, opsService: &OpsService{settingRepo: f.settings}}
	return f
}
func guardRequest(body string) operatoraccess.Request {
	return operatoraccess.Request{Method: "PUT", Key: "PUT /test/:id", Params: map[string]string{"id": "10"}, Body: []byte(body), Query: url.Values{}}
}
func (f *operatorGuardFixture) check(rule, body string) ([]byte, error) {
	return f.svc.CheckFields(context.Background(), f.sc, operatoraccess.FieldRule(rule), guardRequest(body))
}
func guardReason(t *testing.T, err error, reason, field string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.Reason(err))
	if field != "" {
		require.Equal(t, field, infraerrors.FromError(err).Metadata["field"])
	}
}
func TestOperatorTargetUsers(t *testing.T) {
	f := newOperatorGuardFixture(t)
	for _, id := range []int64{2, 3, 9} {
		r := guardRequest("")
		r.Params["id"] = strconv.FormatInt(id, 10)
		guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "user_path_id", r), "ADMIN_TARGET_FORBIDDEN", "")
	}
	deleted := time.Now()
	f.users.rows[1].DeletedAt = &deleted
	r := guardRequest(`{"User_ID":1}`)
	require.NoError(t, f.svc.CheckTarget(context.Background(), f.sc, "user_body_id", r))
	r.Params["id"] = "555"
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "user_path_id", r), "USER_NOT_FOUND", "")
	f.users.err = errors.New("db")
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "user_body_id", r), "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorTargetOwners(t *testing.T) {
	f := newOperatorGuardFixture(t)
	keys := &operatorGuardAPIKeys{key: &APIKey{UserID: 1}}
	f.svc.apiKeyRepo = keys
	subs := &operatorGuardSubscriptions{rows: map[int64]*UserSubscription{10: {UserID: 1}, 11: {UserID: 2}}}
	f.svc.subscriptionRepo = subs
	for _, rule := range []operatoraccess.TargetRule{"apikey_owner", "sub_owner", "sub_body_owners"} {
		require.NoError(t, f.svc.CheckTarget(context.Background(), f.sc, rule, guardRequest(`{"subscription_ids":[10,10]}`)))
	}
	keys.key.UserID = 2
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "apikey_owner", guardRequest("")), "ADMIN_TARGET_FORBIDDEN", "")
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "sub_body_owners", guardRequest(`{"Subscription_IDs":[10,11]}`)), "ADMIN_TARGET_FORBIDDEN", "")
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "sub_body_owners", guardRequest(`{"subscription_ids":[999]}`)), "SUBSCRIPTION_NOT_FOUND", "")
	f.svc.paymentService = nil
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "order_owner", guardRequest("")), "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorTargetBatchLimit(t *testing.T) {
	f := newOperatorGuardFixture(t)
	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 100)
		f.users.rows[ids[i]] = &User{ID: ids[i], Role: RoleUser}
	}
	raw, _ := json.Marshal(map[string]any{"user_ids": ids})
	err := f.svc.CheckTarget(context.Background(), f.sc, "user_body_ids", guardRequest(string(raw)))
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "user_ids")
	require.Equal(t, "1000", infraerrors.FromError(err).Metadata["max"])
	require.Zero(t, f.users.reads)
	raw, _ = json.Marshal(map[string]any{"user_ids": ids[:1000]})
	require.NoError(t, f.svc.CheckTarget(context.Background(), f.sc, "user_body_ids", guardRequest(string(raw))))
	f.users.reads = 0
	ids = make([]int64, 1001)
	for i := range ids {
		ids[i] = 1
	}
	raw, _ = json.Marshal(map[string]any{"User_IDs": ids})
	require.NoError(t, f.svc.CheckTarget(context.Background(), f.sc, "user_body_ids", guardRequest(string(raw))))
	require.Equal(t, 1, f.users.reads)
}
func TestOperatorGroupRateTargets(t *testing.T) {
	f := newOperatorGuardFixture(t)
	rpm := 1
	rates := &operatorGuardRates{rows: []UserGroupRateEntry{{UserID: 1, RPMOverride: &rpm}, {UserID: 2}}}
	f.svc.userGroupRateRepo = rates
	require.NoError(t, f.svc.CheckTarget(context.Background(), f.sc, "group_rpm_rows", guardRequest(`{"entries":[{"User_ID":1}]}`)))
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "group_rate_rows", guardRequest("{}")), "ADMIN_TARGET_FORBIDDEN", "")
	rates.rows[1].RPMOverride = &rpm
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "group_rpm_rows", guardRequest("{}")), "ADMIN_TARGET_FORBIDDEN", "")
	rates.err = errors.New("db")
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "group_rate_rows", guardRequest("{}")), "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorDeleteGroupWithProtectedMembers(t *testing.T) {
	f := newOperatorGuardFixture(t)
	require.NoError(t, f.svc.CheckTarget(context.Background(), f.sc, "group_members", guardRequest("")))
	f.query.protected = true
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "group_members", guardRequest("")), "ADMIN_TARGET_FORBIDDEN", "")
	f.query.err = errors.New("db")
	guardReason(t, f.svc.CheckTarget(context.Background(), f.sc, "group_members", guardRequest("")), "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorAllowedGroupsNeedBalanceFlag(t *testing.T) { // Money and placement are intentionally service-enforced in B8.
	for _, rule := range []operatoraccess.FieldRule{"user_create", "user_update", "user_replace_group", "apikey_group", "group_create", "group_update"} {
		require.True(t, operatoraccess.ServiceEnforcedFields[rule])
	}
	f := newOperatorGuardFixture(t)
	_, err := f.check("user_create", `{"allowed_groups":[1]}`)
	guardReason(t, err, "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorBatchNoAll(t *testing.T) {
	f := newOperatorGuardFixture(t)
	_, err := f.check("user_batch_no_all", `{"All":true}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "all")
	for _, body := range []string{`{}`, `{"all":false}`, `{"all":null}`} {
		_, err = f.check("user_batch_no_all", body)
		require.NoError(t, err)
	}
}
func TestOperatorChannelRules(t *testing.T) {
	f := newOperatorGuardFixture(t)
	for _, tc := range []struct{ rule, body, field string }{{"channel_create", `{"group_ids":[1]}`, "group_ids"}, {"channel_update", `{"group_ids":[1]}`, "group_ids"}, {"channel_update", `{"model_mapping":{}}`, "model_mapping"}, {"channel_update", `{"model_pricing":[{"models":["a"],"input_price":1}]}`, "model_pricing"}, {"channel_update", `{"billing_model_source":"upstream"}`, "billing_model_source"}, {"channel_update", `{"status":"disabled"}`, "status"}} {
		_, err := f.check(tc.rule, tc.body)
		guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", tc.field)
	}
	for _, body := range []string{`{"group_ids":[2,1,1]}`, `{"status":"active","billing_model_source":"requested"}`, `{"model_mapping":{"openai":{"a":"b"}}}`, `{"model_pricing":[]}`, `{"model_pricing":null}`} {
		_, err := f.check("channel_update", body)
		require.NoError(t, err)
	}
	f.channel.row.GroupIDs = nil
	_, err := f.check("channel_update", `{"model_pricing":[],"status":"disabled"}`)
	require.NoError(t, err)
	f.sc.Perms.Flags[operatoraccess.FlagUsersBalanceAdjust] = true
	_, err = f.check("channel_create", `{"group_ids":[1]}`)
	require.NoError(t, err)
}
func TestOperatorMonitorRules(t *testing.T) {
	f := newOperatorGuardFixture(t)
	_, err := f.check("monitor_create", `{"account_id":1}`)
	guardReason(t, err, "ADMIN_PERMISSION_DENIED", "")
	f.sc.Perms.Modules[operatoraccess.ModuleAccounts] = operatoraccess.LevelView
	_, err = f.check("monitor_create", `{"account_id":1}`)
	require.NoError(t, err)
	_, err = f.check("monitor_create", `{"extra_headers":{"x":"******"}}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "extra_headers")
	_, err = f.check("monitor_create", `{"endpoint":"https://127.0.0.1"}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "endpoint")
	_, err = f.check("monitor_create", `{"endpoint":"https://203.0.113.10"}`)
	require.NoError(t, err)
	raw, err := f.check("monitor_update", `{"Extra_Headers":{"x-secret":"******","missing":"******"}}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"endpoint":"https://203.0.113.10","extra_headers":{"x-secret":"stored"}}`, string(raw))
	_, err = f.check("monitor_update", `{"endpoint":"https://203.0.113.11","api_key":"new"}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "endpoint")
	raw, err = f.check("monitor_template_update", `{"extra_headers":{"x-secret":"******"}}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"extra_headers":{"x-secret":"stored"}}`, string(raw))
}
func TestOperatorMonitorEndpointChangeDropsStoredHeaders(t *testing.T) {
	f := newOperatorGuardFixture(t)
	f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
	for _, body := range []string{`{"endpoint":"https://203.0.113.11"}`, `{"provider":"anthropic"}`, `{"api_mode":"chat_completions"}`} {
		_, err := f.check("monitor_update", body)
		guardReason(t, err, "ACCOUNT_API_KEY_REQUIRED", "api_key")
	}
	raw, err := f.check("monitor_update", `{"endpoint":"https://203.0.113.11","api_key":"new","extra_headers":{"x-secret":"******","x-new":"fresh"}}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"endpoint":"https://203.0.113.11","api_key":"new","extra_headers":{"x-new":"fresh"}}`, string(raw))
	raw, err = f.check("monitor_update", `{"provider":"anthropic","api_key":"new"}`)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"extra_headers":{}`)
	require.NotContains(t, string(raw), "stored")
}
func TestOperatorTemplateApplyWithHeadersIsSuperOnly(t *testing.T) {
	f := newOperatorGuardFixture(t)
	_, err := f.check("monitor_template_apply", "{}")
	guardReason(t, err, "SUPER_ADMIN_REQUIRED", "")
	f.template.row.ExtraHeaders = nil
	_, err = f.check("monitor_template_apply", "{}")
	require.NoError(t, err)
	f.template.err = errors.New("db")
	_, err = f.check("monitor_template_apply", "{}")
	guardReason(t, err, "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorProxyRules(t *testing.T) {
	f := newOperatorGuardFixture(t)
	f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
	f.query.used[10] = true
	later := time.Now().Add(time.Hour).Truncate(time.Second)
	f.admin.proxy.ExpiresAt = &later
	for _, tc := range []struct{ body, field string }{{`{"port":8081}`, "port"}, {`{"status":"inactive"}`, "status"}, {fmt.Sprintf(`{"expires_at":%d}`, later.Unix()-1), "expires_at"}} {
		_, err := f.check("proxy_update", tc.body)
		guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", tc.field)
	}
	for _, body := range []string{`{"name":"new"}`, `{"password":"new"}`, `{"expires_at":null}`, `{"expires_at":0}`, `{"expires_at":-1}`, fmt.Sprintf(`{"expires_at":%d}`, later.Unix()+1)} {
		_, err := f.check("proxy_update", body)
		require.NoError(t, err)
	}
	f.admin.proxy.ExpiresAt = nil
	_, err := f.check("proxy_update", fmt.Sprintf(`{"expires_at":%d}`, later.Unix()))
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "expires_at")
	delete(f.query.used, 10)
	_, err = f.check("proxy_update", `{"host":"203.0.113.11"}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "password")
	require.Equal(t, "password", infraerrors.FromError(err).Metadata["requires"])
	_, err = f.check("proxy_update", `{"host":"203.0.113.11","password":"new"}`)
	require.NoError(t, err)
}
func TestOperatorProxyInUseIncludesChainOriginAndSettings(t *testing.T) {
	for _, source := range []string{"account", "origin", "web", "moderation", "engine", "chain", "cycle"} {
		t.Run(source, func(t *testing.T) {
			f := newOperatorGuardFixture(t)
			f.proxies.rows = []Proxy{{ID: 20, FallbackMode: FallbackModeProxy, BackupProxyID: ptrInt64(10)}, {ID: 30, FallbackMode: FallbackModeProxy, BackupProxyID: ptrInt64(20)}}
			switch source {
			case "account", "origin":
				f.query.used[10] = true
			case "web":
				f.settings.values[SettingKeyWebSearchEmulationConfig] = `{"providers":[{"type":"brave","proxy_id":10}]}`
			case "moderation":
				f.settings.values[SettingKeyContentModerationConfig] = `{"proxy_id":10}`
			case "engine":
				f.settings.values[SettingKeyContentModerationConfig] = `{"typesafe":{"proxy_id":10}}`
			case "chain":
				f.query.used[30] = true
			case "cycle":
				f.proxies.rows = append(f.proxies.rows, Proxy{ID: 10, FallbackMode: FallbackModeProxy, BackupProxyID: ptrInt64(30)})
				f.query.used[20] = true
			}
			used, err := f.svc.operatorProxyInUse(context.Background(), 10)
			require.NoError(t, err)
			require.True(t, used)
			if source == "chain" {
				require.ElementsMatch(t, []int64{10, 20, 30}, f.query.queried)
			}
		})
	}
	f := newOperatorGuardFixture(t)
	f.query.err = errors.New("db")
	_, err := f.check("proxy_update", `{"name":"edit"}`)
	guardReason(t, err, "OPERATOR_ACCESS_UNAVAILABLE", "")
	f.query.err = nil
	f.proxies.err = errors.New("db")
	_, err = f.check("proxy_update", `{}`)
	guardReason(t, err, "OPERATOR_ACCESS_UNAVAILABLE", "")
	f.proxies.err = nil
	f.settings.err = errors.New("settings")
	_, err = f.check("proxy_update", `{}`)
	guardReason(t, err, "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorProxyDeleteRespectsInUseSet(t *testing.T) {
	for _, flag := range []bool{false, true} {
		for _, source := range []string{"direct", "origin", "web", "engine", "chain"} {
			for _, batch := range []bool{false, true} {
				t.Run(fmt.Sprintf("%t/%s/%t", flag, source, batch), func(t *testing.T) {
					f := newOperatorGuardFixture(t)
					f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = flag
					switch source {
					case "direct", "origin":
						f.query.used[10] = true
					case "web":
						f.settings.values[SettingKeyWebSearchEmulationConfig] = `{"providers":[{"type":"brave","proxy_id":10}]}`
					case "engine":
						f.settings.values[SettingKeyContentModerationConfig] = `{"typesafe":{"proxy_id":10}}`
					case "chain":
						f.proxies.rows = []Proxy{{ID: 20, FallbackMode: FallbackModeProxy, BackupProxyID: ptrInt64(10)}}
						f.query.used[20] = true
					}
					r := guardRequest("{}")
					field := "id"
					if batch {
						r.Params = nil
						r.Body = []byte(`{"IDs":[10,11]}`)
						field = "ids"
					}
					_, err := f.svc.CheckFields(context.Background(), f.sc, "proxy_delete", r)
					guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", field)
					require.Equal(t, "proxy_in_use", infraerrors.FromError(err).Metadata["cause"])
					require.Equal(t, "10", infraerrors.FromError(err).Metadata["id"])
				})
			}
		}
	}
	f := newOperatorGuardFixture(t)
	_, err := f.check("proxy_delete", `{}`)
	require.NoError(t, err)
	f.query.err = errors.New("db")
	_, err = f.check("proxy_delete", `{}`)
	guardReason(t, err, "OPERATOR_ACCESS_UNAVAILABLE", "")
}
func TestOperatorProxyNetworkFieldsFollowUpstreamAddressFlag(t *testing.T) {
	operatorTestDNS(t)
	f := newOperatorGuardFixture(t)
	_, err := f.check("proxy_create", `{"host":"203.0.113.10"}`)
	guardReason(t, err, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "host")
	for _, tc := range []struct{ body, field string }{{`{"protocol":"https"}`, "protocol"}, {`{"host":"203.0.113.11"}`, "host"}, {`{"port":8081}`, "port"}, {`{"username":"new"}`, "username"}, {`{"password":"new"}`, "password"}, {`{"fallback_mode":"direct"}`, "fallback_mode"}, {`{"backup_proxy_id":20}`, "backup_proxy_id"}} {
		_, err = f.check("proxy_update", tc.body)
		guardReason(t, err, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", tc.field)
	}
	_, err = f.check("proxy_update", `{"host":"203.0.113.10","port":8080,"protocol":"http","name":"ok"}`)
	require.NoError(t, err)
	f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
	_, err = f.check("proxy_create", `{"host":"203.0.113.10"}`)
	require.NoError(t, err)
	for _, host := range []string{"127.0.0.1", "10.1.2.3", "169.254.1.1", "100.64.1.1", "::1", "fc00::1", "metadata.google.internal", "private.operator.test", "link.operator.test", "empty.operator.test", "failure.operator.test"} {
		_, err = f.check("proxy_create", fmt.Sprintf(`{"host":%q}`, host))
		guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "host")
		require.Equal(t, "private_address", infraerrors.FromError(err).Metadata["cause"])
	}
	_, err = f.check("proxy_create", `{"proxies":[{"host":"203.0.113.10"},{"host":"127.0.0.1"}]}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "host")
}
func TestOperatorRedeemRules(t *testing.T) {
	f := newOperatorGuardFixture(t)
	for _, tc := range []struct {
		query url.Values
		field string
	}{{url.Values{"search": {"abc"}}, "search"}, {url.Values{"sort_by": {"code"}}, "sort_by"}} {
		r := guardRequest("")
		r.Query = tc.query
		_, err := f.svc.CheckFields(context.Background(), f.sc, "redeem_search", r)
		guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", tc.field)
	}
	r := guardRequest("")
	r.Query = url.Values{"search": {"u@example.com"}}
	_, err := f.svc.CheckFields(context.Background(), f.sc, "redeem_search", r)
	require.NoError(t, err)
	_, err = f.check("redeem_safe_rows", `{}`)
	require.NoError(t, err)
	r.Params = nil
	r.Body = []byte(`{"ids":[10,11]}`)
	_, err = f.svc.CheckFields(context.Background(), f.sc, "redeem_safe_rows", r)
	guardReason(t, err, "SUPER_ADMIN_REQUIRED", "")
	for _, body := range []string{`{"type":"invitation"}`, `{"code":"exists"}`} {
		f.redeem.byCode = &RedeemCode{}
		_, err = f.check("redeem_create_and_redeem", body)
		require.Equal(t, "ADMIN_FIELD_FORBIDDEN", infraerrors.Reason(err))
	}
	f.redeem.byCode = nil
	_, err = f.check("redeem_create_and_redeem", `{"type":"balance","code":"new"}`)
	require.NoError(t, err)
}
func TestOperatorRedeemBatchUpdateRejectsLedgerAndAdminRows(t *testing.T) {
	f := newOperatorGuardFixture(t)
	f.redeem.rows[11].UsedBy = ptrInt64(2)
	for _, ids := range []string{`[10,12]`, `[10,11]`} {
		_, err := f.check("redeem_batch_rows", `{"ids":`+ids+`,"fields":{"notes":"only"}}`)
		if ids == `[10,12]` {
			guardReason(t, err, "SUPER_ADMIN_REQUIRED", "")
		} else {
			guardReason(t, err, "ADMIN_TARGET_FORBIDDEN", "")
		}
	}
	f.redeem.rows[11].UsedBy = ptrInt64(1)
	_, err := f.check("redeem_batch_rows", `{"IDs":[10,11]}`)
	require.NoError(t, err)
}
func TestOperatorPromoSearchRule(t *testing.T) {
	f := newOperatorGuardFixture(t)
	r := guardRequest("")
	r.Query = url.Values{"search": {"email@example.com"}}
	_, err := f.svc.CheckFields(context.Background(), f.sc, "promo_search", r)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "search")
	f.sc.Perms.Flags[operatoraccess.FlagRedeemCodesIssue] = true
	_, err = f.svc.CheckFields(context.Background(), f.sc, "promo_search", r)
	require.NoError(t, err)
}
func TestOperatorSubscriptionAndAffiliateRules(t *testing.T) {
	f := newOperatorGuardFixture(t)
	for _, action := range []string{"extend", "restore", "reset_quota"} {
		_, err := f.check("sub_bulk_action", fmt.Sprintf(`{"action":%q}`, action))
		guardReason(t, err, "ADMIN_PERMISSION_DENIED", "")
	}
	_, err := f.check("sub_bulk_action", `{"action":"revoke"}`)
	require.NoError(t, err)
	for _, body := range []string{`{"clear_rebate_rate":false}`, `{"aff_rebate_rate_percent":null}`} {
		_, err = f.check("aff_rate", body)
		guardReason(t, err, "ADMIN_PERMISSION_DENIED", "")
	}
	_, err = f.check("aff_rate", `{"aff_code":"new"}`)
	require.NoError(t, err)
	f.sc.Perms.Flags[operatoraccess.FlagUsersBalanceAdjust] = true
	_, err = f.check("aff_rate", `{"clear_rebate_rate":true}`)
	require.NoError(t, err)
	_, err = f.check("codex_ticket_proxy", `{"proxy_url":"http://203.0.113.1"}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "proxy_url")
	_, err = f.check("codex_ticket_proxy", `{"proxy_url":""}`)
	require.NoError(t, err)
}
func TestOperatorRiskAndOpsAlertRules(t *testing.T) {
	f := newOperatorGuardFixture(t)
	f.settings.values[SettingKeyContentModerationConfig] = `{"base_url":"https://203.0.113.12"}`
	for _, tc := range []struct{ body, field string }{{`{"api_key":"new"}`, "api_key"}, {`{"api_keys":["new"]}`, "api_keys"}, {`{"api_keys_mode":"replace"}`, "api_keys_mode"}, {`{"delete_api_key_hashes":["x"]}`, "delete_api_key_hashes"}, {`{"clear_api_key":true}`, "clear_api_key"}, {`{"base_url":"https://203.0.113.10"}`, "base_url"}, {`{"base_url":"http://127.0.0.1"}`, "base_url"}, {`{"model":"changed"}`, "model"}, {`{"proxy_id":10}`, "proxy_id"}, {`{"engine_configs":{"typesafe":{"api_key":"new"}}}`, "engine_configs"}} {
		_, err := f.check("risk_config", tc.body)
		guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", tc.field)
	}
	_, err := f.check("risk_config", `{"api_key":"","api_keys":[],"clear_api_key":false,"hit_retention_days":7}`)
	require.NoError(t, err)
	stored, err := f.svc.moderationService.GetConfig(context.Background())
	require.NoError(t, err)
	raw, _ := json.Marshal(map[string]any{"base_url": stored.BaseURL, "model": stored.Model})
	_, err = f.check("risk_config", string(raw))
	require.NoError(t, err)
	ops, err := f.svc.opsService.GetOpsAlertRuntimeSettings(context.Background())
	require.NoError(t, err)
	raw, _ = json.Marshal(map[string]any{"distributed_lock": ops.DistributedLock})
	_, err = f.check("ops_runtime_alert", string(raw))
	require.NoError(t, err)
	rewritten, err := f.check("ops_runtime_alert", `{"evaluation_interval_seconds":60}`)
	require.NoError(t, err)
	var decoded OpsAlertRuntimeSettings
	require.NoError(t, json.Unmarshal(rewritten, &decoded))
	require.Equal(t, ops.DistributedLock, decoded.DistributedLock)
	ops.DistributedLock.Enabled = !ops.DistributedLock.Enabled
	raw, _ = json.Marshal(map[string]any{"distributed_lock": ops.DistributedLock})
	_, err = f.check("ops_runtime_alert", string(raw))
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "distributed_lock")
}
func TestOperatorPreviewBaseURLFollowsUpstreamAddressFlag(t *testing.T) {
	f := newOperatorGuardFixture(t)
	_, err := f.check("account_url_probe", `{"base_url":"https://203.0.113.10"}`)
	guardReason(t, err, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "base_url")
	_, err = f.check("account_url_probe", `{}`)
	require.NoError(t, err)
	f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
	_, err = f.check("account_url_probe", `{"base_url":"https://203.0.113.10"}`)
	require.NoError(t, err)
	_, err = f.check("account_url_probe", `{"base_url":"https://10.0.0.1"}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "base_url")
}
func TestOperatorImportAddressRules(t *testing.T) {
	f := newOperatorGuardFixture(t)
	for _, body := range []string{`{"credentials":{"Base_URL":"https://203.0.113.10"}}`, `{"extra":{"custom_base_url":"https://203.0.113.10"}}`, `{"extra":{"nested":[[{"Base_URL":"https://203.0.113.10"}]]}}`, `{"items":[{"credentials":{"api_base_urls":{"responses":"https://203.0.113.10"}}}]}`} {
		_, err := f.check("account_import_address", body)
		guardReason(t, err, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "")
		f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
		_, err = f.check("account_import_address", body)
		require.NoError(t, err)
		f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = false
	}
	f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
	_, err := f.check("account_import_address", `{"credentials":{"api_base_urls":{"responses":"http://127.0.0.1"}}}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "")
}

func TestOperatorOrderOwnerUsesStoredUser(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	f := newOperatorGuardFixture(t)
	f.svc.paymentService = &PaymentService{entClient: client}
	for _, userID := range []int64{1, 2, 3, 9} {
		user, err := client.User.Create().SetEmail(fmt.Sprintf("owner-%d@example.com", userID)).SetPasswordHash("unit").SetUsername("Owner").Save(ctx)
		require.NoError(t, err)
		f.users.rows[user.ID] = &User{ID: user.ID, Role: f.users.rows[userID].Role}
		if userID == 9 {
			f.sc.UserID = user.ID
		}
		order, err := client.PaymentOrder.Create().SetUserID(user.ID).
			SetUserEmail("owner@example.com").SetUserName("Owner").
			SetAmount(10).SetPayAmount(10).SetRechargeCode(fmt.Sprintf("operator-%d", userID)).
			SetPaymentType("test").SetPaymentTradeNo("").
			SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("192.0.2.1").SetSrcHost("example.com").Save(ctx)
		require.NoError(t, err)
		r := guardRequest("")
		r.Params["id"] = strconv.FormatInt(order.ID, 10)
		err = f.svc.CheckTarget(ctx, f.sc, "order_owner", r)
		if userID == 1 {
			require.NoError(t, err)
		} else {
			guardReason(t, err, "ADMIN_TARGET_FORBIDDEN", "")
		}
	}
	r := guardRequest("")
	r.Params["id"] = "999999"
	guardReason(t, f.svc.CheckTarget(ctx, f.sc, "order_owner", r), "NOT_FOUND", "")
}

func TestOperatorChannelStoredPricingEquality(t *testing.T) {
	f := newOperatorGuardFixture(t)
	price, maximum := 1.25, 8192
	f.channel.row.ModelPricing = []ChannelModelPricing{{
		ID: 5, ChannelID: 10, Platform: "openai", Models: []string{"m"}, BillingMode: BillingModeToken,
		InputPrice: &price, FastMultiplier: &price, ReasoningEffortMultipliers: map[string]float64{"high": 1.5},
		Intervals:   []PricingInterval{{ID: 3, PricingID: 5, MinTokens: 0, MaxTokens: &maximum, InputPrice: &price}},
		TimePricing: &ChannelTimePricing{Timezone: "UTC", WeekdaysOnly: true, Periods: []ChannelTimePricingPeriod{{StartTime: "09:00", EndTime: "18:00", Multiplier: 1.5}}},
	}}
	raw, err := json.Marshal(map[string]any{"model_pricing": f.channel.row.ModelPricing})
	require.NoError(t, err)
	_, err = f.check("channel_update", string(raw))
	require.NoError(t, err)
	// Database IDs do not participate, but every pricing leaf does.
	changed := append([]ChannelModelPricing(nil), f.channel.row.ModelPricing...)
	changed[0].TimePricing = &ChannelTimePricing{Timezone: "UTC", WeekdaysOnly: false}
	raw, err = json.Marshal(map[string]any{"model_pricing": changed})
	require.NoError(t, err)
	_, err = f.check("channel_update", string(raw))
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "model_pricing")
}

func TestOperatorMonitorEmptyEndpointAndBlankKey(t *testing.T) {
	f := newOperatorGuardFixture(t)
	raw, err := f.check("monitor_update", `{"endpoint":""}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"endpoint":"https://203.0.113.10"}`, string(raw))
	f.sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = true
	_, err = f.check("monitor_update", `{"endpoint":"","api_key":"  "}`)
	guardReason(t, err, "ACCOUNT_API_KEY_REQUIRED", "api_key")
	raw, err = f.check("monitor_update", `{"endpoint":"","api_key":"new"}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"endpoint":"","api_key":"new","extra_headers":{}}`, string(raw))
	f.monitor.row.Endpoint = "http://127.0.0.1"
	_, err = f.check("monitor_update", `{"endpoint":"http://127.0.0.1"}`)
	guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "endpoint")
}

func TestOperatorRiskEngineConfigEquality(t *testing.T) {
	f := newOperatorGuardFixture(t)
	f.settings.values[SettingKeyContentModerationConfig] = `{"base_url":"https://203.0.113.12"}`
	stored, err := f.svc.moderationService.GetConfig(context.Background())
	require.NoError(t, err)
	old := stored.EngineConfigs[ContentModerationEngineOpenAI]
	require.NotNil(t, old)
	input := UpdateContentModerationEngineInput{BaseURL: &old.BaseURL, Model: &old.Model, ProxyID: old.ProxyID, TimeoutMS: &old.TimeoutMS, RetryCount: &old.RetryCount, Thresholds: &old.Thresholds}
	raw, err := json.Marshal(map[string]any{"engine_configs": map[string]UpdateContentModerationEngineInput{"openai": input}})
	require.NoError(t, err)
	_, err = f.check("risk_config", string(raw))
	require.NoError(t, err)
	for _, body := range []string{`{"engine_configs":{"openai":{"timeout_ms":999}}}`, `{"engine_configs":{"openai":{"retry_count":999}}}`, `{"engine_configs":{"openai":{"thresholds":{"test":0.1}}}}`, `{"engine_configs":{"unknown":{}}}`} {
		_, err = f.check("risk_config", body)
		guardReason(t, err, "ADMIN_FIELD_FORBIDDEN", "engine_configs")
	}
}

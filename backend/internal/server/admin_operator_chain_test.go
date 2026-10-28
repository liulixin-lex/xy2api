//go:build unit

package server_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/authidentity"
	_ "github.com/liulixin-lex/xy2api/ent/runtime"
	"github.com/liulixin-lex/xy2api/internal/config"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/pkg/geminicli"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type chainWrites struct {
	mu     sync.Mutex
	calls  []string
	actors []operatoraccess.Actor
}

func (w *chainWrites) record(ctx context.Context, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, name)
	w.actors = append(w.actors, operatoraccess.ActorFrom(ctx))
}
func (w *chainWrites) empty(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Empty(t, w.calls)
}

type chainUsers struct {
	*stubUserRepo
	w              *chainWrites
	searchOperator bool
}

func (r *chainUsers) Create(ctx context.Context, u *service.User) error {
	r.w.record(ctx, "user.create")
	u.ID = 20
	r.users[u.ID] = u
	return nil
}
func (r *chainUsers) Update(ctx context.Context, u *service.User, _ service.UserUpdateFields) error {
	r.w.record(ctx, "user.update")
	r.users[u.ID] = u
	return nil
}
func (r *chainUsers) GetByID(ctx context.Context, id int64) (*service.User, error) {
	u, err := r.stubUserRepo.GetByID(ctx, id)
	if err == nil && u.DeletedAt != nil {
		return nil, service.ErrUserNotFound
	}
	return u, err
}
func (r *chainUsers) GetByIDIncludeDeleted(ctx context.Context, id int64) (*service.User, error) {
	return r.stubUserRepo.GetByID(ctx, id)
}
func (r *chainUsers) BatchSetConcurrency(ctx context.Context, _ []int64, _ int) (int, error) {
	r.w.record(ctx, "user.batch")
	return 1, nil
}
func (r *chainUsers) ListWithFilters(ctx context.Context, _ pagination.PaginationParams, _ service.UserListFilters) ([]service.User, *pagination.PaginationResult, error) {
	r.searchOperator = operatoraccess.HideAPIKeySearch(ctx)
	return []service.User{}, &pagination.PaginationResult{}, nil
}
func (r *chainUsers) AddGroupToAllowedGroups(ctx context.Context, _ int64, _ int64) error {
	r.w.record(ctx, "user.grant")
	return nil
}

type chainAccounts struct {
	stubAccountRepo
	w      *chainWrites
	row    *service.Account
	shadow *service.Account
	outbox []int64
}

func (r *chainAccounts) GetByID(context.Context, int64) (*service.Account, error) {
	a := *r.row
	a.Credentials = maps.Clone(a.Credentials)
	a.Extra = maps.Clone(a.Extra)
	a.GroupIDs = append([]int64{}, a.GroupIDs...)
	return &a, nil
}
func (r *chainAccounts) GetByIDs(ctx context.Context, ids []int64) ([]*service.Account, error) {
	a, e := r.GetByID(ctx, 1)
	return []*service.Account{a}, e
}
func (r *chainAccounts) Create(ctx context.Context, a *service.Account) error {
	r.w.record(ctx, "account.create")
	a.ID = 10
	r.row = a
	return nil
}
func (r *chainAccounts) FindByExtraField(context.Context, string, any) ([]service.Account, error) {
	return nil, nil
}
func (r *chainAccounts) CreateWithAccountGroups(ctx context.Context, a *service.Account, _ []service.AccountGroup) error {
	return r.Create(ctx, a)
}
func (r *chainAccounts) Update(ctx context.Context, a *service.Account) error {
	r.w.record(ctx, "account.update")
	r.outbox = append(r.outbox, a.ID)
	if r.shadow != nil && a.ID == r.shadow.ID {
		r.shadow = a
		return nil
	}
	r.row = a
	return nil
}
func (r *chainAccounts) UpdateWithAccountBillingSettings(ctx context.Context, a *service.Account, _ *bool, _ *bool, _ *float64) error {
	return r.Update(ctx, a)
}
func (r *chainAccounts) UpdateExtra(ctx context.Context, _ int64, extra map[string]any) error {
	r.w.record(ctx, "account.extra")
	maps.Copy(r.row.Extra, extra)
	return nil
}
func (r *chainAccounts) BindGroups(ctx context.Context, _ int64, ids []int64) error {
	r.w.record(ctx, "account.bind")
	r.row.GroupIDs = ids
	return nil
}
func (r *chainAccounts) BulkUpdate(ctx context.Context, _ []int64, _ service.AccountBulkUpdate) (int64, error) {
	r.w.record(ctx, "account.bulk")
	return 1, nil
}
func (r *chainAccounts) ListWithFilters(_ context.Context, p pagination.PaginationParams, _, _, _, _ string, _ int64, _ string) ([]service.Account, *pagination.PaginationResult, error) {
	return []service.Account{*r.row}, &pagination.PaginationResult{Total: 1}, nil
}
func (r *chainAccounts) ListShadowsByParent(context.Context, int64) ([]*service.Account, error) {
	if r.shadow != nil {
		shadow := *r.shadow
		return []*service.Account{&shadow}, nil
	}
	return nil, nil
}
func (r *chainAccounts) RevertProxyFallback(ctx context.Context, _ int64) error {
	r.w.record(ctx, "account.revert")
	r.outbox = append(r.outbox, r.row.ID)
	r.row.ProxyID = r.row.ProxyFallbackOriginID
	r.row.ProxyFallbackOriginID = nil
	return nil
}

type chainGroups struct {
	*stubGroupRepo
	w        *chainWrites
	row      *service.Group
	nonempty bool
}

func (r *chainGroups) GetByID(context.Context, int64) (*service.Group, error) {
	g := *r.row
	return &g, nil
}
func (r *chainGroups) GetByIDLite(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByID(ctx, id)
}
func (r *chainGroups) GetByIDs(context.Context, []int64) ([]service.Group, error) {
	return []service.Group{*r.row}, nil
}
func (r *chainGroups) Create(ctx context.Context, g *service.Group) error {
	r.w.record(ctx, "group.create")
	g.ID = 1
	r.row = g
	return nil
}
func (r *chainGroups) Update(ctx context.Context, g *service.Group) error {
	r.w.record(ctx, "group.update")
	r.row = g
	return nil
}
func (r *chainGroups) DeleteCascade(ctx context.Context, _ int64) ([]int64, error) {
	r.w.record(ctx, "group.delete")
	return nil, nil
}
func (r *chainGroups) DeleteCascadeIfEmpty(ctx context.Context, id int64) ([]int64, error) {
	if r.nonempty {
		return nil, service.ErrGroupNotEmpty
	}
	return r.DeleteCascade(ctx, id)
}
func (r *chainGroups) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	return []int64{10}, nil
}
func (r *chainGroups) GetAccountCount(context.Context, int64) (int64, int64, error) { return 0, 0, nil }
func (r *chainGroups) DeleteAccountGroupsByGroupID(ctx context.Context, _ int64) (int64, error) {
	r.w.record(ctx, "group.clear_bindings")
	return 0, nil
}
func (r *chainGroups) BindAccountsToGroup(ctx context.Context, _ int64, _ []int64) error {
	r.w.record(ctx, "group.bind")
	return nil
}
func (r *chainGroups) ExistsByName(context.Context, string) (bool, error) { return false, nil }
func (r *chainGroups) ListActiveByPlatform(context.Context, string) ([]service.Group, error) {
	return []service.Group{{ID: 1, Name: "openai-default"}}, nil
}

type chainProxies struct {
	stubProxyRepo
	w            *chainWrites
	row          *service.Proxy
	extra        []service.Proxy
	deleted      bool
	deletedState *bool
}

func (r *chainProxies) GetByID(context.Context, int64) (*service.Proxy, error) {
	if r.deletedState != nil && *r.deletedState {
		return nil, service.ErrProxyNotFound
	}
	p := *r.row
	return &p, nil
}
func (r *chainProxies) ListAllForFallback(context.Context) ([]service.Proxy, error) {
	if r.deletedState != nil && *r.deletedState {
		return append([]service.Proxy(nil), r.extra...), nil
	}
	return append([]service.Proxy{*r.row}, r.extra...), nil
}
func (r *chainProxies) Create(ctx context.Context, p *service.Proxy) error {
	r.w.record(ctx, "proxy.create")
	p.ID = 10
	r.row = p
	return nil
}
func (r *chainProxies) Update(ctx context.Context, p *service.Proxy) error {
	r.w.record(ctx, "proxy.update")
	r.row = p
	return nil
}
func (r *chainProxies) Delete(ctx context.Context, _ int64) error {
	r.w.record(ctx, "proxy.delete")
	r.deleted = true
	if r.deletedState != nil {
		*r.deletedState = true
	}
	return nil
}
func (r *chainProxies) ExistsByHostPortAuth(context.Context, string, int, string, string) (bool, error) {
	return false, nil
}
func (r *chainProxies) CountAccountsByProxyID(context.Context, int64) (int64, error) { return 0, nil }

type chainQuery struct {
	protected, used bool
	usedIDs         map[int64]bool
	owners          []int64
	err             error
}

func (q *chainQuery) GroupHasProtectedMembers(context.Context, int64, int64) (bool, error) {
	return q.protected, nil
}
func (q *chainQuery) ProxyIDsUsedByAccounts(_ context.Context, ids []int64) ([]int64, error) {
	out := []int64{}
	for _, id := range ids {
		if q.used || q.usedIDs[id] {
			out = append(out, id)
		}
	}
	return out, nil
}
func (q *chainQuery) AttributeValueOwnerIDs(context.Context, int64) ([]int64, error) {
	return q.owners, q.err
}

type chainPermissions struct{ raw []byte }

func (r *chainPermissions) Get(context.Context, int64) ([]byte, bool, error) { return r.raw, true, nil }
func (*chainPermissions) Upsert(context.Context, int64, []byte, int64) error { return nil }

type chainAssign struct{ w *chainWrites }

func (r chainAssign) AssignOrExtendSubscription(ctx context.Context, _ *service.AssignSubscriptionInput) (*service.UserSubscription, bool, error) {
	r.w.record(ctx, "subscription.assign")
	return &service.UserSubscription{}, true, nil
}

type operatorChain struct {
	r            *gin.Engine
	token        string
	w            *chainWrites
	users        *chainUsers
	accounts     *chainAccounts
	groups       *chainGroups
	proxies      *chainProxies
	query        *chainQuery
	permissions  *chainPermissions
	settings     *service.SettingService
	settingsRepo *stubSettingRepo
	admin        service.AdminService
	monitors     *chainMonitors
	templates    *chainTemplates
	redeems      *chainRedeems
	http         *chainHTTP
	sql          sqlmock.Sqlmock
	riskWrites   *chainRiskSettings
	channels     *chainChannels
	attributes   *chainAttributes
	cfg          *config.Config
}

func newOperatorChain(t *testing.T, role string, modules bool, flags []operatoraccess.Flag, backendMode bool) *operatorChain {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if strings.Contains(t.Name(), "/backend_on/") {
		backendMode = true
	}
	f := &operatorChain{w: &chainWrites{}, query: &chainQuery{}}
	f.users = &chainUsers{stubUserRepo: &stubUserRepo{users: map[int64]*service.User{1: {ID: 1, Email: "plain@example.com", Role: service.RoleUser, Status: service.StatusActive}, 7: {ID: 7, Email: "actor@example.com", Role: role, Status: service.StatusActive}}}, w: f.w}
	f.accounts = &chainAccounts{w: f.w, row: &service.Account{ID: 10, Name: "account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Credentials: map[string]any{"api_key": "stored", "base_url": "https://203.0.113.10"}, Extra: map[string]any{}, Concurrency: 1}}
	f.groups = &chainGroups{stubGroupRepo: &stubGroupRepo{}, w: f.w, row: &service.Group{ID: 1, Name: "group", Platform: service.PlatformOpenAI, RateMultiplier: 1, IsExclusive: true, Status: service.StatusActive, DailyLimitUSD: chainPtr(10.0)}}
	f.proxies = &chainProxies{w: f.w, row: &service.Proxy{ID: 10, Name: "proxy", Protocol: "http", Host: "203.0.113.10", Port: 8080, Status: service.StatusActive, FallbackMode: service.FallbackModeNone}}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "unit-chain-secret", ExpireHour: 1}, RunMode: config.RunModeStandard}
	f.cfg = cfg
	f.settingsRepo = newStubSettingRepo()
	f.settingsRepo.all[service.SettingKeyDefaultBalance] = "5"
	f.settingsRepo.all[service.SettingKeyDefaultSubscriptions] = `[{"group_id":1,"validity_days":7}]`
	f.settingsRepo.all[service.SettingKeyStepUpEnabled] = "false"
	f.settings = service.NewSettingService(f.settingsRepo, cfg)
	f.settingsRepo.all[service.SettingKeyBackendModeEnabled] = strconv.FormatBool(backendMode)
	storedSettings, err := f.settings.GetAllSettings(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		storedSettings.BackendModeEnabled = false
		require.NoError(t, f.settings.UpdateSettings(context.Background(), storedSettings))
	})
	storedSettings.BackendModeEnabled = backendMode
	require.NoError(t, f.settings.UpdateSettings(context.Background(), storedSettings))
	keys := newStubApiKeyRepo(time.Now())
	keys.MustSeed(&service.APIKey{ID: 1, UserID: 1, Key: "unit-chain-key", Status: service.StatusActive})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		matched, err := regexp.MatchString(expected, actual)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("unexpected SQL: %s", actual)
		}
		if strings.HasPrefix(actual, "INSERT") || strings.HasPrefix(actual, "UPDATE") || strings.HasPrefix(actual, "DELETE") {
			f.w.record(context.Background(), "identity.write")
		}
		return nil
	})))
	require.NoError(t, err)
	f.sql = mock
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()); _ = client.Close() })
	f.admin = service.NewAdminService(cfg, f.users, f.groups, f.accounts, f.proxies, keys, &stubRedeemCodeRepo{}, nil, nil, nil, nil, nil, nil, client, f.settings, chainAssign{f.w}, &stubUserSubscriptionRepo{}, nil, nil, nil, nil, nil, nil)
	perms := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{}, Flags: map[operatoraccess.Flag]bool{}}
	if modules {
		for _, m := range operatoraccess.Modules {
			perms.Modules[m] = operatoraccess.LevelManage
		}
	}
	for _, flag := range flags {
		perms.Flags[flag] = true
	}
	raw, err := json.Marshal(perms.Wire())
	require.NoError(t, err)
	f.permissions = &chainPermissions{raw}
	f.monitors = &chainMonitors{w: f.w, row: &service.ChannelMonitor{ID: 10, Name: "monitor", Provider: "openai", APIMode: "responses", Endpoint: "https://203.0.113.10", APIKey: "stored", PrimaryModel: "test", IntervalSeconds: 60, ExtraHeaders: map[string]string{"x-secret": "stored-header"}}}
	f.templates = &chainTemplates{w: f.w, row: &service.ChannelMonitorRequestTemplate{ID: 10, Provider: "openai", APIMode: "responses", ExtraHeaders: map[string]string{"x-secret": "stored-header"}}}
	f.redeems = &chainRedeems{w: f.w, rows: map[int64]*service.RedeemCode{10: {ID: 10, Type: "balance", Status: "unused"}, 11: {ID: 11, Type: "admin_balance", Status: "unused"}, 12: {ID: 12, Type: "balance", Status: "used", UsedBy: chainPtr(int64(7))}}}
	monitorService := service.NewChannelMonitorService(f.monitors, chainEncryptor{})
	templateService := service.NewChannelMonitorRequestTemplateService(f.templates)
	moderation := service.NewContentModerationService(f.settingsRepo, nil, nil, nil, nil, nil, nil, nil)
	f.riskWrites = &chainRiskSettings{SettingRepository: f.settingsRepo, w: f.w}
	moderation = service.NewContentModerationService(f.riskWrites, nil, nil, nil, nil, nil, nil, nil)
	f.channels = &chainChannels{w: f.w, row: &service.Channel{ID: 10, Name: "channel", Status: service.StatusActive, GroupIDs: []int64{1}}}
	channelService := service.NewChannelService(f.channels, f.groups, nil, nil, nil)
	f.attributes = &chainAttributes{w: f.w}
	access := service.NewOperatorAccessService(f.permissions, f.users, keys, nil, nil, f.redeems, f.proxies, f.admin, nil, channelService, monitorService, templateService, moderation, nil, f.settings, nil, nil, f.query)
	users := service.NewUserService(f.users, nil, nil, nil)
	auth := service.NewAuthService(nil, f.users, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	f.token, err = auth.GenerateToken(context.Background(), f.users.users[7])
	require.NoError(t, err)
	f.http = &chainHTTP{}
	gemini := service.NewGeminiOAuthService(f.proxies, nil, nil, chainDrive{}, cfg)
	account := adminhandler.NewAccountHandler(f.admin, nil, nil, gemini, nil, nil, nil, nil, service.NewAccountTestService(f.accounts, nil, nil, nil, nil, f.http, cfg, nil), nil, nil, nil, nil, nil)
	user := adminhandler.NewUserHandler(f.admin, nil, nil, nil, nil, users, f.settings)
	group := adminhandler.NewGroupHandler(f.admin, nil, nil)
	proxy := adminhandler.NewProxyHandler(f.admin)
	f.r = gin.New()
	g := f.r.Group("/api/v1/admin")
	g.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, users, f.settings, nil, access)))
	g.Use(middleware.BackendModeUserGuard(f.settings))
	g.Use(middleware.AdminPermission())
	g.POST("/users", user.Create)
	g.PUT("/users/:id", user.Update)
	g.POST("/users/:id/auth-identities", user.BindAuthIdentity)
	g.POST("/users/batch-concurrency", user.BatchUpdateConcurrency)
	g.POST("/users/:id/replace-group", user.ReplaceGroup)
	g.GET("/users", user.List)
	attrs := adminhandler.NewUserAttributeHandler(service.NewUserAttributeService(f.attributes, &chainAttributeValues{f.attributes}))
	g.DELETE("/user-attributes/:id", attrs.DeleteDefinition)
	g.POST("/user-attributes/batch", attrs.GetBatchUserAttributes)
	channels := adminhandler.NewChannelHandler(channelService, nil, nil)
	g.PUT("/channels/:id", channels.Update)
	risk := adminhandler.NewContentModerationHandler(moderation)
	g.GET("/risk-control/config", risk.GetConfig)
	g.PUT("/risk-control/config", risk.UpdateConfig)
	g.POST("/accounts", account.Create)
	g.PUT("/accounts/:id", account.Update)
	g.GET("/accounts/:id", account.GetByID)
	g.GET("/accounts", account.List)
	g.POST("/accounts/:id/duplicate", account.Duplicate)
	g.POST("/accounts/:id/shadow", adminhandler.NewOpenAIOAuthHandler(nil, f.admin, nil, nil).CreateShadow)
	g.POST("/accounts/models/sync-upstream-preview", account.SyncUpstreamModelsPreview)
	g.POST("/accounts/bulk-update", account.BulkUpdate)
	g.POST("/accounts/batch", account.BatchCreate)
	g.POST("/accounts/batch-update-credentials", account.BatchUpdateCredentials)
	g.POST("/accounts/batch-refresh-tier", account.BatchRefreshTier)
	g.POST("/accounts/:id/apply-oauth-credentials", account.ApplyOAuthCredentials)
	g.POST("/accounts/:id/revert-proxy-fallback", account.RevertProxyFallback)
	g.POST("/accounts/:id/test", account.Test)
	g.POST("/groups", group.Create)
	g.PUT("/groups/:id", group.Update)
	g.PUT("/groups/:id/rate-multipliers", group.BatchSetGroupRateMultipliers)
	g.DELETE("/groups/:id", group.Delete)
	g.POST("/proxies", proxy.Create)
	g.PUT("/proxies/:id", proxy.Update)
	g.DELETE("/proxies/:id", proxy.Delete)
	g.POST("/proxies/batch-delete", proxy.BatchDelete)
	monitor := adminhandler.NewChannelMonitorHandler(monitorService)
	template := adminhandler.NewChannelMonitorRequestTemplateHandler(templateService)
	redeem := adminhandler.NewRedeemHandler(f.admin, service.NewRedeemService(f.redeems, f.users, nil, nil, nil, nil, nil, nil))
	g.POST("/channel-monitors", monitor.Create)
	g.PUT("/channel-monitors/:id", monitor.Update)
	g.GET("/channel-monitors/:id", monitor.Get)
	g.GET("/channel-monitors", monitor.List)
	g.GET("/audit-logs/:id", adminhandler.NewAuditLogHandler(service.NewAuditLogService(chainAudit{}, f.settings), nil).Get)
	g.GET("/channel-monitor-templates", template.List)
	g.PUT("/channel-monitor-templates/:id", template.Update)
	g.POST("/channel-monitor-templates/:id/apply", template.Apply)
	g.POST("/redeem-codes/batch-update", redeem.BatchUpdate)
	g.GET("/redeem-codes", redeem.List)
	g.PUT("/api-keys/:id", adminhandler.NewAdminAPIKeyHandler(f.admin).UpdateGroup)
	return f
}
func chainPtr[T any](v T) *T { return &v }
func (f *operatorChain) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/v1/admin"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "operator-chain-key")
	f.r.ServeHTTP(rec, req)
	return rec
}
func chainDeny(t *testing.T, f *operatorChain, method, path, body string, status int, reason, field string) {
	t.Helper()
	rec := f.do(method, path, body)
	require.Equal(t, status, rec.Code, rec.Body.String())
	var out struct {
		Reason   string            `json:"reason"`
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, reason, out.Reason, rec.Body.String())
	if field != "" {
		require.Equal(t, field, out.Metadata["field"], rec.Body.String())
	}
	f.w.empty(t)
}

func TestOperatorChains(t *testing.T) {
	for _, finding := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"TestOperatorCreateUserGetsNoSignupGrants", chainCreateUser},
		{"TestOperatorPlacementNeedsBalanceFlag", chainPlacement},
		{"TestOperatorNoncanonicalRequestsRejected", chainNoncanonical},
		{"TestOperatorGroupNullLimitIsAChange", chainNullGroup},
		{"TestOperatorApplyOAuthCannotChangeAddress", chainOAuth},
		{"TestOperatorBulkUpdateWithFiltersRejectsNetworkKeys", chainBulk},
		{"TestOperatorRefreshableAccountNetworkLocked", chainRefreshable},
		{"TestOperatorUpstreamAddressMatrix", chainAddresses},
		{"TestProtectedCallsFromBatchAndIdempotentPathsCarryActor", chainActors},
		{"TestOperatorAccountTestStreamHidesAddress", chainStream},
		{"TestOperatorCannotDeleteFallbackOriginThenRevert", chainRevert},
		{"TestOperatorReservedEmailAndGroupDelete", chainReservedAndDelete},
		{"TestOperatorMonitorAndTemplateHeaders", chainMonitor},
		{"TestOperatorProxyChainAndExpiry", chainProxy},
		{"TestOperatorReadOnlyViewsHideSecrets", chainReadOnly},
		{"TestOperatorRedeemBatchUpdateRejectsLedgerAndAdminRows", chainRedeem},
		{"TestOperatorBindIdentityRejectsMetadata", chainIdentity},
		{"TestOperatorAccountBillingExtrasNeedFlag", chainBilling},
		{"TestOperatorAccountRejectsInjectedAWSRegion", chainRegion},
		{"TestOperatorDeleteGroupWithProtectedMembers", chainProtectedGroup},
	} {
		t.Run(finding.name, func(t *testing.T) {
			for _, mode := range []string{"backend_off", "backend_on"} {
				t.Run(mode, func(t *testing.T) {
					t.Run("details", finding.run)
					t.Run("axes", func(t *testing.T) { chainFindingAxes(t, finding.name) })
				})
			}
		})
	}
}

func chainCreateUser(t *testing.T) {
	for _, mode := range []bool{false, true} {
		for _, axis := range []string{"super", "no_module", "operator", "flag", "user"} {
			t.Run(fmt.Sprintf("%t/%s", mode, axis), func(t *testing.T) {
				role := service.RoleOperator
				mods := true
				var flags []operatoraccess.Flag
				switch axis {
				case "super":
					role = service.RoleAdmin
				case "no_module":
					mods = false
				case "flag":
					flags = []operatoraccess.Flag{operatoraccess.FlagUsersBalanceAdjust}
				case "user":
					role = service.RoleUser
				}
				f := newOperatorChain(t, role, mods, flags, mode)
				body := `{"email":"created@example.com","password":"password123","concurrency":1}`
				rec := f.do("POST", "/users", body)
				if axis == "no_module" || axis == "user" {
					require.Equal(t, 403, rec.Code, rec.Body.String())
					f.w.empty(t)
					return
				}
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.NotEmpty(t, f.w.calls)
				if axis == "operator" {
					require.Zero(t, f.users.users[20].Balance)
					require.NotContains(t, f.w.calls, "subscription.assign")
				} else {
					require.Equal(t, 5.0, f.users.users[20].Balance)
					require.Contains(t, f.w.calls, "subscription.assign")
				}
			})
		}
	}
	for _, balance := range []string{"0", "5"} {
		f := newOperatorChain(t, service.RoleOperator, true, nil, false)
		body := `{"email":"created@example.com","password":"password123","balance":` + balance + `}`
		if balance == "5" {
			chainDeny(t, f, "POST", "/users", body, 400, "ADMIN_FIELD_FORBIDDEN", "balance")
		} else {
			rec := f.do("POST", "/users", body)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Zero(t, f.users.users[20].Balance)
		}
	}
}
func chainPlacement(t *testing.T) {
	for _, tc := range []struct{ method, path, body, field string }{
		{"PUT", "/accounts/10", `{"group_ids":[2]}`, "group_ids"},
		{"POST", "/accounts", `{"name":"a","platform":"openai","type":"apikey","credentials":{"api_key":"new"},"group_ids":[2]}`, "group_ids"},
		{"POST", "/accounts/bulk-update", `{"account_ids":[10],"group_ids":[2]}`, "group_ids"},
		{"POST", "/groups", `{"name":"g","platform":"openai","rate_multiplier":1,"is_exclusive":true,"copy_accounts_from_group_ids":[2]}`, "copy_accounts_from_group_ids"},
		{"PUT", "/groups/1", `{"copy_accounts_from_group_ids":[2]}`, "copy_accounts_from_group_ids"},
	} {
		f := newOperatorChain(t, service.RoleOperator, true, nil, false)
		chainDeny(t, f, tc.method, tc.path, tc.body, 400, "ADMIN_FIELD_FORBIDDEN", tc.field)
		allowed := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagUsersBalanceAdjust}, false)
		rec := allowed.do(tc.method, tc.path, tc.body)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.NotEmpty(t, allowed.w.calls)
	}
	for _, action := range []string{"shadow", "duplicate"} {
		denied := newOperatorChain(t, service.RoleOperator, true, nil, false)
		chainDeny(t, denied, "POST", "/accounts/10/"+action, `{}`, 403, "ADMIN_PERMISSION_DENIED", "")
		allowed := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagUsersBalanceAdjust}, false)
		if action == "shadow" {
			allowed.accounts.row.Type = "oauth"
		} else {
			allowed.accounts.row.Platform = "anthropic"
		}
		rec := allowed.do("POST", "/accounts/10/"+action, `{}`)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, allowed.w.calls, "account.create")
	}
	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	rec := f.do("POST", "/accounts", `{"name":"a","platform":"openai","type":"apikey","credentials":{"api_key":"new"}}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Empty(t, f.accounts.row.GroupIDs)
	require.NotContains(t, f.w.calls, "account.bind")
}
func chainNoncanonical(t *testing.T) {
	chainPlainText(t)
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "/users/1", `{"balance":null,"Balance":1000000}`}, {"PUT", "/users/1", `{"Role":"admin"}`},
		{"POST", "/users/batch-concurrency", `{"user_ids":[1],"User_IDs":[7]}`}, {"POST", "/users/batch-concurrency", `{"All":true,"concurrency":100}`},
		{"PUT", "/groups/1", `{} {}`}, {"PUT", "/groups/1/rate-multipliers", `{"entries":[{"user_id":1,"User_ID":7,"rate_multiplier":1}]}`}, {"GET", "/redeem-codes?search=a@b&search=X", ``},
	} {
		f := newOperatorChain(t, service.RoleOperator, true, operatoraccess.Flags, false)

		rec := f.do(tc.method, tc.path, tc.body)
		require.Equal(t, 400, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "ADMIN_FIELD_FORBIDDEN")
		f.w.empty(t)
	}
}
func chainPlainText(t *testing.T) {
	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	req := httptest.NewRequest("PUT", "/api/v1/admin/users/1", strings.NewReader(`{"balance":10}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	require.Equal(t, 400, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "noncanonical")
	f.w.empty(t)
}
func chainNullGroup(t *testing.T) {
	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	chainDeny(t, f, "PUT", "/groups/1", `{"daily_limit_usd":null}`, 400, "ADMIN_FIELD_FORBIDDEN", "daily_limit_usd")
}
func chainOAuth(t *testing.T) {
	allowed := newOperatorChain(t, service.RoleOperator, true, nil, false)
	allowed.accounts.row.Type = "oauth"
	rec := allowed.do("POST", "/accounts/10/apply-oauth-credentials", `{"type":"oauth","credentials":{"access_token":"fresh"}}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, allowed.w.calls, "account.update")
	for _, flag := range []bool{false, true} {
		var flags []operatoraccess.Flag
		if flag {
			flags = []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}
		}
		f := newOperatorChain(t, service.RoleOperator, true, flags, false)
		f.accounts.row.Type = "oauth"
		chainDeny(t, f, "POST", "/accounts/10/apply-oauth-credentials", `{"type":"oauth","credentials":{},"extra":{"custom_base_url":"https://evil.example"}}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "extra.custom_base_url")
		f.accounts.row.Type = "setup-token"
		chainDeny(t, f, "POST", "/accounts/10/apply-oauth-credentials", `{"type":"oauth","credentials":{}}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "type")
	}
}
func chainBulk(t *testing.T) {
	for _, flag := range []bool{false, true} {
		var flags []operatoraccess.Flag
		if flag {
			flags = []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}
		}
		for _, body := range []string{`{"filters":{"platform":"openai"},"credentials":{"base_url":"https://203.0.113.11"}}`, `{"filters":{"platform":"openai"},"proxy_id":2}`, `{"filters":{"platform":"openai"},"credentials":{"aws_region":"eu-central-1"}}`} {
			f := newOperatorChain(t, service.RoleOperator, true, flags, false)
			chainDeny(t, f, "POST", "/accounts/bulk-update", body, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "")
		}
	}
}
func chainRefreshable(t *testing.T) {
	allowed := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
	rec := allowed.do("PUT", "/accounts/10", `{"proxy_id":2,"credentials":{"api_key":"fresh"}}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, int64(2), *allowed.accounts.row.ProxyID)
	require.Contains(t, allowed.w.calls, "account.update")
	for _, flag := range []bool{false, true} {
		var flags []operatoraccess.Flag
		if flag {
			flags = []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}
		}
		f := newOperatorChain(t, service.RoleOperator, true, flags, false)
		f.accounts.row.Type = "oauth"
		chainDeny(t, f, "PUT", "/accounts/10", `{"proxy_id":2,"credentials":{"api_key":"new"}}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "proxy_id")
	}
	f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
	f.accounts.row.Credentials["newapi_access_token"] = "stored"
	chainDeny(t, f, "PUT", "/accounts/10", `{"proxy_id":2,"credentials":{"api_key":"new"}}`, 400, "ACCOUNT_API_KEY_REQUIRED", "credentials.newapi_access_token")
}
func chainAddresses(t *testing.T) {
	for _, host := range []string{"203.0.113.11", "127.0.0.1"} {
		f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
		body := `{"name":"p","protocol":"http","host":"` + host + `","port":8080}`
		if host == "127.0.0.1" {
			chainDeny(t, f, "POST", "/proxies", body, 400, "ADMIN_FIELD_FORBIDDEN", "host")
		} else {
			rec := f.do("POST", "/proxies", body)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, host, f.proxies.row.Host)
			require.Contains(t, f.w.calls, "proxy.create")
		}
	}
	monitor := newOperatorChain(t, service.RoleOperator, true, nil, false)
	rec := monitor.do("POST", "/channel-monitors", `{"name":"monitor","provider":"openai","api_mode":"responses","endpoint":"https://203.0.113.11","api_key":"fresh","primary_model":"test","interval_seconds":60}`)
	require.Equal(t, 201, rec.Code, rec.Body.String())
	require.Contains(t, monitor.w.calls, "monitor.create")
	for _, tc := range []struct{ method, path, body, field string }{
		{"POST", "/accounts", `{"name":"a","platform":"openai","type":"apikey","credentials":{"base_url":"https://203.0.113.11","api_key":"new"}}`, "credentials.base_url"},
		{"PUT", "/accounts/10", `{"credentials":{"base_url":"https://203.0.113.11","api_key":"new"}}`, "credentials.base_url"},
		{"POST", "/accounts", `{"name":"a","platform":"openai","type":"apikey","credentials":{"api_key":"new"},"proxy_id":2}`, "proxy_id"},
		{"POST", "/accounts/models/sync-upstream-preview", `{"platform":"openai","api_key":"new","base_url":"https://203.0.113.11"}`, "base_url"},
		{"PUT", "/accounts/10", `{"proxy_id":2}`, "proxy_id"}, {"PUT", "/accounts/10", `{"credentials":{"api_protocol":"responses"}}`, "credentials.api_protocol"},
		{"POST", "/proxies", `{"name":"p","protocol":"http","host":"203.0.113.10","port":8080}`, "host"}, {"PUT", "/proxies/10", `{"host":"203.0.113.11"}`, "host"}, {"PUT", "/proxies/10", `{"backup_proxy_id":20}`, "backup_proxy_id"},
	} {
		f := newOperatorChain(t, service.RoleOperator, true, nil, false)
		chainDeny(t, f, tc.method, tc.path, tc.body, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", tc.field)
	}
	for _, host := range []string{"https://127.0.0.1", "https://localhost", "https://169.254.169.254", "http://metadata.google.internal"} {
		f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
		chainDeny(t, f, "PUT", "/accounts/10", `{"credentials":{"api_key":"new","base_url":"`+host+`"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "credentials.base_url")
	}
	for _, role := range []string{service.RoleOperator, service.RoleAdmin} {
		f := newOperatorChain(t, role, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
		rec := f.do("PUT", "/accounts/10", `{"credentials":{"api_key":"new","base_url":"https://203.0.113.11"}}`)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Equal(t, "https://203.0.113.11", f.accounts.row.Credentials["base_url"])
		require.NotEmpty(t, f.w.calls)
	}
}
func chainActors(t *testing.T) {
	for _, role := range []string{service.RoleOperator, service.RoleAdmin} {
		for _, tc := range []struct{ path, body string }{
			{"/accounts", `{"name":"a","platform":"openai","type":"apikey","credentials":{"api_key":"new"}}`},
			{"/accounts/batch", `{"accounts":[{"name":"a","platform":"openai","type":"apikey","credentials":{"api_key":"new"}}]}`},
			{"/accounts/batch-update-credentials", `{"account_ids":[10],"field":"account_uuid","value":"new-id"}`},
			{"/users/batch-concurrency", `{"user_ids":[1],"concurrency":2,"mode":"set"}`},
			{"/accounts/batch-refresh-tier", `{"account_ids":[10]}`},
			{"/groups/1", ``},
		} {
			f := newOperatorChain(t, role, true, nil, false)
			repo := &chainIdempotency{rows: map[string]*service.IdempotencyRecord{}, w: f.w}
			cfg := service.DefaultIdempotencyConfig()
			cfg.ObserveOnly = false
			previous := service.DefaultIdempotencyCoordinator()
			service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, cfg))
			if strings.Contains(tc.path, "refresh-tier") {
				f.accounts.row.Platform = service.PlatformGemini
				f.accounts.row.Type = service.AccountTypeOAuth
				f.accounts.row.Credentials = map[string]any{"oauth_type": "google_one", "access_token": "unit-token"}
			}
			method := "POST"
			if strings.HasPrefix(tc.path, "/groups/") {
				method = "DELETE"
			}
			rec := f.do(method, tc.path, tc.body)
			service.SetDefaultIdempotencyCoordinator(previous)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.NotContains(t, rec.Body.String(), "ADMIN_ACTOR_MISSING")
			require.NotEmpty(t, f.w.actors)
			if strings.Contains(tc.path, "refresh-tier") {
				require.Contains(t, f.w.calls, "account.update")
			}
			if tc.path == "/accounts/batch" {
				var result struct {
					Data struct {
						Success int `json:"success"`
						Failed  int `json:"failed"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result), rec.Body.String())
				require.Equal(t, 1, result.Data.Success, rec.Body.String())
				require.Zero(t, result.Data.Failed, rec.Body.String())
				require.Contains(t, f.w.calls, "account.create")
			}
			if tc.path == "/accounts" {
				require.Contains(t, f.w.calls, "idempotency.claim")
			}
			for _, actor := range f.w.actors {
				if role == service.RoleAdmin {
					require.Equal(t, operatoraccess.ActorSuperAdmin, actor.Kind)
				} else {
					require.Equal(t, operatoraccess.ActorOperator, actor.Kind)
				}
			}
		}
	}
}
func chainStream(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer tlsServer.Close()
	parsed, err := url.Parse(tlsServer.URL)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(tlsServer.Certificate())
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	refused := listener.Addr().String()
	require.NoError(t, listener.Close())
	for _, base := range []string{"not a url", "https://no-such-host.invalid", "https://localhost:" + parsed.Port(), "http://" + refused} {
		t.Run(base, func(t *testing.T) {
			for _, role := range []string{service.RoleOperator, service.RoleAdmin} {
				f := newOperatorChain(t, role, true, nil, false)
				f.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				f.accounts.row.Credentials["base_url"] = base
				f.http.client = client
				// The TLS client trusts the test CA; only its hostname is wrong.

				rec := f.do("POST", "/accounts/10/test", `{}`)
				if base == "http://"+refused {
					require.Greater(t, f.http.calls.Load(), int32(0), "request must reach the transport")
					if role == service.RoleAdmin {
						require.Contains(t, rec.Body.String(), "connection refused")
						require.Contains(t, rec.Body.String(), refused)
					}
				}
				if role == service.RoleOperator {
					require.Contains(t, rec.Body.String(), operatoraccess.ErrorDetailHidden)
					for _, hidden := range []string{base, "127.0.0.1", refused, parsed.Port(), "localhost", "no-such-host.invalid", "example.com"} {
						require.NotContains(t, rec.Body.String(), hidden)
					}
				} else {
					require.NotContains(t, rec.Body.String(), operatoraccess.ErrorDetailHidden)
					require.Contains(t, rec.Body.String(), "error")
				}
				f.w.empty(t)
			}
		})
	}
}

func chainRevert(t *testing.T) {
	for _, typ := range []string{"oauth", "apikey"} {
		for _, hasFlag := range []bool{false, true} {
			var flags []operatoraccess.Flag
			if hasFlag {
				flags = []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}
			}
			f := newOperatorChain(t, service.RoleOperator, true, flags, false)
			f.accounts.row.Type = typ
			f.accounts.row.ProxyID = chainPtr(int64(20))
			f.accounts.row.ProxyFallbackOriginID = chainPtr(int64(10))
			f.query.usedIDs = map[int64]bool{10: true}
			f.accounts.shadow = &service.Account{ID: 11, ProxyID: chainPtr(int64(20)), ParentAccountID: chainPtr(int64(10))}
			f.accounts.outbox = []int64{99}
			snapshot := func() string {
				raw, e := json.Marshal([]any{f.accounts.row, f.accounts.shadow, f.accounts.outbox})
				require.NoError(t, e)
				return string(raw)
			}
			before := snapshot()
			for _, tc := range []struct{ method, path, body, field string }{{"DELETE", "/proxies/10", "", "id"}, {"POST", "/proxies/batch-delete", `{"ids":[10]}`, "ids"}} {
				chainDeny(t, f, tc.method, tc.path, tc.body, 400, "ADMIN_FIELD_FORBIDDEN", tc.field)
				require.False(t, f.proxies.deleted)
			}
			chainDeny(t, f, "POST", "/accounts/10/revert-proxy-fallback", `{}`, 403, "SUPER_ADMIN_REQUIRED", "")
			require.JSONEq(t, before, snapshot())
			// Delete through the super-admin's actual authenticated chain, sharing state.
			deleted := false
			f.proxies.deletedState = &deleted
			admin := newOperatorChain(t, service.RoleAdmin, true, nil, false)
			admin.proxies.row = f.proxies.row
			admin.proxies.deletedState = &deleted
			rec := admin.do("DELETE", "/proxies/10", "")
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.True(t, deleted)
			_, err := f.proxies.GetByID(context.Background(), 10)
			require.ErrorIs(t, err, service.ErrProxyNotFound)
			_, err = admin.proxies.GetByID(context.Background(), 10)
			require.ErrorIs(t, err, service.ErrProxyNotFound)
			chainDeny(t, f, "POST", "/accounts/10/revert-proxy-fallback", `{}`, 403, "SUPER_ADMIN_REQUIRED", "")
			require.JSONEq(t, before, snapshot())
			admin.accounts.row = f.accounts.row
			admin.accounts.shadow = f.accounts.shadow
			admin.accounts.outbox = append([]int64(nil), f.accounts.outbox...)
			rec = admin.do("POST", "/accounts/10/revert-proxy-fallback", `{}`)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Nil(t, admin.accounts.row.ProxyFallbackOriginID)
			require.Equal(t, int64(10), *admin.accounts.row.ProxyID)
			require.Equal(t, int64(10), *admin.accounts.shadow.ProxyID)
			require.Equal(t, []int64{99, 10, 11}, admin.accounts.outbox)
			require.Contains(t, admin.w.calls, "account.update")
		}
	}
}

func chainReservedAndDelete(t *testing.T) {
	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	chainDeny(t, f, "POST", "/users", `{"email":"a@linuxdo-connect.invalid","password":"password123"}`, 400, "ADMIN_FIELD_FORBIDDEN", "email")
	f = newOperatorChain(t, service.RoleOperator, true, nil, false)
	f.groups.nonempty = true
	chainDeny(t, f, "DELETE", "/groups/1", "", 409, "GROUP_NOT_EMPTY", "")
}
func chainMonitor(t *testing.T) {
	f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
	rec := f.do("PUT", "/channel-monitors/10", `{"endpoint":"https://203.0.113.11","api_key":"new","extra_headers":{"x-secret":"******","x-new":"fresh"}}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, map[string]string{"x-new": "fresh"}, f.monitors.row.ExtraHeaders)
	f = newOperatorChain(t, service.RoleOperator, true, nil, false)
	chainDeny(t, f, "POST", "/channel-monitor-templates/10/apply", `{"monitor_ids":[10]}`, 403, "SUPER_ADMIN_REQUIRED", "")
	chainDeny(t, f, "POST", "/channel-monitors", `{"extra_headers":{"x-secret":"******"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "extra_headers")
}
func chainProxy(t *testing.T) {
	for _, source := range []string{"backup", "origin", "websearch"} {
		t.Run(source, func(t *testing.T) {
			f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
			switch source {
			case "backup":
				f.proxies.extra = []service.Proxy{{ID: 20, FallbackMode: service.FallbackModeProxy, BackupProxyID: chainPtr(int64(10))}}
				f.query.usedIDs = map[int64]bool{20: true}
			case "origin":
				f.query.usedIDs = map[int64]bool{10: true}
				f.accounts.row.ProxyFallbackOriginID = chainPtr(int64(10))
			case "websearch":
				require.NoError(t, f.settings.SaveWebSearchEmulationConfig(context.Background(), &service.WebSearchEmulationConfig{Providers: []service.WebSearchProviderConfig{{Type: "brave", ProxyID: chainPtr(int64(10))}}}))
				t.Cleanup(func() {
					require.NoError(t, f.settings.SaveWebSearchEmulationConfig(context.Background(), &service.WebSearchEmulationConfig{}))
				})
			}
			chainDeny(t, f, "PUT", "/proxies/10", `{"host":"203.0.113.11","password":"fresh"}`, 400, "ADMIN_FIELD_FORBIDDEN", "host")
			old := time.Now().Add(time.Hour)
			f.proxies.row.ExpiresAt = &old
			chainDeny(t, f, "PUT", "/proxies/10", fmt.Sprintf(`{"expires_at":%d}`, time.Now().Add(-time.Hour).Unix()), 400, "ADMIN_FIELD_FORBIDDEN", "expires_at")
			rec := f.do("PUT", "/proxies/10", fmt.Sprintf(`{"expires_at":%d}`, old.Add(time.Hour).Unix()))
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Contains(t, f.w.calls, "proxy.update")
		})
	}
}
func chainReadOnly(t *testing.T) {
	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	_, found := operatoraccess.Lookup("PUT /channel-monitors/:id")
	require.True(t, found, "audit route must resolve through the production catalog")
	f.accounts.row.Extra["custom_base_url"] = "https://203.0.113.15"
	for _, path := range []string{"/accounts", "/accounts/10", "/channel-monitors", "/channel-monitor-templates", "/audit-logs/10", "/audit-logs/11"} {
		rec := f.do("GET", path, "")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		if strings.HasPrefix(path, "/audit-logs/") {
			var out struct {
				Data struct {
					Body string `json:"request_body"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
			if path == "/audit-logs/10" {
				require.Equal(t, "<request body hidden>", out.Data.Body)
			} else {
				require.JSONEq(t, `{"extra_headers":{"x-secret":"***"},"name":"visible"}`, out.Data.Body)
			}
		}
		for _, secret := range []string{"https://203.0.113.", "custom_base_url", "stored-header", "audit-secret", "truncated-secret"} {
			require.NotContains(t, rec.Body.String(), secret, path)
		}
	}
	f.w.empty(t)
}

func chainRedeem(t *testing.T) {
	for _, tc := range []struct{ ids, reason string }{{"[11]", "SUPER_ADMIN_REQUIRED"}, {"[12]", "ADMIN_TARGET_FORBIDDEN"}, {"[10,11]", "SUPER_ADMIN_REQUIRED"}} {
		f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagRedeemCodesIssue}, false)
		chainDeny(t, f, "POST", "/redeem-codes/batch-update", `{"ids":`+tc.ids+`,"fields":{"notes":"only"}}`, 403, tc.reason, "")
	}
}
func chainIdentity(t *testing.T) {
	allowed := newOperatorChain(t, service.RoleOperator, true, nil, false)
	chainIdentitySQL(t, allowed, false)
	rec := allowed.do("POST", "/users/1/auth-identities", `{"provider_type":"email","provider_key":"email","provider_subject":"new@example.com"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, allowed.w.calls, "identity.write")

	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	chainDeny(t, f, "POST", "/users/1/auth-identities", `{"provider_type":"email","provider_key":"email","provider_subject":"x@example.com","metadata":{"source":"auth_service_dual_write"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "metadata")
}
func chainBilling(t *testing.T) {
	preserved := newOperatorChain(t, service.RoleOperator, true, nil, false)
	preserved.accounts.row.Extra["cache_ttl_override_enabled"] = true
	preserved.accounts.row.Extra["openai_long_context_billing_enabled"] = true
	rec := preserved.do("PUT", "/accounts/10", `{"name":"renamed"}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, true, preserved.accounts.row.Extra["cache_ttl_override_enabled"])
	require.Equal(t, true, preserved.accounts.row.Extra["openai_long_context_billing_enabled"])
	for _, tc := range []struct{ path, body string }{{"/accounts/10", `{"extra":{"cache_ttl_override_enabled":true}}`}, {"/accounts/bulk-update", `{"account_ids":[10],"extra":{"openai_long_context_billing_enabled":true}}`}} {
		f := newOperatorChain(t, service.RoleOperator, true, nil, false)
		method := "PUT"
		if strings.Contains(tc.path, "bulk") {
			method = "POST"
		}
		chainDeny(t, f, method, tc.path, tc.body, 400, "ADMIN_FIELD_FORBIDDEN", "")
	}
}
func chainRegion(t *testing.T) {
	for _, region := range []string{"x@evil.example/", "eu-central-1"} {
		f := newOperatorChain(t, service.RoleOperator, true, nil, false)
		body := `{"name":"bedrock","platform":"anthropic","type":"bedrock","credentials":{"aws_access_key_id":"key","aws_secret_access_key":"secret","aws_region":"` + region + `"}}`
		if region == "eu-central-1" {
			rec := f.do("POST", "/accounts", body)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Contains(t, f.w.calls, "account.create")
		} else {
			chainDeny(t, f, "POST", "/accounts", body, 400, "ADMIN_FIELD_FORBIDDEN", "credentials.aws_region")
		}
		require.Zero(t, f.http.calls.Load())
	}
	for _, hasFlag := range []bool{false, true} {
		var flags []operatoraccess.Flag
		if hasFlag {
			flags = []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}
		}
		f := newOperatorChain(t, service.RoleOperator, true, flags, false)
		f.accounts.row.Type = "bedrock"
		f.accounts.row.Credentials["aws_region"] = "us-east-1"
		body := `{"credentials":{"api_key":"fresh","aws_region":"eu-central-1"}}`
		if hasFlag {
			rec := f.do("PUT", "/accounts/10", body)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, "eu-central-1", f.accounts.row.Credentials["aws_region"])
			require.Contains(t, f.w.calls, "account.update")
		} else {
			chainDeny(t, f, "PUT", "/accounts/10", body, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "credentials.aws_region")
		}
	}
	for _, typ := range []string{"bedrock", "apikey"} {
		for _, flag := range []bool{false, true} {
			var flags []operatoraccess.Flag
			if flag {
				flags = []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}
			}
			f := newOperatorChain(t, service.RoleOperator, true, flags, false)
			f.accounts.row.Type = typ
			f.accounts.row.Credentials["aws_region"] = "us-east-1"
			chainDeny(t, f, "PUT", "/accounts/10", `{"type":"bedrock","credentials":{"api_key":"new","aws_region":"x@127.0.0.1/"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "credentials.aws_region")
		}
	}
}
func chainProtectedGroup(t *testing.T) {
	fplain := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagUsersBalanceAdjust}, false)
	rec := fplain.do("DELETE", "/groups/1", "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, fplain.w.calls, "group.delete")
	f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagUsersBalanceAdjust}, false)
	f.query.protected = true
	chainDeny(t, f, "DELETE", "/groups/1", "", 403, "ADMIN_TARGET_FORBIDDEN", "")
}

type chainHTTP struct {
	service.HTTPUpstream
	client *http.Client
	calls  atomic.Int32
}

func (h *chainHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	h.calls.Add(1)
	if req.URL.Hostname() != "127.0.0.1" && req.URL.Hostname() != "localhost" && req.URL.Hostname() != "no-such-host.invalid" {
		return nil, errors.New("unit transport refused upstream " + req.URL.Host)
	}
	if h.client != nil {
		return h.client.Do(req)
	}
	return (&http.Client{Timeout: time.Second}).Do(req)
}
func (h *chainHTTP) DoWithTLS(req *http.Request, proxy string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return h.Do(req, proxy, id, n)
}

type chainEncryptor struct{}

func (chainEncryptor) Encrypt(s string) (string, error) { return s, nil }
func (chainEncryptor) Decrypt(s string) (string, error) { return s, nil }

type chainMonitors struct {
	service.ChannelMonitorRepository
	w   *chainWrites
	row *service.ChannelMonitor
}

func (r *chainMonitors) GetByID(context.Context, int64) (*service.ChannelMonitor, error) {
	m := *r.row
	m.ExtraHeaders = maps.Clone(m.ExtraHeaders)
	return &m, nil
}
func (r *chainMonitors) Create(ctx context.Context, m *service.ChannelMonitor) error {
	r.w.record(ctx, "monitor.create")
	m.ID = 10
	r.row = m
	return nil
}
func (r *chainMonitors) Update(ctx context.Context, m *service.ChannelMonitor) error {
	r.w.record(ctx, "monitor.update")
	r.row = m
	return nil
}

type chainTemplates struct {
	service.ChannelMonitorRequestTemplateRepository
	w   *chainWrites
	row *service.ChannelMonitorRequestTemplate
}

func (r *chainTemplates) GetByID(context.Context, int64) (*service.ChannelMonitorRequestTemplate, error) {
	return r.row, nil
}
func (r *chainTemplates) List(context.Context, service.ChannelMonitorRequestTemplateListParams) ([]*service.ChannelMonitorRequestTemplate, error) {
	return []*service.ChannelMonitorRequestTemplate{r.row}, nil
}
func (r *chainTemplates) Update(ctx context.Context, row *service.ChannelMonitorRequestTemplate) error {
	r.w.record(ctx, "template.update")
	r.row = row
	return nil
}
func (r *chainTemplates) CountAssociatedMonitors(context.Context, int64) (int64, error) {
	return 1, nil
}
func (r *chainTemplates) ApplyToMonitors(ctx context.Context, _ int64, _ []int64) (int64, error) {
	r.w.record(ctx, "template.apply")
	return 1, nil
}

type chainRedeems struct {
	stubRedeemCodeRepo
	w    *chainWrites
	rows map[int64]*service.RedeemCode
}

func (r *chainRedeems) GetByID(_ context.Context, id int64) (*service.RedeemCode, error) {
	if v := r.rows[id]; v != nil {
		return v, nil
	}
	return nil, service.ErrRedeemCodeNotFound
}
func (r *chainRedeems) BatchUpdate(ctx context.Context, _ []int64, _ service.RedeemCodeBatchUpdateFields) (int64, error) {
	r.w.record(ctx, "redeem.update")
	return 1, nil
}

type serviceDenyCase struct {
	method, path, body, reason, field string
	status                            int
}

var serviceEnforcedDenyCases = map[operatoraccess.FieldRule]serviceDenyCase{
	"account_create":      {"POST", "/accounts", `{"name":"a","platform":"openai","type":"apikey","credentials":{"base_url":"https://203.0.113.11"}}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "credentials.base_url", 403},
	"account_update":      {"PUT", "/accounts/10", `{"proxy_id":2}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "proxy_id", 403},
	"account_bulk_update": {"POST", "/accounts/bulk-update", `{"account_ids":[10],"proxy_id":2}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "proxy_id", 403},
	"account_oauth_apply": {"POST", "/accounts/10/apply-oauth-credentials", `{"type":"oauth","credentials":{},"extra":{"custom_base_url":"https://evil.example"}}`, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "extra.custom_base_url", 403},
	"user_create":         {"POST", "/users", `{"email":"x@example.com","password":"password123","balance":5}`, "ADMIN_FIELD_FORBIDDEN", "balance", 400},
	"user_update":         {"PUT", "/users/1", `{"balance":5}`, "ADMIN_FIELD_FORBIDDEN", "balance", 400},
	"user_bind_identity":  {"POST", "/users/1/auth-identities", `{"provider_type":"email","provider_key":"email","provider_subject":"x@example.com","metadata":{"source":"auth_service_dual_write"}}`, "ADMIN_FIELD_FORBIDDEN", "metadata", 400},
	"user_replace_group":  {"POST", "/users/1/replace-group", `{"old_group_id":1,"new_group_id":2}`, "ADMIN_FIELD_FORBIDDEN", "new_group_id", 400},
	"apikey_group":        {"PUT", "/api-keys/1", `{"group_id":2}`, "ADMIN_FIELD_FORBIDDEN", "group_id", 400},
	"group_create":        {"POST", "/groups", `{"name":"g","platform":"openai","rate_multiplier":1,"is_exclusive":false}`, "ADMIN_FIELD_FORBIDDEN", "is_exclusive", 400},
	"group_update":        {"PUT", "/groups/1", `{"daily_limit_usd":null}`, "ADMIN_FIELD_FORBIDDEN", "daily_limit_usd", 400},
	"group_delete":        {"DELETE", "/groups/1", "", "GROUP_NOT_EMPTY", "", 409},
	// user_search suppresses a predicate rather than rejecting a request. Its deny
	// assertion observes the repository context; the PostgreSQL test proves SQL behavior.
	"user_search": {"GET", "/users?search=secret", "", "", "", 200},
}

func TestEveryServiceEnforcedFieldHasAServiceHook(t *testing.T) {
	require.Len(t, serviceEnforcedDenyCases, len(operatoraccess.ServiceEnforcedFields))
	for id := range operatoraccess.ServiceEnforcedFields {
		tc, ok := serviceEnforcedDenyCases[id]
		require.True(t, ok, string(id))
		t.Run(string(id), func(t *testing.T) {
			f := newOperatorChain(t, service.RoleOperator, true, nil, false)
			if id == "account_oauth_apply" {
				f.accounts.row.Type = "oauth"
			}
			if id == "group_delete" {
				f.groups.nonempty = true
			}
			if id == "user_search" {
				rec := f.do(tc.method, tc.path, tc.body)
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.True(t, f.users.searchOperator)
				f.w.empty(t)
				return
			}
			chainDeny(t, f, tc.method, tc.path, tc.body, tc.status, tc.reason, tc.field)
		})
	}
}

// The repository boundary records all mutations, including best-effort writes.
func (r *chainAccounts) ClearError(ctx context.Context, _ int64) error {
	r.w.record(ctx, "account.clear_error")
	return nil
}
func (r *chainMonitors) List(ctx context.Context, _ service.ChannelMonitorListParams) ([]*service.ChannelMonitor, int64, error) {
	m, e := r.GetByID(ctx, 10)
	return []*service.ChannelMonitor{m}, 1, e
}
func (r *chainMonitors) ListLatestForMonitorIDs(context.Context, []int64) (map[int64][]*service.ChannelMonitorLatest, error) {
	return map[int64][]*service.ChannelMonitorLatest{}, nil
}
func (r *chainMonitors) ComputeAvailabilityForMonitors(context.Context, []int64, int) (map[int64][]*service.ChannelMonitorAvailability, error) {
	return map[int64][]*service.ChannelMonitorAvailability{}, nil
}
func (r *chainMonitors) ListRecentHistoryForMonitors(context.Context, []int64, map[int64]string, int) (map[int64][]*service.ChannelMonitorHistoryEntry, error) {
	return map[int64][]*service.ChannelMonitorHistoryEntry{}, nil
}

type chainDrive struct{}

func (chainDrive) GetStorageQuota(context.Context, string, string) (*geminicli.DriveStorageInfo, error) {
	return &geminicli.DriveStorageInfo{Limit: 15 * 1024 * 1024 * 1024}, nil
}

type chainAudit struct{ service.AuditLogRepository }

func (chainAudit) GetByID(_ context.Context, id int64) (*service.AuditLog, error) {
	if id == 11 {
		return &service.AuditLog{ID: 11, Method: "PUT", Path: "/api/v1/admin/channel-monitors/:id", RequestBody: `{"extra_headers":{"x-secret":"audit-secret"},"name":"visible"}`}, nil
	}
	return &service.AuditLog{ID: 10, Method: "PUT", Path: "/api/v1/admin/channel-monitors/:id", RequestBody: `{"extra_headers":{"x-secret":"truncated-secret"}`, Extra: map[string]any{"body_truncated": true}}, nil
}

type chainAxisCase struct {
	method, path, body string
	status             int
	reason, field      string
	flag               operatoraccess.Flag
	flagStillDenied    bool
	setup              func(*operatorChain)
	readOnly           bool
}

func chainFindingAxes(t *testing.T, finding string) {
	cases := map[string]chainAxisCase{
		"TestOperatorCreateUserGetsNoSignupGrants":                {"POST", "/users", `{"email":"new@example.com","password":"password123","balance":5}`, 400, "ADMIN_FIELD_FORBIDDEN", "balance", operatoraccess.FlagUsersBalanceAdjust, false, nil, false},
		"TestOperatorPlacementNeedsBalanceFlag":                   {"PUT", "/accounts/10", `{"group_ids":[1]}`, 400, "ADMIN_FIELD_FORBIDDEN", "group_ids", operatoraccess.FlagUsersBalanceAdjust, false, nil, false},
		"TestOperatorNoncanonicalRequestsRejected":                {"PUT", "/users/1", `{"balance":null,"Balance":5}`, 400, "ADMIN_FIELD_FORBIDDEN", "body", operatoraccess.FlagUsersBalanceAdjust, true, nil, false},
		"TestOperatorGroupNullLimitIsAChange":                     {"PUT", "/groups/1", `{"daily_limit_usd":null}`, 400, "ADMIN_FIELD_FORBIDDEN", "daily_limit_usd", operatoraccess.FlagUsersBalanceAdjust, false, nil, false},
		"TestOperatorApplyOAuthCannotChangeAddress":               {"POST", "/accounts/10/apply-oauth-credentials", `{"type":"oauth","credentials":{},"extra":{"custom_base_url":"https://203.0.113.11"}}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "extra.custom_base_url", operatoraccess.FlagAccountsUpstreamAddress, true, func(f *operatorChain) { f.accounts.row.Type = "oauth" }, false},
		"TestOperatorBulkUpdateWithFiltersRejectsNetworkKeys":     {"POST", "/accounts/bulk-update", `{"filters":{"platform":"openai"},"proxy_id":2}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "proxy_id", operatoraccess.FlagAccountsUpstreamAddress, true, nil, false},
		"TestOperatorRefreshableAccountNetworkLocked":             {"PUT", "/accounts/10", `{"proxy_id":2,"credentials":{"api_key":"fresh"}}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "proxy_id", operatoraccess.FlagAccountsUpstreamAddress, true, func(f *operatorChain) { f.accounts.row.Type = "oauth" }, false},
		"TestOperatorUpstreamAddressMatrix":                       {"PUT", "/accounts/10", `{"credentials":{"api_key":"fresh","base_url":"https://203.0.113.11"}}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "credentials.base_url", operatoraccess.FlagAccountsUpstreamAddress, false, nil, false},
		"TestProtectedCallsFromBatchAndIdempotentPathsCarryActor": {"POST", "/users/batch-concurrency", `{"user_ids":[1],"concurrency":2,"mode":"set"}`, 200, "", "", operatoraccess.FlagUsersBalanceAdjust, false, nil, false},
		"TestOperatorAccountTestStreamHidesAddress":               {"GET", "/accounts/10", "", 200, "", "", operatoraccess.FlagAccountsUpstreamAddress, false, nil, true},
		"TestOperatorCannotDeleteFallbackOriginThenRevert":        {"POST", "/accounts/10/revert-proxy-fallback", `{}`, 403, "SUPER_ADMIN_REQUIRED", "", operatoraccess.FlagAccountsUpstreamAddress, true, func(f *operatorChain) { f.accounts.row.ProxyFallbackOriginID = chainPtr(int64(10)) }, false},
		"TestOperatorReservedEmailAndGroupDelete":                 {"DELETE", "/groups/1", "", 409, "GROUP_NOT_EMPTY", "", operatoraccess.FlagUsersBalanceAdjust, false, func(f *operatorChain) { f.groups.nonempty = true }, false},
		"TestOperatorMonitorAndTemplateHeaders":                   {"PUT", "/channel-monitors/10", `{"endpoint":"https://203.0.113.11","api_key":"fresh"}`, 400, "ADMIN_FIELD_FORBIDDEN", "endpoint", operatoraccess.FlagAccountsUpstreamAddress, false, nil, false},
		"TestOperatorProxyChainAndExpiry":                         {"PUT", "/proxies/10", `{"host":"203.0.113.11","password":"fresh"}`, 403, "OPERATOR_NETWORK_CHANGE_FORBIDDEN", "host", operatoraccess.FlagAccountsUpstreamAddress, false, nil, false},
		"TestOperatorReadOnlyViewsHideSecrets":                    {"GET", "/accounts/10", "", 200, "", "", operatoraccess.FlagAccountsUpstreamAddress, false, nil, true},
		"TestOperatorRedeemBatchUpdateRejectsLedgerAndAdminRows":  {"POST", "/redeem-codes/batch-update", `{"ids":[10],"fields":{"notes":"safe"}}`, 403, "ADMIN_PERMISSION_DENIED", "", operatoraccess.FlagRedeemCodesIssue, false, nil, false},
		"TestOperatorBindIdentityRejectsMetadata":                 {"POST", "/users/1/auth-identities", `{"provider_type":"email","provider_key":"email","provider_subject":"new@example.com","metadata":{"source":"auth_service_dual_write"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "metadata", operatoraccess.FlagUsersBalanceAdjust, true, nil, false},
		"TestOperatorAccountBillingExtrasNeedFlag":                {"PUT", "/accounts/10", `{"extra":{"cache_ttl_override_enabled":true}}`, 400, "ADMIN_FIELD_FORBIDDEN", "extra.cache_ttl_override_enabled", operatoraccess.FlagUsersBalanceAdjust, false, nil, false},
		"TestOperatorAccountRejectsInjectedAWSRegion": {"PUT", "/accounts/10", `{"credentials":{"aws_region":"x@127.0.0.1/","api_key":"fresh"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "credentials.aws_region", operatoraccess.FlagAccountsUpstreamAddress, true, func(f *operatorChain) {
			f.accounts.row.Type = "bedrock"
			f.accounts.row.Credentials["aws_region"] = "us-east-1"
		}, false},
		"TestOperatorDeleteGroupWithProtectedMembers": {"DELETE", "/groups/1", "", 403, "ADMIN_TARGET_FORBIDDEN", "", operatoraccess.FlagUsersBalanceAdjust, true, func(f *operatorChain) { f.query.protected = true }, false},
	}
	tc, ok := cases[finding]
	require.True(t, ok, finding)
	for _, axis := range []string{"super", "no_module", "operator", "flag", "user"} {
		t.Run(axis, func(t *testing.T) {
			role, mods := service.RoleOperator, true
			var flags []operatoraccess.Flag
			switch axis {
			case "super":
				role = service.RoleAdmin
			case "no_module":
				mods = false
			case "flag":
				flags = operatoraccess.Flags
			case "user":
				role = service.RoleUser
			}
			f := newOperatorChain(t, role, mods, flags, false)
			if tc.setup != nil {
				tc.setup(f)
			}
			status, reason, field := tc.status, tc.reason, tc.field
			if axis == "super" || axis == "flag" && !tc.flagStillDenied {
				status = 200
				reason = ""
				field = ""
			}
			if axis == "no_module" {
				status = 403
				reason = "ADMIN_PERMISSION_DENIED"
				if strings.Contains(finding, "CannotDeleteFallbackOrigin") {
					reason = "SUPER_ADMIN_REQUIRED"
				}
				field = ""
			}
			if axis == "user" {
				status = 403
				reason = ""
				field = ""
			}
			// Identity success uses the SQL transaction fixture below.
			if axis == "super" && strings.Contains(finding, "BindIdentity") {
				chainIdentitySQL(t, f, true)
			}
			rec := f.do(tc.method, tc.path, tc.body)
			require.Equal(t, status, rec.Code, rec.Body.String())
			if status >= 400 {
				if reason != "" {
					var out struct {
						Reason   string
						Metadata map[string]string
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
					require.Equal(t, reason, out.Reason, rec.Body.String())
					if field != "" {
						require.Equal(t, field, out.Metadata["field"], rec.Body.String())
					}
				}
				f.w.empty(t)
			} else if !tc.readOnly {
				require.NotEmpty(t, f.w.calls)
			} else {
				f.w.empty(t)
			}
		})
	}
}

func chainIdentitySQL(t *testing.T, f *operatorChain, _ bool) {
	t.Helper()
	f.sql.ExpectBegin()
	f.sql.ExpectQuery(`SELECT .*auth_identities`).WillReturnRows(sqlmock.NewRows(authidentity.Columns))
	f.sql.ExpectQuery(`INSERT INTO "auth_identities"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
	f.sql.ExpectCommit()
}

type chainIdempotency struct {
	service.IdempotencyRepository
	mu   sync.Mutex
	rows map[string]*service.IdempotencyRecord
	w    *chainWrites
}

func (r *chainIdempotency) CreateProcessing(ctx context.Context, v *service.IdempotencyRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := v.Scope + v.IdempotencyKeyHash
	if r.rows[k] != nil {
		return false, nil
	}
	v.ID = 1
	c := *v
	r.rows[k] = &c
	r.w.record(ctx, "idempotency.claim")
	return true, nil
}
func (r *chainIdempotency) GetByScopeAndKeyHash(_ context.Context, scope, key string) (*service.IdempotencyRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v := r.rows[scope+key]; v != nil {
		c := *v
		return &c, nil
	}
	return nil, nil
}
func (r *chainIdempotency) MarkSucceeded(_ context.Context, id int64, status int, body string, expires time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.rows {
		if v.ID == id {
			v.Status = service.IdempotencyStatusSucceeded
			v.ResponseStatus = &status
			v.ResponseBody = &body
			v.ExpiresAt = expires
		}
	}
	return nil
}
func (r *chainIdempotency) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return true, nil
}
func (r *chainIdempotency) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return nil
}

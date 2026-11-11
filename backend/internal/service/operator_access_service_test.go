//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
)

type operatorPermissionRepoStub struct {
	raw             []byte
	found           bool
	err             error
	writes          int
	userID, actorID int64
}

func (r *operatorPermissionRepoStub) Get(context.Context, int64) ([]byte, bool, error) {
	return append([]byte(nil), r.raw...), r.found, r.err
}
func (r *operatorPermissionRepoStub) Upsert(_ context.Context, userID int64, raw []byte, actorID int64) error {
	if r.err != nil {
		return r.err
	}
	r.raw = append([]byte(nil), raw...)
	r.found = true
	r.writes++
	r.userID = userID
	r.actorID = actorID
	return nil
}

type operatorUserRepoStub struct {
	UserRepository
	user  *User
	err   error
	reads int
}

func (r *operatorUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	if r.user == nil || r.user.ID != id {
		return nil, ErrUserNotFound
	}
	return r.user, nil
}

type operatorSettingRepoStub struct {
	SettingRepository
	values map[string]string
	err    error
}

func (r *operatorSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return r.values, r.err
}
func (r *operatorSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return r.values, r.err
}
func (r *operatorSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	v, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func operatorTestService() (*OperatorAccessService, *operatorPermissionRepoStub, *operatorUserRepoStub) {
	repo := &operatorPermissionRepoStub{}
	users := &operatorUserRepoStub{user: &User{ID: 5, Role: RoleOperator, Status: StatusActive}}
	return &OperatorAccessService{repo: repo, userRepo: users}, repo, users
}
func TestPutOperatorPermissionsValidatesAndStoresCanonicalJSON(t *testing.T) {
	s, repo, users := operatorTestService()
	ctx := context.Background()
	for _, body := range []string{`{"modules":{"unknown":"view"}}`, `{"flags":["unknown"]}`, `{"unknown":1}`} {
		_, err := s.PutPermissions(ctx, 2, 5, []byte(body))
		require.Equal(t, "ADMIN_PERMISSION_INVALID", infraerrors.Reason(err))
		require.Zero(t, repo.writes)
	}
	w, err := s.PutPermissions(ctx, 2, 5, []byte(`{"flags":["data.export","accounts.upstream_address","data.export"],"modules":{"usage":"view"}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"accounts.upstream_address", "data.export"}, w.Flags)
	require.Equal(t, 1, repo.writes)
	require.Equal(t, int64(2), repo.actorID)
	require.Equal(t, int64(5), repo.userID)
	require.JSONEq(t, `{"modules":{"usage":"view"},"flags":["accounts.upstream_address","data.export"]}`, string(repo.raw))
	stored, err := s.GetPermissions(ctx, 5)
	require.NoError(t, err)
	require.Equal(t, w, stored)
	users.user.Role = RoleUser
	_, err = s.PutPermissions(ctx, 2, 5, []byte("{}"))
	require.NoError(t, err, "future operator permissions may be stored for a user")
	users.user = nil
	_, err = s.PutPermissions(ctx, 2, 5, []byte("{}"))
	require.ErrorIs(t, err, ErrUserNotFound)
	_, err = s.GetPermissions(ctx, 5)
	require.ErrorIs(t, err, ErrUserNotFound)
	users.user = &User{ID: 5, DeletedAt: new(time.Time)}
	_, err = s.GetPermissions(ctx, 5)
	require.ErrorIs(t, err, ErrUserNotFound)
	users.user = &User{ID: 5}
	repo.err = errors.New("storage failed")
	_, err = s.PutPermissions(ctx, 2, 5, []byte("{}"))
	require.Equal(t, "OPERATOR_ACCESS_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, 2, repo.writes)
}
func TestPutOperatorPermissionsRecordsAuditDiff(t *testing.T) {
	s, repo, _ := operatorTestService()
	repo.raw = []byte(`{"modules":{"usage":"view"},"flags":[]}`)
	repo.found = true
	s.auditLogService = NewAuditLogService(nil, nil)
	defer s.auditLogService.Stop()
	_, err := s.PutPermissions(context.Background(), 2, 5, []byte(`{"modules":{"accounts":"manage"},"flags":["data.export"]}`))
	require.NoError(t, err)
	select {
	case event := <-s.auditLogService.queue:
		require.Equal(t, "admin.operator_permissions.update", event.Action)
		require.Equal(t, int64(2), *event.ActorUserID)
		require.Equal(t, "PUT", event.Method)
		require.Equal(t, "/api/v1/admin/users/:id/operator-permissions", event.Path)
		require.Equal(t, 200, event.StatusCode)
		require.JSONEq(t, `{"user_id":5,"before":{"modules":{"usage":"view"},"flags":[]},"after":{"modules":{"accounts":"manage"},"flags":["data.export"]}}`, event.RequestBody)
	default:
		t.Fatal("audit event missing")
	}
	repo.err = errors.New("db unavailable")
	_, err = s.PutPermissions(context.Background(), 2, 5, []byte("{}"))
	require.Error(t, err)
	require.Empty(t, s.auditLogService.queue)
}
func TestLoadScopeIgnoresRowsOfNonOperators(t *testing.T) {
	s, repo, users := operatorTestService()
	repo.err = errors.New("should not read")
	for _, u := range []*User{nil, {ID: 5, Role: RoleUser}, {ID: 5, Role: RoleAdmin}} {
		sc, err := s.LoadScope(context.Background(), u)
		require.NoError(t, err)
		require.Nil(t, sc)
	}
	repo.err = nil
	sc, err := s.LoadScope(context.Background(), users.user)
	require.NoError(t, err)
	require.NotNil(t, sc)
	require.Equal(t, int64(5), sc.UserID)
	require.Same(t, s, sc.Guard)
	require.Empty(t, sc.Perms.Wire().Modules)
	repo.raw = []byte(`{"modules":{"usage":"manage"},"flags":["data.export"]}`)
	repo.found = true
	p, active, err := sc.Reload(context.Background())
	require.NoError(t, err)
	require.True(t, active)
	require.True(t, p.Can(operatoraccess.ModuleUsage, operatoraccess.LevelManage))
	for _, bad := range []string{`{"modules":{"unknown":"manage"}}`, "broken"} {
		repo.raw = []byte(bad)
		p, active, err = sc.Reload(context.Background())
		require.NoError(t, err)
		require.True(t, active)
		require.Empty(t, p.Wire().Modules)
	}
	for _, u := range []*User{nil, {ID: 5, Role: RoleUser, Status: StatusActive}, {ID: 5, Role: RoleOperator, Status: StatusDisabled}, {ID: 5, Role: RoleOperator, Status: StatusActive, DeletedAt: new(time.Time)}} {
		users.user = u
		_, active, err = sc.Reload(context.Background())
		require.NoError(t, err)
		require.False(t, active)
	}
	users.user = &User{ID: 5, Role: RoleOperator, Status: StatusActive}
	users.err = errors.New("user read failed")
	_, active, err = sc.Reload(context.Background())
	require.False(t, active)
	require.Equal(t, "OPERATOR_ACCESS_UNAVAILABLE", infraerrors.Reason(err))
}
func TestMeAccessOperatorSettings(t *testing.T) {
	s, repo, users := operatorTestService()
	repo.raw = []byte(`{"modules":{"accounts":"manage"},"flags":["data.export"]}`)
	repo.found = true
	settings := &operatorSettingRepoStub{values: map[string]string{SettingKeyOpsMonitoringEnabled: "true", SettingKeyOpsRealtimeMonitoringEnabled: "true", SettingKeyOpsQueryModeDefault: "auto", SettingKeyAdminUsageCacheHitRateEnabled: "true", SettingKeyAdminUsageTokenSpeedEnabled: "true", SettingKeyAccountQuotaNotifyEnabled: "true", SettingKeyWebSearchEmulationConfig: `{"enabled":true,"providers":[{"type":"brave","api_key":"never-exposed"}]}`, SettingPaymentEnabled: "true", SettingSubscriptionUSDToCNYRate: "7.2", SettingRechargeFeeRate: "0.03", SettingKeyCustomMenuItems: `[{"id":"private"}]`}}
	s.settingService = NewSettingService(settings, &config.Config{})
	s.opsService = &OpsService{}
	s.paymentConfigService = NewPaymentConfigService(nil, settings, nil)
	webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{})
	defer webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{})
	out, err := s.MeAccess(context.Background(), users.user)
	require.NoError(t, err)
	require.Equal(t, RoleOperator, out.Role)
	require.False(t, out.IsSuperAdmin)
	require.Equal(t, map[string]string{"accounts": "manage"}, out.Modules)
	require.Equal(t, []string{"data.export"}, out.Flags)
	require.Equal(t, "[]", string(out.CustomMenuItems))
	require.True(t, out.Settings.OpsMonitoringEnabled)
	require.True(t, out.Settings.OpsRealtimeMonitoringEnabled)
	require.Equal(t, "auto", out.Settings.OpsQueryModeDefault)
	require.True(t, out.Settings.AdminUsageCacheHitRateEnabled)
	require.True(t, out.Settings.AdminUsageTokenSpeedEnabled)
	require.True(t, out.Settings.PaymentEnabled)
	require.True(t, out.Settings.AccountQuotaNotifyEnabled)
	require.True(t, out.Settings.WebSearchEmulationEnabled)
	require.Equal(t, 7.2, out.Settings.PaymentSubscriptionUSDToCNYRate)
	require.Equal(t, 0.03, out.Settings.PaymentRechargeFeeRate)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "never-exposed")
	require.NotContains(t, string(raw), "private")
	settings.err = errors.New("settings unavailable")
	webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{})
	out, err = s.MeAccess(context.Background(), users.user)
	require.NoError(t, err)
	require.Equal(t, MeAccessSettings{}, out.Settings)
}
func TestMeAccessSuperAdminGetsAllModulesAndFlags(t *testing.T) {
	s, repo, _ := operatorTestService()
	repo.err = errors.New("must not read permissions")
	settings := &operatorSettingRepoStub{values: map[string]string{SettingKeyCustomMenuItems: `[{"id":"menu","label":"Help","url":"https://docs.example","visibility":"admin","icon_svg":"","sort_order":1}]`}}
	s.settingService = NewSettingService(settings, &config.Config{})
	webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{})
	defer webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{})
	out, err := s.MeAccess(context.Background(), &User{ID: 2, Role: RoleAdmin})
	require.NoError(t, err)
	require.True(t, out.IsSuperAdmin)
	require.Len(t, out.Modules, 17)
	for _, m := range operatoraccess.Modules {
		require.Equal(t, "manage", out.Modules[string(m)])
	}
	require.Len(t, out.Flags, 5)
	require.JSONEq(t, settings.values[SettingKeyCustomMenuItems], string(out.CustomMenuItems))
	for _, bad := range []string{"null", "invalid", "{}"} {
		settings.values[SettingKeyCustomMenuItems] = bad
		out, err = s.MeAccess(context.Background(), &User{Role: RoleAdmin})
		require.NoError(t, err)
		require.Equal(t, "[]", string(out.CustomMenuItems))
	}
}
func TestAllowsAdminExportReadsFreshPermissions(t *testing.T) {
	s, repo, users := operatorTestService()
	ctx := context.Background()
	require.False(t, s.AllowsAdminExport(ctx, 5))
	repo.raw = []byte(`{"modules":{"usage":"view"},"flags":["data.export"]}`)
	repo.found = true
	require.True(t, s.AllowsAdminExport(ctx, 5))
	repo.raw = []byte(`{"modules":{"usage":"view"},"flags":[]}`)
	require.False(t, s.AllowsAdminExport(ctx, 5))
	repo.raw = []byte(`{"modules":{"usage":"manage"},"flags":["data.export"]}`)
	require.True(t, s.AllowsAdminExport(ctx, 5))
	users.user.Role = RoleAdmin
	require.False(t, s.AllowsAdminExport(ctx, 5))
	users.user.Role = RoleOperator
	users.user.Status = StatusDisabled
	require.False(t, s.AllowsAdminExport(ctx, 5))
	require.GreaterOrEqual(t, users.reads, 6)
}
func TestOperatorAccessServiceConstructorArmsAdmin(t *testing.T) {
	admin := NewAdminService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	impl, ok := admin.(*adminServiceImpl)
	require.True(t, ok)
	require.False(t, impl.requireActor)
	access := NewOperatorAccessService(nil, nil, nil, nil, nil, nil, nil, admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NotNil(t, access)
	require.True(t, impl.requireActor)
}
func TestCanAccessAdminRole(t *testing.T) {
	var u *User
	require.False(t, u.CanAccessAdmin())
	for _, role := range []string{RoleAdmin, RoleOperator, RoleUser, "unknown", ""} {
		want := role == RoleAdmin || role == RoleOperator
		require.Equal(t, want, CanAccessAdminRole(role))
		require.Equal(t, want, (&User{Role: role}).CanAccessAdmin())
	}
}

//go:build unit

package service

import (
	"context"
	"errors"
	dbent "github.com/liulixin-lex/xy2api/ent"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/stretchr/testify/require"
	"testing"
)

var operatorActorReached = errors.New("recording repository reached")

type operatorActorAccounts struct {
	AccountRepository
	reads, writes int
	account       *Account
	shadow        *Account
}

func (r *operatorActorAccounts) GetByID(context.Context, int64) (*Account, error) {
	r.reads++
	if r.account != nil {
		return r.account, nil
	}
	return nil, operatorActorReached
}
func (r *operatorActorAccounts) RevertProxyFallback(context.Context, int64) error {
	r.writes++
	if r.account == nil {
		return operatorActorReached
	}
	r.account.ProxyID = operatorPtr(int64(3))
	return nil
}
func (r *operatorActorAccounts) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	if r.shadow != nil {
		return []*Account{r.shadow}, nil
	}
	return nil, nil
}
func (r *operatorActorAccounts) Update(context.Context, *Account) error { r.writes++; return nil }
func operatorProtectedCalls(s *adminServiceImpl) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"CreateAccount":      func(ctx context.Context) error { _, err := s.CreateAccount(ctx, &CreateAccountInput{}); return err },
		"UpdateAccount":      func(ctx context.Context) error { _, err := s.UpdateAccount(ctx, 1, &UpdateAccountInput{}); return err },
		"UpdateAccountExtra": func(ctx context.Context) error { return s.UpdateAccountExtra(ctx, 1, map[string]any{}) },
		"BulkUpdateAccounts": func(ctx context.Context) error {
			_, err := s.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{})
			return err
		},
		"RevertAccountProxyFallback": func(ctx context.Context) error { return s.RevertAccountProxyFallback(ctx, 1) },
		"CreateUser":                 func(ctx context.Context) error { _, err := s.CreateUser(ctx, &CreateUserInput{}); return err },
		"UpdateUser":                 func(ctx context.Context) error { _, err := s.UpdateUser(ctx, 1, &UpdateUserInput{}); return err },
		"BindUserAuthIdentity": func(ctx context.Context) error {
			_, err := s.BindUserAuthIdentity(ctx, 1, AdminBindAuthIdentityInput{})
			return err
		},
		"BatchUpdateConcurrency": func(ctx context.Context) error { _, err := s.BatchUpdateConcurrency(ctx, nil, 1, "set"); return err },
		"BatchUpdateLimits":      func(ctx context.Context) error { _, err := s.BatchUpdateLimits(ctx, nil, nil, nil); return err },
		"BatchUpdateUserStatus": func(ctx context.Context) error {
			_, err := s.BatchUpdateUserStatus(ctx, nil, StatusActive, 2)
			return err
		},
		"CreateGroup":              func(ctx context.Context) error { _, err := s.CreateGroup(ctx, &CreateGroupInput{}); return err },
		"UpdateGroup":              func(ctx context.Context) error { _, err := s.UpdateGroup(ctx, 1, &UpdateGroupInput{}); return err },
		"DeleteGroup":              func(ctx context.Context) error { return s.DeleteGroup(ctx, 1) },
		"AdminUpdateAPIKeyGroupID": func(ctx context.Context) error { _, err := s.AdminUpdateAPIKeyGroupID(ctx, 1, nil); return err },
		"ReplaceUserGroup":         func(ctx context.Context) error { _, err := s.ReplaceUserGroup(ctx, 1, 1, 2); return err },
	}
}
func TestProtectedServiceCallsWithoutActorFail(t *testing.T) {
	r := &operatorActorAccounts{account: operatorStoredAccount()}
	s := &adminServiceImpl{requireActor: true, accountRepo: r}
	calls := operatorProtectedCalls(s)
	require.Len(t, calls, 16)
	for name, call := range calls {
		t.Run(name, func(t *testing.T) { operatorRuleError(t, call(context.Background()), "ADMIN_ACTOR_MISSING", "", "") })
	}
	require.Zero(t, r.writes)
}
func TestSystemActorPassesProtectedHooks(t *testing.T) {
	operatorSystemEntryPoints(t)
	s := &adminServiceImpl{requireActor: true}
	for _, ctx := range []context.Context{operatoraccess.WithSystemActor(context.Background(), "unit-worker"), operatoraccess.WithSuperAdmin(context.Background(), 2)} {
		sc, svc, err := s.operatorScope(ctx)
		require.NoError(t, err)
		require.Nil(t, sc)
		require.Nil(t, svc)
		require.NoError(t, s.operatorAccountCreateRules(ctx, &CreateAccountInput{GroupIDs: []int64{1}}))
		require.NoError(t, s.operatorAccountUpdateRules(ctx, nil, nil))
		require.NoError(t, s.operatorAccountExtraRules(ctx, 1, nil))
		require.NoError(t, s.operatorAccountBulkRules(ctx, nil))
		require.NoError(t, s.operatorUserCreateRules(ctx, &CreateUserInput{Role: RoleAdmin}))
		require.NoError(t, s.operatorUserUpdateRules(ctx, 1, nil))
		require.NoError(t, s.operatorBindIdentityRules(ctx, AdminBindAuthIdentityInput{}))
		require.NoError(t, s.operatorCheckUserTargets(ctx, []int64{1}))
		require.NoError(t, s.operatorGroupCreateRules(ctx, &CreateGroupInput{}))
		require.NoError(t, s.operatorGroupUpdateRules(ctx, 1, nil))
		require.NoError(t, s.operatorAPIKeyGroupRules(ctx, 1, nil))
		require.NoError(t, s.operatorReplaceGroupRules(ctx, 1, 2))
		handled, err := s.operatorDeleteGroup(ctx, 1)
		require.NoError(t, err)
		require.False(t, handled)
		require.NoError(t, s.operatorSuperAdminOnly(ctx))
	}
}
func TestOperatorAccessServiceArmsActorRequirement(t *testing.T) {
	admin := NewAdminService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	impl := admin.(*adminServiceImpl)
	require.False(t, impl.requireActor)
	_, _, err := impl.operatorScope(context.Background())
	require.NoError(t, err)
	NewOperatorAccessService(nil, nil, nil, nil, nil, nil, nil, admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.True(t, impl.requireActor)
	_, _, err = impl.operatorScope(context.Background())
	require.Equal(t, "ADMIN_ACTOR_MISSING", infraerrors.Reason(err))
}
func TestOperatorSkipsSignupGrantsWithoutActor(t *testing.T) {
	s := &adminServiceImpl{requireActor: true}
	require.True(t, s.operatorSkipsSignupGrants(context.Background()))
	require.False(t, s.operatorSkipsSignupGrants(operatoraccess.WithSystemActor(context.Background(), "unit-worker")))
	require.True(t, s.operatorSkipsSignupGrants(operatorRulesContext(s)))
	require.False(t, s.operatorSkipsSignupGrants(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust)))
}
func TestOperatorCannotRevertProxyFallback(t *testing.T) {
	for _, typ := range []string{"oauth", "apikey"} {
		for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagAccountsUpstreamAddress}} {
			a := operatorStoredAccount()
			a.Type = typ
			r := &operatorActorAccounts{account: a, shadow: &Account{ID: 2}}
			s := &adminServiceImpl{requireActor: true, accountRepo: r}
			operatorRuleError(t, s.RevertAccountProxyFallback(context.Background(), 1), "ADMIN_ACTOR_MISSING", "", "")
			operatorRuleError(t, s.RevertAccountProxyFallback(operatorRulesContext(s, flags...), 1), "SUPER_ADMIN_REQUIRED", "", "")
			require.Zero(t, r.writes)
			require.Nil(t, r.account.ProxyID)
			require.Nil(t, r.shadow.ProxyID)
			for _, ctx := range []context.Context{operatoraccess.WithSuperAdmin(context.Background(), 2), operatoraccess.WithSystemActor(context.Background(), "unit-worker")} {
				require.NoError(t, s.RevertAccountProxyFallback(ctx, 1))
				require.Equal(t, int64(3), *r.shadow.ProxyID)
			}
			require.Equal(t, 4, r.writes)
		}
	}
}

// Named system actors must reach each protected entry point's persistence boundary.
type operatorSystemAccounts struct {
	AccountRepository
	calls int
}

func (r *operatorSystemAccounts) GetByID(context.Context, int64) (*Account, error) {
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "stored"}}, nil
}
func (r *operatorSystemAccounts) Create(context.Context, *Account) error {
	r.calls++
	return operatorActorReached
}
func (r *operatorSystemAccounts) Update(context.Context, *Account) error {
	r.calls++
	return operatorActorReached
}
func (r *operatorSystemAccounts) UpdateExtra(context.Context, int64, map[string]any) error {
	r.calls++
	return operatorActorReached
}
func (r *operatorSystemAccounts) BulkUpdate(context.Context, []int64, AccountBulkUpdate) (int64, error) {
	r.calls++
	return 0, operatorActorReached
}
func (r *operatorSystemAccounts) RevertProxyFallback(context.Context, int64) error {
	r.calls++
	return operatorActorReached
}

type operatorSystemUsers struct {
	UserRepository
	calls int
}

func (r *operatorSystemUsers) GetByID(context.Context, int64) (*User, error) {
	r.calls++
	return nil, operatorActorReached
}
func (r *operatorSystemUsers) Create(context.Context, *User) error {
	r.calls++
	return operatorActorReached
}
func (r *operatorSystemUsers) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	r.calls++
	return 0, operatorActorReached
}
func (r *operatorSystemUsers) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	r.calls++
	return 0, operatorActorReached
}
func (r *operatorSystemUsers) BatchUpdateStatus(context.Context, []int64, string) ([]int64, error) {
	r.calls++
	return nil, operatorActorReached
}

type operatorSystemGroups struct {
	GroupRepository
	calls int
}

func (r *operatorSystemGroups) GetByID(context.Context, int64) (*Group, error) {
	r.calls++
	return nil, operatorActorReached
}
func (r *operatorSystemGroups) Create(context.Context, *Group) error {
	r.calls++
	return operatorActorReached
}
func (r *operatorSystemGroups) DeleteCascade(context.Context, int64) ([]int64, error) {
	r.calls++
	return nil, operatorActorReached
}

type operatorSystemKeys struct {
	APIKeyRepository
	calls int
}

func (r *operatorSystemKeys) GetByID(context.Context, int64) (*APIKey, error) {
	r.calls++
	return nil, operatorActorReached
}
func operatorSystemEntryPoints(t *testing.T) {
	t.Helper()
	accounts, users, groups, keys := &operatorSystemAccounts{}, &operatorSystemUsers{}, &operatorSystemGroups{}, &operatorSystemKeys{}
	s := &adminServiceImpl{requireActor: true, accountRepo: accounts, userRepo: users, groupRepo: groups, apiKeyRepo: keys, entClient: &dbent.Client{}}
	calls := operatorProtectedCalls(s)
	calls["CreateAccount"] = func(ctx context.Context) error {
		_, err := s.CreateAccount(ctx, &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Name: "system account", Credentials: map[string]any{"api_key": "key"}, SkipDefaultGroupBind: true})
		return err
	}
	calls["UpdateAccountExtra"] = func(ctx context.Context) error { return s.UpdateAccountExtra(ctx, 1, map[string]any{"ordinary": true}) }
	calls["BulkUpdateAccounts"] = func(ctx context.Context) error {
		_, err := s.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{1}})
		return err
	}
	calls["CreateUser"] = func(ctx context.Context) error {
		_, err := s.CreateUser(ctx, &CreateUserInput{Password: "test-password", Role: RoleAdmin})
		return err
	}
	calls["CreateGroup"] = func(ctx context.Context) error {
		_, err := s.CreateGroup(ctx, &CreateGroupInput{Name: "system group", Platform: PlatformOpenAI, RateMultiplier: 1})
		return err
	}
	calls["BatchUpdateConcurrency"] = func(ctx context.Context) error {
		_, err := s.BatchUpdateConcurrency(ctx, []int64{1}, 1, "set")
		return err
	}
	calls["BatchUpdateLimits"] = func(ctx context.Context) error {
		_, err := s.BatchUpdateLimits(ctx, []int64{1}, operatorPtr(1), nil)
		return err
	}
	calls["BatchUpdateUserStatus"] = func(ctx context.Context) error {
		_, err := s.BatchUpdateUserStatus(ctx, []int64{1}, StatusActive, 2)
		return err
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			before := accounts.calls + users.calls + groups.calls + keys.calls
			err := call(operatoraccess.WithSystemActor(context.Background(), "unit-worker"))
			require.ErrorIs(t, err, operatorActorReached)
			require.Equal(t, before+1, accounts.calls+users.calls+groups.calls+keys.calls)
		})
	}
}

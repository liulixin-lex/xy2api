//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

type operatorRulesUsers struct {
	UserRepository
	rows   map[int64]*User
	writes int
	admins int64
}

func (r *operatorRulesUsers) GetByID(_ context.Context, id int64) (*User, error) {
	if u := r.rows[id]; u != nil {
		copy := *u
		return &copy, nil
	}
	return nil, ErrUserNotFound
}
func (r *operatorRulesUsers) GetByIDIncludeDeleted(ctx context.Context, id int64) (*User, error) {
	return r.GetByID(ctx, id)
}
func (r *operatorRulesUsers) Create(_ context.Context, u *User) error {
	r.writes++
	u.ID = 100
	r.rows[u.ID] = u
	return nil
}
func (r *operatorRulesUsers) Update(_ context.Context, u *User, _ UserUpdateFields) error {
	r.writes++
	r.rows[u.ID] = u
	return nil
}
func (r *operatorRulesUsers) ListWithFilters(_ context.Context, _ pagination.PaginationParams, f UserListFilters) ([]User, *pagination.PaginationResult, error) {
	return nil, &pagination.PaginationResult{Total: r.admins}, nil
}
func (r *operatorRulesUsers) BatchUpdateStatus(_ context.Context, ids []int64, _ string) ([]int64, error) {
	r.writes++
	return ids, nil
}

type operatorRulesGroups struct {
	GroupRepository
	rows           map[int64]*Group
	cascade, empty int
	notEmpty       bool
	err            error
}

func (r *operatorRulesGroups) GetByID(_ context.Context, id int64) (*Group, error) {
	if r.err != nil {
		return nil, r.err
	}
	if g := r.rows[id]; g != nil {
		return g, nil
	}
	return nil, ErrGroupNotFound
}
func (r *operatorRulesGroups) DeleteCascade(context.Context, int64) ([]int64, error) {
	r.cascade++
	return nil, nil
}
func (r *operatorRulesGroups) DeleteCascadeIfEmpty(context.Context, int64) ([]int64, error) {
	r.empty++
	if r.notEmpty {
		return nil, ErrGroupNotEmpty
	}
	return nil, nil
}

type operatorGrantSpy struct{ calls int }

func (r *operatorGrantSpy) AssignOrExtendSubscription(context.Context, *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	r.calls++
	return nil, false, nil
}
func operatorUserFixture() (*adminServiceImpl, *operatorRulesUsers, *operatorRulesGroups) {
	u := &operatorRulesUsers{rows: map[int64]*User{1: {ID: 1, Role: RoleUser, Email: "user@example.com", AllowedGroups: []int64{1}}, 2: {ID: 2, Role: RoleAdmin}, 7: {ID: 7, Role: RoleOperator}}, admins: 1}
	g := &operatorRulesGroups{rows: map[int64]*Group{1: {ID: 1, IsExclusive: true}, 2: {ID: 2, IsExclusive: true}, 3: {ID: 3}, 4: {ID: 4, IsExclusive: true, SubscriptionType: "subscription"}}}
	return &adminServiceImpl{userRepo: u, groupRepo: g}, u, g
}
func TestOperatorUserCreateAndUpdateRules(t *testing.T) {
	s, _, _ := operatorUserFixture()
	ctx := operatorRulesContext(s)
	for _, in := range []*CreateUserInput{{Role: RoleUser}, {Balance: operatorPtr(1.0)}, {AllowedGroups: []int64{2}}} {
		require.Error(t, s.operatorUserCreateRules(ctx, in))
	}
	for _, in := range []*UpdateUserInput{{Role: RoleAdmin}, {Balance: operatorPtr(0.0)}, {GroupRates: map[int64]*float64{}}, {AllowedGroups: &[]int64{1, 2}}} {
		require.Error(t, s.operatorUserUpdateRules(ctx, 1, in))
	}
	require.NoError(t, s.operatorUserCreateRules(ctx, &CreateUserInput{AllowedGroups: []int64{3, 4}}))
	require.NoError(t, s.operatorUserUpdateRules(ctx, 1, &UpdateUserInput{AllowedGroups: &[]int64{1, 3}}))
	require.NoError(t, s.operatorUserUpdateRules(ctx, 1, &UpdateUserInput{AllowedGroups: &[]int64{}}))
	operatorRuleError(t, s.operatorUserUpdateRules(ctx, 2, &UpdateUserInput{}), "ADMIN_TARGET_FORBIDDEN", "", "")
	require.NoError(t, s.operatorUserUpdateRules(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust), 1, &UpdateUserInput{Balance: operatorPtr(5.0)}))
}
func TestOperatorCreateUserSkipsDefaultsWithoutFlag(t *testing.T) {
	s, users, _ := operatorUserFixture()
	spy := &operatorGrantSpy{}
	s.defaultSubAssigner = spy
	s.settingService = &SettingService{cfg: &config.Config{}, settingRepo: &operatorSettingRepoStub{values: map[string]string{SettingKeyDefaultBalance: "5", SettingKeyDefaultSubscriptions: `[{"group_id":4,"validity_days":30}]`}}}
	for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagUsersBalanceAdjust}} {
		user, err := s.CreateUser(operatorRulesContext(s, flags...), &CreateUserInput{Email: "fresh@example.com", Password: "safe-password"})
		require.NoError(t, err)
		if len(flags) == 0 {
			require.Zero(t, user.Balance)
			require.Zero(t, spy.calls)
		} else {
			require.Equal(t, 5.0, user.Balance)
			require.Equal(t, 1, spy.calls)
		}
	}
	require.Equal(t, 2, users.writes)
}
func TestOperatorBatchUserTargetsCheckedInService(t *testing.T) {
	s, users, _ := operatorUserFixture()
	ctx := operatorRulesContext(s)
	for _, id := range []int64{2, 7} {
		_, err := s.BatchUpdateConcurrency(ctx, []int64{1, id}, 10, "set")
		operatorRuleError(t, err, "ADMIN_TARGET_FORBIDDEN", "", "")
		_, err = s.BatchUpdateLimits(ctx, []int64{1, id}, operatorPtr(10), nil)
		require.Error(t, err)
		_, err = s.BatchUpdateUserStatus(ctx, []int64{1, id}, StatusDisabled, 7)
		require.Error(t, err)
	}
	require.Zero(t, users.writes)
	require.NoError(t, s.operatorCheckUserTargets(ctx, []int64{-1, 0, 1, 1}))
	_, err := s.BatchUpdateUserStatus(ctx, []int64{1}, StatusDisabled, 7)
	require.NoError(t, err)
	require.Equal(t, 1, users.writes)
}
func TestOperatorBindIdentityRejectsMetadata(t *testing.T) {
	s, _, _ := operatorUserFixture()
	for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagUsersBalanceAdjust}} {
		ctx := operatorRulesContext(s, flags...)
		operatorRuleError(t, s.operatorBindIdentityRules(ctx, AdminBindAuthIdentityInput{Metadata: map[string]any{"source": "auth_service_dual_write"}}), "ADMIN_FIELD_FORBIDDEN", "metadata", "")
		operatorRuleError(t, s.operatorBindIdentityRules(ctx, AdminBindAuthIdentityInput{Channel: &AdminBindAuthIdentityChannelInput{Metadata: map[string]any{"source": "forged"}}}), "ADMIN_FIELD_FORBIDDEN", "channel.metadata", "")
		require.NoError(t, s.operatorBindIdentityRules(ctx, AdminBindAuthIdentityInput{Metadata: map[string]any{}}))
	}
}
func TestOperatorCannotCreateOrRenameUserToReservedEmail(t *testing.T) {
	s, users, _ := operatorUserFixture()
	for _, domain := range []string{LinuxDoConnectSyntheticEmailDomain, OIDCConnectSyntheticEmailDomain, WeChatConnectSyntheticEmailDomain, DingTalkConnectSyntheticEmailDomain} {
		for _, email := range []string{"actor" + domain, "  " + strings.ToUpper("actor"+domain) + "  "} {
			ctx := operatorRulesContext(s)
			operatorRuleError(t, s.operatorUserCreateRules(ctx, &CreateUserInput{Email: email}), "ADMIN_FIELD_FORBIDDEN", "email", "")
			operatorRuleError(t, s.operatorUserUpdateRules(ctx, 1, &UpdateUserInput{Email: email}), "ADMIN_FIELD_FORBIDDEN", "email", "")
			users.rows[1].Email = strings.TrimSpace(email)
			require.NoError(t, s.operatorUserUpdateRules(ctx, 1, &UpdateUserInput{Email: email}))
			users.rows[1].Email = "user@example.com"
			require.NoError(t, s.operatorUserCreateRules(operatorRulesContext(s, operatoraccess.FlagUsersBalanceAdjust), &CreateUserInput{Email: email}))
		}
	}
	require.Zero(t, users.writes)
}

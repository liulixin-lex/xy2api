//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOperatorAccessQueryRepoGroupHasProtectedMembers(t *testing.T) {
	ctx := context.Background()
	repo := NewOperatorAccessQueryRepository(integrationDB)
	for _, source := range []string{"subscription", "allowed_group"} {
		for _, role := range []string{service.RoleAdmin, service.RoleOperator, service.RoleUser} {
			for _, deletedUser := range []bool{false, true} {
				t.Run(source+"/"+role+map[bool]string{false: "/live", true: "/deleted"}[deletedUser], func(t *testing.T) {
					g := mustCreateCategoryGroup(t, groupCategoryTestPrefix())
					u := operatorRepositoryUser(t, role)
					self := operatorRepositoryUser(t, service.RoleOperator)
					found, err := repo.GroupHasProtectedMembers(ctx, g.ID, self.ID)
					require.NoError(t, err)
					require.False(t, found)
					var subID int64
					if source == "subscription" {
						sub := mustCreateSubscription(t, testEntClient(t), &service.UserSubscription{UserID: u.ID, GroupID: g.ID})
						subID = sub.ID
					} else {
						_, err = integrationDB.Exec(`INSERT INTO user_allowed_groups(user_id,group_id) VALUES($1,$2)`, u.ID, g.ID)
						require.NoError(t, err)
					}
					if deletedUser {
						_, err = integrationDB.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, u.ID)
						require.NoError(t, err)
					}
					found, err = repo.GroupHasProtectedMembers(ctx, g.ID, self.ID)
					require.NoError(t, err)
					require.Equal(t, role != service.RoleUser, found)
					found, err = repo.GroupHasProtectedMembers(ctx, g.ID, u.ID)
					require.NoError(t, err)
					require.True(t, found, "self is protected even when role is user")
					if source == "subscription" {
						_, err = integrationDB.Exec(`UPDATE user_subscriptions SET deleted_at=NOW() WHERE id=$1`, subID)
						require.NoError(t, err)
						found, err = repo.GroupHasProtectedMembers(ctx, g.ID, u.ID)
						require.NoError(t, err)
						require.False(t, found)
					}
				})
			}
		}
	}
}
func TestOperatorAccessQueryRepoProxyIDsUsedByAccounts(t *testing.T) {
	ctx := context.Background()
	repo := NewOperatorAccessQueryRepository(integrationDB)
	var ids []int64
	for i := 0; i < 4; i++ {
		p := mustCreateProxy(t, testEntClient(t), &service.Proxy{Name: groupCategoryTestPrefix(), Host: "192.0.2.1"})
		ids = append(ids, p.ID)
		t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM proxies WHERE id=$1`, p.ID) })
	}
	a := mustCreateCategoryAccount(t, groupCategoryTestPrefix())
	deleted := mustCreateCategoryAccount(t, groupCategoryTestPrefix())
	_, err := integrationDB.Exec(`UPDATE accounts SET proxy_id=$1,proxy_fallback_origin_id=$2 WHERE id=$3`, ids[0], ids[1], a.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE accounts SET proxy_id=$1,proxy_fallback_origin_id=$2,deleted_at=NOW() WHERE id=$3`, ids[2], ids[3], deleted.ID)
	require.NoError(t, err)
	used, err := repo.ProxyIDsUsedByAccounts(ctx, ids)
	require.NoError(t, err)
	require.ElementsMatch(t, ids[:2], used)
	used, err = repo.ProxyIDsUsedByAccounts(ctx, []int64{ids[1], ids[1], ids[2]})
	require.NoError(t, err)
	require.Equal(t, []int64{ids[1]}, used)
	used, err = repo.ProxyIDsUsedByAccounts(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, used)
}

func TestOperatorAccessQueryRepoAttributeValueOwnersIncludesDeletedUsers(t *testing.T) {
	ctx := context.Background()
	repo := NewOperatorAccessQueryRepository(integrationDB)
	u := operatorRepositoryUser(t, service.RoleOperator)
	client := testEntClient(t)
	def, err := client.UserAttributeDefinition.Create().SetKey(groupCategoryTestPrefix()).SetName("guard-test").SetType("text").Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM user_attribute_values WHERE attribute_id=$1`, def.ID)
		_, _ = integrationDB.Exec(`DELETE FROM user_attribute_definitions WHERE id=$1`, def.ID)
	})
	_, err = integrationDB.Exec(`INSERT INTO user_attribute_values(user_id,attribute_id,value) VALUES($1,$2,$3)`, u.ID, def.ID, "held")
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, u.ID)
	require.NoError(t, err)
	ids, err := repo.AttributeValueOwnerIDs(ctx, def.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{u.ID}, ids)
}

func TestUserGroupRateRepoIncludesSoftDeletedOwners(t *testing.T) {
	ctx := context.Background()
	g := mustCreateCategoryGroup(t, groupCategoryTestPrefix())
	u := operatorRepositoryUser(t, service.RoleOperator)
	_, err := integrationDB.Exec(`INSERT INTO user_group_rate_multipliers(user_id,group_id,rate_multiplier,rpm_override) VALUES($1,$2,$3,25)`, u.ID, g.ID, 1.25)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, u.ID)
	require.NoError(t, err)
	repo := NewUserGroupRateRepository(integrationDB)
	for _, rpmOnly := range []bool{false, true} {
		ids, err := repo.GetOwnerIDsByGroupID(ctx, g.ID, rpmOnly)
		require.NoError(t, err)
		require.Equal(t, []int64{u.ID}, ids)
	}
}

func TestUserGroupRateRepoSeparatesVisibleRowsAndOwners(t *testing.T) {
	ctx := context.Background()
	repo := NewUserGroupRateRepository(integrationDB)
	users := NewUserRepository(testEntClient(t), integrationDB)
	admin := service.NewAdminService(nil, users, nil, nil, nil, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	access := service.NewOperatorAccessService(nil, users, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	actor := operatorRepositoryUser(t, service.RoleOperator)
	adminActor := operatorRepositoryUser(t, service.RoleAdmin)
	for _, role := range []string{service.RoleUser, service.RoleOperator} {
		t.Run(role, func(t *testing.T) {
			g := mustCreateCategoryGroup(t, groupCategoryTestPrefix())
			live := operatorRepositoryUser(t, service.RoleUser)
			deleted := operatorRepositoryUser(t, role)
			_, err := integrationDB.Exec(`INSERT INTO user_group_rate_multipliers(user_id,group_id,rate_multiplier,rpm_override) VALUES($1,$3,1.5,NULL),($2,$3,1.25,25)`, live.ID, deleted.ID, g.ID)
			require.NoError(t, err)
			listCtx := operatoraccess.WithSuperAdmin(ctx, adminActor.ID)
			before, err := admin.GetGroupRateMultipliers(listCtx, g.ID)
			require.NoError(t, err)
			require.Len(t, before, 2)
			require.NoError(t, users.Delete(listCtx, deleted.ID))
			stored, err := users.GetByIDIncludeDeleted(ctx, deleted.ID)
			require.NoError(t, err)
			require.NotNil(t, stored.DeletedAt)
			after, err := admin.GetGroupRateMultipliers(listCtx, g.ID)
			require.NoError(t, err)
			require.Len(t, after, 1, "the super-admin list must exclude the deleted user's rate/RPM row")
			require.Equal(t, live.ID, after[0].UserID)
			require.Contains(t, before, after[0], "the remaining user's fields stay unchanged")
			for _, rpmOnly := range []bool{false, true} {
				owners, err := repo.GetOwnerIDsByGroupID(ctx, g.ID, rpmOnly)
				require.NoError(t, err)
				if rpmOnly {
					require.Equal(t, []int64{deleted.ID}, owners)
				} else {
					require.ElementsMatch(t, []int64{live.ID, deleted.ID}, owners)
				}
			}
			scope := &operatoraccess.Scope{UserID: actor.ID}
			for _, rule := range []operatoraccess.TargetRule{"group_rate_rows", "group_rpm_rows"} {
				request := operatoraccess.Request{Params: map[string]string{"id": fmt.Sprint(g.ID)}, Body: []byte("{}")}
				err := access.CheckTarget(ctx, scope, rule, request)
				if role == service.RoleOperator {
					require.Equal(t, "ADMIN_TARGET_FORBIDDEN", infraerrors.Reason(err))
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
}

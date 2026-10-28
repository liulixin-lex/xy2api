//go:build integration

package repository

import (
	"context"
	"testing"

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

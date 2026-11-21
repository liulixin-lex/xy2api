//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type oauthOnlyGroups struct{ *groupRepoStubForAdmin }

func (g oauthOnlyGroups) ListActiveByPlatform(context.Context, string) ([]Group, error) {
	return []Group{*g.getByID}, nil
}

func TestOAuthOnlyAccountWrites(t *testing.T) {
	for _, entry := range []string{"create", "default", "update", "bulk", "service-create", "service-update", "filtered-bulk"} {
		for _, state := range []string{"new", "existing", "disabled", "oauth", "setup-token"} {
			t.Run(entry+"/"+state, func(t *testing.T) {
				group := &Group{ID: 12, Name: "openai-default", Platform: PlatformOpenAI, RequireOAuthOnly: state != "disabled"}
				account := &Account{ID: 5, Name: "original", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive}
				if state == "oauth" {
					account.Type = AccountTypeOAuth
				}
				if state == "setup-token" {
					account.Type = AccountTypeSetupToken
				}
				if state == "existing" {
					account.GroupIDs = []int64{group.ID}
				}
				repo := &accountRepoStubForBulkUpdate{createID: 6, getByIDsAccounts: []*Account{account}, getByIDAccounts: map[int64]*Account{account.ID: account}, listData: []Account{*account}}
				groups := oauthOnlyGroups{&groupRepoStubForAdmin{getByID: group}}
				admin := &adminServiceImpl{accountRepo: repo, groupRepo: groups}
				basic := NewAccountService(repo, groups)
				requested := []int64{group.ID}
				var err error
				creating := entry == "create" || entry == "default" || entry == "service-create"
				switch entry {
				case "create", "default":
					input := &CreateAccountInput{Name: "created", Platform: account.Platform, Type: account.Type, GroupIDs: requested, SkipMixedChannelCheck: true}
					if entry == "default" {
						input.GroupIDs = nil
					}
					_, err = admin.CreateAccount(t.Context(), input)
				case "update":
					_, err = admin.UpdateAccount(t.Context(), account.ID, &UpdateAccountInput{Name: "changed", GroupIDs: &requested, SkipMixedChannelCheck: true})
				case "filtered-bulk":
					_, err = admin.BulkUpdateAccounts(t.Context(), &BulkUpdateAccountsInput{Name: "changed", Filters: &BulkUpdateAccountFilters{}, GroupIDs: &requested, SkipMixedChannelCheck: true})
				case "bulk":
					_, err = admin.BulkUpdateAccounts(t.Context(), &BulkUpdateAccountsInput{Name: "changed", AccountIDs: []int64{account.ID}, GroupIDs: &requested, SkipMixedChannelCheck: true})
				case "service-create":
					_, err = basic.Create(t.Context(), CreateAccountRequest{Name: "created", Platform: account.Platform, Type: account.Type, GroupIDs: requested})
				case "service-update":
					name := "changed"
					_, err = basic.Update(t.Context(), account.ID, UpdateAccountRequest{Name: &name, GroupIDs: &requested})
				}
				reject := state == "new" || state == "existing" && creating
				if reject {
					requireApplicationErrorReason(t, err, "GROUP_OAUTH_ONLY")
					require.Nil(t, repo.createAccount)
					require.Empty(t, repo.updatedAccounts)
					require.Zero(t, repo.bulkUpdateCalls)
					require.Empty(t, repo.bindGroupsCalls)
				} else {
					require.NoError(t, err)
					if creating {
						require.Equal(t, requested, repo.createAccount.GroupIDs)
						require.Empty(t, repo.bindGroupsCalls, "creation owns its bindings atomically")
					} else {
						require.Len(t, repo.bindGroupsCalls, 1)
					}
				}
			})
		}
	}
}

func TestOAuthOnlyLegacyUnrelatedEdits(t *testing.T) {
	for _, entry := range []string{"update", "bulk", "service-update"} {
		t.Run(entry, func(t *testing.T) {
			account := &Account{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: []int64{12}, Status: StatusActive}
			repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{account.ID: account}}
			svc := &adminServiceImpl{accountRepo: repo}
			var err error
			switch entry {
			case "update":
				_, err = svc.UpdateAccount(t.Context(), account.ID, &UpdateAccountInput{Name: "changed"})
			case "bulk":
				_, err = svc.BulkUpdateAccounts(t.Context(), &BulkUpdateAccountsInput{Name: "changed", AccountIDs: []int64{account.ID}})
			case "service-update":
				name := "changed"
				_, err = NewAccountService(repo, nil).Update(t.Context(), account.ID, UpdateAccountRequest{Name: &name})
			}
			require.NoError(t, err)
			require.Empty(t, repo.bindGroupsCalls)
		})
	}
}

func TestOAuthOnlyNewBindingAlongsideLegacy(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		account := &Account{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: []int64{12}}
		repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{account}, getByIDAccounts: map[int64]*Account{5: account}}
		groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{12: {ID: 12, Platform: PlatformOpenAI, RequireOAuthOnly: true}, 13: {ID: 13, Platform: PlatformOpenAI, RequireOAuthOnly: true}}}
		svc := &adminServiceImpl{accountRepo: repo, groupRepo: groups}
		ids := []int64{12, 13}
		var err error
		if bulk {
			_, err = svc.BulkUpdateAccounts(t.Context(), &BulkUpdateAccountsInput{AccountIDs: []int64{5}, GroupIDs: &ids, SkipMixedChannelCheck: true})
		} else {
			_, err = svc.UpdateAccount(t.Context(), 5, &UpdateAccountInput{GroupIDs: &ids, SkipMixedChannelCheck: true})
		}
		requireApplicationErrorReason(t, err, "GROUP_OAUTH_ONLY")
		require.Empty(t, repo.bindGroupsCalls)
	}
}

func TestOAuthOnlyDuplicateAccountAndShadow(t *testing.T) {
	groups := &groupRepoStubForAdmin{getByID: &Group{ID: 12, Platform: PlatformOpenAI, RequireOAuthOnly: true}}
	repo := newDuplicateAccountRepoStub()
	source := &Account{Name: "synthetic", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, GroupIDs: []int64{12}}
	require.NoError(t, repo.Create(t.Context(), source))
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo, groupRepo: groups}
	_, err := svc.DuplicateAccount(t.Context(), source.ID, "", "")
	requireApplicationErrorReason(t, err, "GROUP_OAUTH_ONLY")
	require.Len(t, repo.accounts, 1)
	_, err = svc.CreateShadow(t.Context(), source.ID, ShadowOptions{GroupIDs: []int64{12}})
	requireApplicationErrorReason(t, err, "SPARK_SHADOW_INVALID_PARENT")
	source.Type = AccountTypeOAuth
	require.NoError(t, repo.Update(t.Context(), source))
	shadow, err := svc.CreateShadow(t.Context(), source.ID, ShadowOptions{GroupIDs: []int64{12}})
	require.NoError(t, err)
	require.Equal(t, AccountTypeOAuth, shadow.Type)
	require.Equal(t, []int64{12}, shadow.GroupIDs)
}

func TestOAuthOnlyGroupCopyAndFlagEdits(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(map[bool]string{true: "create", false: "update"}[create], func(t *testing.T) {
			target := &Group{ID: 12, Name: "target", Platform: PlatformOpenAI, Status: StatusActive, RequireOAuthOnly: true, OAuthOnlyMismatchCount: 3}
			copied := false
			groups := &groupRepoStubForAdmin{createID: 12, getByIDByID: map[int64]*Group{12: target, 13: {ID: 13, Platform: PlatformOpenAI}}, copyAccountMembersFn: func(id int64, sources []int64, oauthOnly, replace bool) (int64, error) {
				copied = true
				require.Equal(t, int64(12), id)
				require.Equal(t, []int64{13}, sources)
				require.True(t, oauthOnly, "copy routes retain repository filtering")
				require.Equal(t, !create, replace)
				return 2, nil
			}}
			svc := &adminServiceImpl{groupRepo: groups}
			var got *Group
			var err error
			if create {
				got, err = svc.CreateGroup(t.Context(), &CreateGroupInput{Name: "target", Platform: PlatformOpenAI, RateMultiplier: 1, RequireOAuthOnly: true, CopyAccountsFromGroupIDs: []int64{13}})
			} else {
				got, err = svc.UpdateGroup(t.Context(), 12, &UpdateGroupInput{CopyAccountsFromGroupIDs: []int64{13}})
			}
			require.NoError(t, err)
			require.True(t, copied)
			require.Zero(t, got.OAuthOnlyMismatchCount)
		})
	}
	group := &Group{ID: 12, Name: "target", Platform: PlatformOpenAI, Status: StatusActive, OAuthOnlyMismatchCount: 3}
	svc := &adminServiceImpl{groupRepo: &groupRepoStubForAdmin{getByID: group}}
	enabled := true
	got, err := svc.UpdateGroup(t.Context(), 12, &UpdateGroupInput{RequireOAuthOnly: &enabled})
	require.NoError(t, err)
	require.EqualValues(t, 3, got.OAuthOnlyMismatchCount, "enabling the flag retains existing members and their count")
}

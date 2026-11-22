//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func runOAuthOnlyMembershipCounts(t *testing.T, client *dbent.Client, exec sqlExecutor) {
	ctx := context.Background()
	source, err := client.Group.Create().SetName("oauth-source").SetPlatform(service.PlatformOpenAI).SetRequireOauthOnly(true).Save(ctx)
	require.NoError(t, err)
	for _, fixture := range []struct {
		kind, status string
		deleted      bool
	}{
		{service.AccountTypeAPIKey, service.StatusActive, false},
		{service.AccountTypeAPIKey, service.StatusDisabled, false},
		{service.AccountTypeAPIKey, service.StatusActive, true},
		{service.AccountTypeOAuth, service.StatusActive, false},
		{service.AccountTypeSetupToken, service.StatusActive, false},
	} {
		create := client.Account.Create().SetName("synthetic-member").SetPlatform(service.PlatformOpenAI).SetType(fixture.kind).SetStatus(fixture.status).SetCredentials(map[string]any{})
		if fixture.deleted {
			create.SetDeletedAt(time.Now())
		}
		account, err := create.Save(ctx)
		require.NoError(t, err)
		_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(source.ID).Save(ctx)
		require.NoError(t, err)
	}
	groups := newGroupRepositoryWithSQL(client, exec)
	actual, err := groups.GetByID(ctx, source.ID)
	require.NoError(t, err)
	require.EqualValues(t, 4, actual.AccountCount)
	require.EqualValues(t, 2, actual.OAuthOnlyMismatchCount)
	var members int64
	require.NoError(t, scanSingleRow(ctx, exec, `SELECT COUNT(*) FROM account_groups ag JOIN accounts a ON a.id=ag.account_id WHERE ag.group_id=$1 AND a.type='apikey' AND a.deleted_at IS NULL`, []any{source.ID}, &members))
	require.Equal(t, members, actual.OAuthOnlyMismatchCount)
	for _, sortBy := range []string{"id", "account_count"} {
		list, _, err := groups.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100, SortBy: sortBy}, "", "", source.Name, nil)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, members, list[0].OAuthOnlyMismatchCount)
	}
	for _, listFn := range []func(context.Context) ([]service.Group, error){groups.ListActive, func(ctx context.Context) ([]service.Group, error) {
		return groups.ListActiveByPlatform(ctx, service.PlatformOpenAI)
	}} {
		list, err := listFn(ctx)
		require.NoError(t, err)
		found := false
		for _, group := range list {
			if group.ID == source.ID {
				found = true
				require.Equal(t, members, group.OAuthOnlyMismatchCount)
			}
		}
		require.True(t, found)
	}
	target, err := client.Group.Create().SetName("oauth-target").SetPlatform(service.PlatformOpenAI).SetRequireOauthOnly(true).Save(ctx)
	require.NoError(t, err)
	copied, err := groups.CopyAccountMembers(ctx, target.ID, []int64{source.ID}, true, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, copied, "OAuth and setup-token retain their existing eligibility")
	actual, err = groups.GetByID(ctx, target.ID)
	require.NoError(t, err)
	require.Zero(t, actual.OAuthOnlyMismatchCount)
	clone := &service.Group{Name: "oauth-clone", Platform: service.PlatformOpenAI, Status: service.StatusActive, SubscriptionType: "standard", RateMultiplier: 1, RequireOAuthOnly: true}
	require.NoError(t, groups.CreateFromSource(ctx, clone, source.ID))
	actual, err = groups.GetByID(ctx, clone.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, actual.AccountCount)
	require.Zero(t, actual.OAuthOnlyMismatchCount)
	// Copying elsewhere must neither remove nor disable legacy members in the source.
	actual, err = groups.GetByID(ctx, source.ID)
	require.NoError(t, err)
	require.EqualValues(t, 4, actual.AccountCount)
	require.EqualValues(t, 2, actual.OAuthOnlyMismatchCount)
}

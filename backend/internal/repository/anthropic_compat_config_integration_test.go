//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAnthropicCompatBulkSeedsAndAdminLifecyclePostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAdminAccountRepository(client, integrationDB, nil)
	svc := service.NewAdminService(nil, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ids := make([]int64, 0, 2)
	for i := 0; i < 2; i++ {
		account := &service.Account{Name: fmt.Sprintf("compat-seed-%d-%d", time.Now().UnixNano(), i), Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://relay.example"}, Extra: map[string]any{"quota_limit": 100}}
		require.NoError(t, repo.Create(ctx, account))
		ids = append(ids, account.ID)
		t.Cleanup(func() { _ = repo.Delete(ctx, account.ID) })
	}
	result, err := svc.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{AccountIDs: ids, Extra: map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCodeV2, service.AnthropicCompatIdentitySeedExtraKey: "untrusted"}})
	require.NoError(t, err)
	require.Equal(t, 2, result.Success)
	one, err := repo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	two, err := repo.GetByID(ctx, ids[1])
	require.NoError(t, err)
	seed := one.AnthropicCompatIdentitySeed()
	require.Len(t, seed, 64)
	require.NotEqual(t, seed, two.AnthropicCompatIdentitySeed())
	updated, err := svc.UpdateAccount(ctx, one.ID, &service.UpdateAccountInput{Extra: map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, seed, updated.AnthropicCompatIdentitySeed())
	require.Equal(t, service.AnthropicCompatProfileClaudeCodeV2, updated.Extra[service.AnthropicCompatProfileExtraKey])
	require.NotContains(t, updated.Extra, "quota_limit")
	_, err = svc.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{AccountIDs: ids, Credentials: map[string]any{"base_url": "https://changed.example"}})
	require.NoError(t, err)
	updated, err = repo.GetByID(ctx, one.ID)
	require.NoError(t, err)
	require.Len(t, updated.AnthropicCompatIdentitySeed(), 64)
	require.NotEqual(t, seed, updated.AnthropicCompatIdentitySeed())
	seed = updated.AnthropicCompatIdentitySeed()
	require.NoError(t, svc.UpdateAccountExtra(ctx, one.ID, map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCodeV2, service.AnthropicCompatIdentitySeedExtraKey: "untrusted"}))
	updated, err = repo.GetByID(ctx, one.ID)
	require.NoError(t, err)
	require.Equal(t, seed, updated.AnthropicCompatIdentitySeed())
}

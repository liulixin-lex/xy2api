//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// An admin edit must not overwrite the reset time written by RunReset.
func TestUpdateAccountPreservesQualityResetTime(t *testing.T) {
	const fresh = "2026-11-12 02:30:00.123456+00"
	for name, stale := range map[string]map[string]any{
		"older":  {"group_quality_reset_at": "2026-11-12T01:00:00Z", "custom": true},
		"absent": {"custom": true},
	} {
		t.Run(name, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Extra = map[string]any{"group_quality_reset_at": fresh}
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: account}}
			svc := &adminServiceImpl{accountRepo: repo}
			updated, err := svc.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Extra: stale})
			require.NoError(t, err)
			require.Equal(t, fresh, updated.Extra["group_quality_reset_at"])
			require.Equal(t, true, updated.Extra["custom"])
			require.NoError(t, svc.UpdateAccountExtra(context.Background(), 41, map[string]any{
				"group_quality_reset_at": "2026-11-12T01:00:00Z",
				"custom":                 false,
			}))
			require.Equal(t, fresh, repo.accounts[41].Extra["group_quality_reset_at"])
			require.Equal(t, false, repo.accounts[41].Extra["custom"])
		})
	}
}

func TestUpdateAccountDropsInjectedQualityResetTime(t *testing.T) {
	account := ticketTestAccount(41)
	account.Extra = map[string]any{}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Extra: map[string]any{
		"group_quality_reset_at": "2026-11-12T01:00:00Z",
		"custom":                 true,
	}})
	require.NoError(t, err)
	require.NotContains(t, updated.Extra, "group_quality_reset_at")
	require.Equal(t, true, updated.Extra["custom"])
	require.NoError(t, svc.UpdateAccountExtra(context.Background(), 41, map[string]any{
		"group_quality_reset_at": "2026-11-12T01:00:00Z",
	}))
	require.NotContains(t, repo.accounts[41].Extra, "group_quality_reset_at")
}

func TestCreateAccountDropsQualityResetTime(t *testing.T) {
	account, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, map[string]any{
		"group_quality_reset_at": "2026-11-12T01:00:00Z",
		"custom":                 true,
	})
	require.NoError(t, err)
	require.NotContains(t, account.Extra, "group_quality_reset_at")
	require.Equal(t, true, account.Extra["custom"])
}

//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountUpdatePreservesQualityResetAfterRead(t *testing.T) {
	for _, name := range []string{"older", "absent", "nil_extra"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			quality, accountID, _ := qualityRepoFixture(t)
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			if name == "older" {
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{group_quality_reset_at}',to_jsonb($2::text)) WHERE id=$1`, accountID, "2026-11-12T01:00:00Z")
				require.NoError(t, err)
			}
			stale, err := repo.GetByID(ctx, accountID)
			require.NoError(t, err)
			stale.Priority = 17
			if name == "nil_extra" {
				stale.Extra = nil
			} else {
				stale.Extra["custom"] = true
			}

			// Reset after the caller's read, before its stale copy is written back.
			resetAt, err := quality.RunReset(ctx, accountID)
			require.NoError(t, err)
			require.NoError(t, repo.Update(ctx, stale))
			stored, err := repo.GetByID(ctx, accountID)
			require.NoError(t, err)
			resetText, ok := stored.Extra["group_quality_reset_at"].(string)
			require.True(t, ok)
			parsed, err := time.Parse(time.RFC3339Nano, resetText)
			require.NoError(t, err)
			require.True(t, resetAt.Equal(parsed))
			require.Equal(t, resetText, stale.Extra["group_quality_reset_at"])
			require.Equal(t, 17, stored.Priority)
			if name != "nil_extra" {
				require.Equal(t, true, stored.Extra["custom"])
			}
		})
	}
}

func TestAccountUpdateDropsInjectedQualityResetTime(t *testing.T) {
	ctx := context.Background()
	_, accountID, _ := qualityRepoFixture(t)
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	account, err := repo.GetByID(ctx, accountID)
	require.NoError(t, err)
	account.Extra["group_quality_reset_at"] = "2026-11-12T01:00:00Z"
	account.Extra["custom"] = true
	require.NoError(t, repo.Update(ctx, account))
	stored, err := repo.GetByID(ctx, accountID)
	require.NoError(t, err)
	require.NotContains(t, account.Extra, "group_quality_reset_at")
	require.NotContains(t, stored.Extra, "group_quality_reset_at")
	require.Equal(t, true, stored.Extra["custom"])
}

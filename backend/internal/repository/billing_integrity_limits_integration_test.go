//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyCreateLimits_ConcurrentAndDeletedKeys(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	u := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash"})
	r := &apiKeyRepository{client: client, sql: integrationDB}
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := &service.APIKey{UserID: u.ID, Name: "limit", Key: "sk-test-" + uuid.NewString(), Status: service.StatusAPIKeyActive}
			err := r.CreateWithLimits(ctx, k, 3, 3)
			if err == nil {
				created.Add(1)
			} else if !errors.Is(err, service.ErrAPIKeyCountExceeded) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(3), created.Load())
	_, err := integrationDB.Exec("UPDATE api_keys SET deleted_at = NOW() WHERE user_id = $1", u.ID)
	require.NoError(t, err)
	err = r.CreateWithLimits(ctx, &service.APIKey{UserID: u.ID, Name: "limit", Key: "sk-test-" + uuid.NewString(), Status: service.StatusAPIKeyActive}, 3, 3)
	require.ErrorIs(t, err, service.ErrAPIKeyCreateLimited, "deleting all keys must not replenish the hourly budget")
}

func TestBillingIntent_PlatformQuotaCommitsExactlyOnce(t *testing.T) {
	r, c := newBillingIntentFixture(t)
	c.QuotaPlatform = service.PlatformOpenAI
	_, err := integrationDB.Exec(`INSERT INTO user_platform_quotas (user_id, platform, daily_limit_usd, weekly_limit_usd, monthly_limit_usd)
		VALUES ($1, $2, 100, 100, 100)`, c.UserID, c.QuotaPlatform)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		res, err := r.Apply(context.Background(), c)
		require.NoError(t, err)
		require.Equal(t, i == 0, res.Applied)
	}
	var daily, weekly, monthly float64
	require.NoError(t, integrationDB.QueryRow(`SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd FROM user_platform_quotas
		WHERE user_id = $1 AND platform = $2`, c.UserID, c.QuotaPlatform).Scan(&daily, &weekly, &monthly))
	require.Equal(t, 1.25, daily)
	require.Equal(t, daily, weekly)
	require.Equal(t, weekly, monthly)
	assertBillingIntentMoney(t, c, 98.75, 1)
}

func TestBatchImageBalance_DeletedOwnerCanCaptureOrRelease(t *testing.T) {
	for _, capture := range []bool{true, false} {
		t.Run(map[bool]string{true: "capture", false: "release"}[capture], func(t *testing.T) {
			r, c := newBillingIntentFixture(t)
			ctx := context.Background()
			batch := uuid.NewString()
			hold := &service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageHoldRequestID(batch), BatchID: batch, APIKeyID: c.APIKeyID, UserID: c.UserID, HoldAmount: 2}
			_, err := r.ReserveBatchImageBalance(ctx, hold)
			require.NoError(t, err)
			_, err = integrationDB.Exec("UPDATE users SET deleted_at = NOW() WHERE id = $1", c.UserID)
			require.NoError(t, err)
			settle := &service.BatchImageBalanceHoldCommand{RequestID: uuid.NewString(), BatchID: batch, APIKeyID: c.APIKeyID, UserID: c.UserID, HoldAmount: 2, ActualAmount: 1.25}
			if capture {
				_, err = r.CaptureBatchImageBalance(ctx, settle)
			} else {
				_, err = r.ReleaseBatchImageBalance(ctx, settle)
			}
			require.NoError(t, err)
			var balance, frozen float64
			require.NoError(t, integrationDB.QueryRow("SELECT balance, frozen_balance FROM users WHERE id = $1", c.UserID).Scan(&balance, &frozen))
			require.Zero(t, frozen)
			if capture {
				require.Equal(t, 98.75, balance)
			} else {
				require.Equal(t, 100.0, balance)
			}
		})
	}
}

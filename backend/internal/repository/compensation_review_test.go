//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCompensationReviewIntentRollbackKeepsClaim(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	store := &resilienceStore{rdb: client}
	ctx := context.Background()
	require.NoError(t, store.FreezeCompensationAction(ctx, "review", service.OpenAICompensationAction{Kind: "migrate", From: 1, To: 3, At: time.Now()}))
	repo := &usageBillingRepository{db: db}
	cmd := &service.UsageBillingCommand{RequestID: "review-bill", UserID: 1, APIKeyID: 2, AccountID: 3, BalanceCost: 10, Usage: &service.UsageLog{RequestID: "review-bill", UserID: 1, APIKeyID: 2, AccountID: 3, ActualCost: 10, CreatedAt: time.Now()}}
	cmd.Normalize()
	takes := 0
	cmd.ResolveCompensation = func(c *service.UsageBillingCommand) {
		takes++
		a, e := store.TakeCompensationAction(ctx, "review", "review-bill", 3)
		require.NoError(t, e)
		if a != nil && a.Kind == "migrate" {
			c.BalanceCost = 7.5
			c.Usage.ActualCost = 7.5
		}
	}
	frozen := *cmd
	frozen.Usage = service.BillingUsageSnapshot(cmd.Usage)
	frozen.BalanceCost = 7.5
	frozen.Usage.ActualCost = 7.5
	raw, err := json.Marshal(&frozen)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL xy2api.billing_worker_protocol").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO usage_billing_intents").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_intents SET command").WithArgs(cmd.RequestID, cmd.APIKeyID, compensationPayload{t, 7.5}).WillReturnError(errors.New("injected intent update failure"))
	mock.ExpectRollback()
	_, err = repo.prepareBillingIntent(ctx, cmd)
	require.EqualError(t, err, "injected intent update failure")
	_, pending := compensationRedisKeys("review")
	require.False(t, mr.Exists(pending))
	require.NoError(t, store.FreezeCompensationAction(ctx, "review", service.OpenAICompensationAction{RequestID: "later", Kind: "takeover", From: 3, To: 4, At: time.Now()}))
	shared, err := client.Get(ctx, pending).Result()
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL xy2api.billing_worker_protocol").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO usage_billing_intents").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_billing_intents SET command").WithArgs(cmd.RequestID, cmd.APIKeyID, compensationPayload{t, 7.5}).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT request_fingerprint, command FROM usage_billing_intents").WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint", "command"}).AddRow(cmd.RequestFingerprint, raw))
	mock.ExpectCommit()
	got, err := repo.prepareBillingIntent(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, 7.5, got.BalanceCost)
	require.Equal(t, 2, takes)
	require.NoError(t, mock.ExpectationsWereMet())
	remaining, err := client.Get(ctx, pending).Result()
	require.NoError(t, err)
	require.Equal(t, shared, remaining)
	t.Logf("first frozen charge=7.5; SQL rollback; retry frozen charge=%g; Redis takes=%d", got.BalanceCost, takes)
}
func TestCompensationReviewForeignBillPreservesSwitch(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()
	store := &resilienceStore{rdb: client}
	ctx := context.Background()
	require.NoError(t, store.FreezeCompensationAction(ctx, "review", service.OpenAICompensationAction{RequestID: "switch-owner", Kind: "request_switch", From: 1, To: 2, At: time.Now()}))
	foreign, err := store.TakeCompensationAction(ctx, "review", "concurrent-bill", 1)
	require.NoError(t, err)
	require.Nil(t, foreign)
	own, err := store.TakeCompensationAction(ctx, "review", "switch-owner", 2)
	require.NoError(t, err)
	require.NotNil(t, own)
	t.Logf("foreign action=nil; rightful request action=%s", own.RequestID)
}

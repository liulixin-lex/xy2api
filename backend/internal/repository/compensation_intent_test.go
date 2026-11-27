//go:build unit

package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type compensationPayload struct {
	t    *testing.T
	cost float64
}

func (p compensationPayload) Match(v driver.Value) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	var cmd service.UsageBillingCommand
	if json.Unmarshal([]byte(s), &cmd) != nil {
		return false
	}
	return cmd.Usage != nil && cmd.BalanceCost == p.cost && cmd.Usage.ActualCost == p.cost && cmd.ResolveCompensation == nil
}

func TestCompensationIntentFreezesBeforeMoneyAndReplaySkipsRedis(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &usageBillingRepository{db: db}
	cmd := &service.UsageBillingCommand{RequestID: "fixture", UserID: 1, APIKeyID: 2, AccountID: 3, BalanceCost: 10, Usage: &service.UsageLog{RequestID: "fixture", UserID: 1, APIKeyID: 2, AccountID: 3, ActualCost: 10, CreatedAt: time.Now()}}
	cmd.Normalize()
	calls := 0
	cmd.ResolveCompensation = func(c *service.UsageBillingCommand) {
		calls++
		c.BalanceCost = 7.5
		c.Usage.ActualCost = 7.5
		c.Usage.MeteringAdjustment = json.RawMessage(`{"adjustments":[{"kind":"openai_voluntary_compensation","amount":2.5}]}`)
	}
	frozen := *cmd
	frozen.Usage = service.BillingUsageSnapshot(cmd.Usage)
	cmd.ResolveCompensation(&frozen)
	calls = 0
	payload, err := json.Marshal(&frozen)
	require.NoError(t, err)
	for _, inserted := range []int64{1, 0, 0} {
		mock.ExpectBegin()
		mock.ExpectExec("SET LOCAL xy2api.billing_worker_protocol").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("INSERT INTO usage_billing_intents").WillReturnResult(sqlmock.NewResult(0, inserted))
		if inserted == 1 {
			mock.ExpectExec("UPDATE usage_billing_intents SET command").WithArgs(cmd.RequestID, cmd.APIKeyID, compensationPayload{t, 7.5}).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectQuery("SELECT request_fingerprint, command FROM usage_billing_intents").WithArgs(cmd.RequestID, cmd.APIKeyID).WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint", "command"}).AddRow(cmd.RequestFingerprint, payload))
		mock.ExpectCommit()
		got, e := repo.prepareBillingIntent(context.Background(), cmd)
		require.NoError(t, e)
		require.Equal(t, 7.5, got.BalanceCost)
		require.Equal(t, 7.5, got.Usage.ActualCost)
		require.Nil(t, got.ResolveCompensation)
		require.Equal(t, cmd.RequestFingerprint, got.RequestFingerprint)
	}
	require.Equal(t, 1, calls)
	require.Equal(t, 10.0, cmd.Usage.ActualCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

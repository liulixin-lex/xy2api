//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayRequestOneRowOwnershipPostgres(t *testing.T) {
	for _, mode := range []string{"billed", "unbilled", "billing_failure"} {
		t.Run(mode, func(t *testing.T) {
			billing, cmd := newBillingIntentFixture(t)
			ctx := context.Background()
			repo := newUsageLogRepositoryWithSQL(testEntClient(t), integrationDB)
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.NoteGatewaySend(cmd.AccountID)
			st.CompleteGatewayForward(mode != "unbilled", false)
			var gatewayID string
			if mode != "unbilled" {
				meta, write := st.ClaimGatewayRequest(true)
				require.False(t, write)
				gatewayID = meta.RequestID
				cmd.Usage.GatewayRequestID, cmd.Usage.Result = &meta.RequestID, &meta.Result
				if mode == "billing_failure" {
					cmd.Usage.Model = strings.Repeat("x", 101)
				}
				_, err := billing.Apply(ctx, cmd)
				if mode == "billing_failure" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}
			meta, write := st.ClaimGatewayRequest(false)
			gatewayID = meta.RequestID
			if write {
				row := &service.GatewayUnbilledRequest{RequestID: meta.RequestID, CreatedAt: meta.StartedAt, UserID: cmd.UserID, APIKeyID: cmd.APIKeyID, Result: meta.Result}
				require.NoError(t, repo.CreateUnbilledRequest(ctx, row))
				require.NoError(t, repo.CreateUnbilledRequest(ctx, row), "duplicate delivery remains idempotent")
				t.Cleanup(func() {
					_, _ = integrationDB.Exec("DELETE FROM gateway_unbilled_requests WHERE request_id=$1", gatewayID)
				})
			}
			_, write = st.ClaimGatewayRequest(false)
			require.False(t, write)
			var bills, unbilled int
			require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM usage_logs WHERE gateway_request_id=$1", gatewayID).Scan(&bills))
			require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM gateway_unbilled_requests WHERE request_id=$1", gatewayID).Scan(&unbilled))
			switch mode {
			case "billed":
				require.Equal(t, 1, bills)
				require.Zero(t, unbilled)
				assertBillingIntentMoney(t, cmd, 98.75, 1)
			case "unbilled":
				require.Zero(t, bills)
				require.Equal(t, 1, unbilled)
				assertBillingIntentMoney(t, cmd, 100, 0)
			default:
				require.Zero(t, bills)
				require.Zero(t, unbilled)
				assertBillingIntentMoney(t, cmd, 100, 0)
			}
		})
	}
}

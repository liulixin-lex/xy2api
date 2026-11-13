//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAISelectionSessionTakeoverLogging(t *testing.T) {
	for _, kind := range []string{"takeover", "migrate"} {
		for _, outcome := range []string{"success", "conflict", "error"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				output := captureSchedObsLogs(t)
				f := selectionNewSessionFixture()
				if kind == "migrate" {
					f.state(f.owner.ID, schedquality.StateView{State: schedquality.StateCooling})
				}
				f.binding.casFail = outcome == "conflict"
				f.binding.casError = outcome == "error"
				state := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.WithValue(context.Background(), ctxkey.RequestID, "client-chosen"), state)
				result := f.selectNow(ctx)
				if outcome != "success" {
					require.NotNil(t, result)
					result.ReleaseFunc()
					require.Equal(t, int64(10), f.binding.owner)
					for _, row := range schedObsLogRecords(t, output) {
						require.NotEqual(t, "sched_quality.takeover", row["msg"])
					}
					require.Zero(t, f.slots.held)
					return
				}
				require.NotNil(t, result)
				defer result.ReleaseFunc()
				require.Equal(t, []map[string]any{{
					"level": "INFO", "msg": "sched_quality.takeover", "component": "sched_quality",
					"kind": kind, "gateway_request_id": state.GatewayRequestID(), "group_id": float64(7),
					"model": "m", "from_account_id": float64(10), "to_account_id": float64(1),
				}}, schedObsLogRecords(t, output))
				require.Equal(t, result.Account.ID, f.binding.owner)
			})
		}
	}
}

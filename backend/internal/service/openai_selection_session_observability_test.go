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
					f.state(f.owner.ID, schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}})
				}
				f.binding.casFail = outcome == "conflict"
				f.binding.casError = outcome == "error"
				state := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.WithValue(context.Background(), ctxkey.RequestID, "client-chosen"), state)
				result := f.selectNow(ctx)
				require.NotNil(t, result)
				defer result.ReleaseFunc()
				var events []map[string]any
				for _, row := range schedObsLogRecords(t, output) {
					if row["msg"] == "sched_quality.session_switch" {
						events = append(events, row)
					}
				}
				reason := "priority_takeover"
				if kind == "migrate" {
					reason = "quality_migration_error"
				}
				require.Equal(t, []map[string]any{{
					"level": "INFO", "msg": "sched_quality.session_switch", "component": "sched_quality",
					"kind": kind, "reason": reason, "committed": outcome == "success",
					"gateway_request_id": state.GatewayRequestID(), "group_id": float64(7),
					"model": "m", "from": float64(10), "to": float64(1),
				}}, events)
				if outcome != "success" {
					require.Equal(t, int64(10), f.binding.owner)
					return
				}

				require.Equal(t, result.Account.ID, f.binding.owner)
			})
		}
	}
}

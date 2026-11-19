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
				fromModel, toModel := "m", "m"
				if kind == "takeover" {
					fromModel, toModel = "owner-model", "next-model"
					f.owner.Credentials = map[string]any{"model_mapping": map[string]any{"m": fromModel}}
					f.candidates[0].Credentials = map[string]any{"model_mapping": map[string]any{"m": toModel}}
				}
				if kind == "migrate" {
					f.state(f.owner.ID, schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}})
				}
				f.binding.casFail = outcome == "conflict"
				f.binding.casError = outcome == "error"
				state := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.WithValue(context.Background(), ctxkey.RequestID, "client-chosen"), state)
				result := f.selectNow(ctx)
				require.NotNil(t, result)
				defer func() { result.ReleaseFunc(); require.Zero(t, f.slots.held) }()
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
				want := map[string]any{
					"level": "INFO", "msg": "sched_quality.session_switch", "component": "sched_quality",
					"kind": kind, "reason": reason, "committed": outcome == "success",
					"gateway_request_id": state.GatewayRequestID(), "group_id": float64(7),
					"model": "m", "from_model": fromModel, "to_model": toModel, "from": float64(10), "to": float64(1),
					"commit_attempted": true, "attempt_seq": nil, "from_priority": float64(5), "to_priority": float64(1),
					"expected_gen": "g1", "observed_gen": "g1", "new_gen": nil, "commit_result": "binding_changed",
				}
				switch outcome {
				case "success":
					want["commit_result"], want["new_gen"] = "committed", "g1-next"
				case "error":
					want["commit_result"], want["observed_gen"] = "error", nil
				}
				if kind == "takeover" {
					want["takeover_idle_setting"], want["takeover_idle_observed"] = float64(0), nil
					want["avoid_seen_at_select"], want["session_on"] = false, resilience.Current().SessionOn()
					want["transfer_allowed"], want["quality_owned"] = true, nil
				}
				require.Equal(t, []map[string]any{want}, events)
				if outcome != "success" {
					require.Equal(t, int64(10), f.binding.owner)
					return
				}

				require.Equal(t, result.Account.ID, f.binding.owner)
			})
		}
	}
}

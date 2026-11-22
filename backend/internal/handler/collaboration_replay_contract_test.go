//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// These are independent coverage sentinels. A trivially successful fake or a
// missing ingress budget must not produce a plausible replacement baseline.
func TestCollaborationReplayCoverage(t *testing.T) {
	raw, err := readReplayGolden()
	require.NoError(t, err)
	var goldens []replayGolden
	require.NoError(t, json.Unmarshal(raw, &goldens))
	index := map[string]replayGolden{}
	for _, g := range goldens {
		require.NotContains(t, index, g.Case)
		index[g.Case] = g
		for _, turn := range g.Turns {
			require.LessOrEqual(t, turn.TerminalCount, 1)
			require.LessOrEqual(t, len(turn.UsageAccounts), 1)
		}
	}
	require.Len(t, index, len(collaborationReplayCorpus()))
	require.NoError(t, checkHandledReplay(goldens))
	get := func(name string) replayGolden { c, ok := index[name]; require.True(t, ok, name); return c }
	for _, pass := range []string{"false", "true"} {
		for _, stream := range []string{"false", "true"} {
			suffix := "/pass_" + pass + "/stream_" + stream
			for _, limit := range []string{"4", "0"} {
				chain := get("long_repair_chain" + suffix + "/attempts_" + limit).Turns[0]
				require.True(t, chain.Success)
				require.Len(t, chain.Sends, 7)
				var kinds []string
				for _, send := range chain.Sends {
					kinds = append(kinds, send.Rule)
				}
				require.Equal(t, []string{"tool_output", "field", "field", "web_search", "named", "named", "success"}, kinds)
			}
			limited := get("long_repair_chain" + suffix + "/attempts_1").Turns[0]
			require.False(t, limited.Success)
			require.Len(t, limited.Sends, 1)
			require.Equal(t, "upstream_standalone_tool_output_unsupported", limited.Code)
			task := get("single_attempt_task" + suffix + "/attempts_1").Turns[0]
			require.True(t, task.Success)
			require.Len(t, task.Sends, 2)
			reasoning := get("reasoning_only_unreadable" + suffix + "/attempts_1").Turns[0]
			require.False(t, reasoning.Success)
			require.Len(t, reasoning.Sends, 1)
			loop := get("next_tool_loop" + suffix + "/attempts_4")
			require.Len(t, loop.Turns[1].Sends, 1)
			require.NotEmpty(t, loop.Turns[0].Sends[1].Learning)
			require.Equal(t, loop.Turns[0].Learning, loop.Turns[1].Sends[0].Learning)
			child := get("child_after_second_recovery" + suffix + "/attempts_4")
			require.True(t, child.Turns[0].Success)
			require.True(t, child.Turns[1].Success)
			require.Equal(t, 0, child.Turns[1].SendsPerAccount["first"])
			for _, name := range []string{"target_slot_unavailable", "first_account_waiters", "target_executable_then_failure", "target_created_then_stall", "little_time_left", "shared_quota_extra_send"} {
				turn := get(name + suffix + "/attempts_4").Turns[0]
				require.True(t, turn.Success)
				require.Equal(t, 0, turn.SendsPerAccount["second"])
			}
			for _, name := range []string{"queued_user_retryable", "queued_user_nonretryable"} {
				turn := get(name + suffix + "/attempts_4").Turns[0]
				require.False(t, turn.Success)
				require.True(t, turn.UserWaiters)
				require.Zero(t, turn.TerminalMillis)
				require.False(t, turn.HeadersBeforeTerminal)
			}
		}
	}
}

func TestCollaborationReplayRecordFaultScripts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		slow := &replayRecordStore{mode: "slow"}
		start := time.Now()
		require.ErrorIs(t, slow.Operation(ctx), context.DeadlineExceeded)
		require.Equal(t, time.Second, time.Since(start))
		require.Equal(t, 1, slow.count())
		require.Error(t, (&replayRecordStore{mode: "failure"}).Operation(context.Background()))
		require.Panics(t, func() { _ = (&replayRecordStore{mode: "panic"}).Operation(context.Background()) })
	})
}

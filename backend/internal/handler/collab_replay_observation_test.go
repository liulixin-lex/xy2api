//go:build unit

package handler

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabReplayHandledItemEndpoints(t *testing.T) {
	for _, protected := range []bool{false, true} {
		target, name := "rs_alpha", "handled_reasoning_named_again/synthetic"
		if protected {
			target, name = "amsg_task", "handled_task_named_again/synthetic"
		}
		original := replaySend{Account: "first", Rule: "named_always", Items: map[string]string{target: "original", "independent": "kept"}}
		repaired := replaySend{Account: "first", Rule: "named_always", Optional: true, Action: resilience.CollabActionNamedRepair, RejectedItem: target, Items: map[string]string{target: "placeholder", "independent": "kept"}}
		final := replaySend{Account: "first", Rule: "named_always", Optional: true, Action: resilience.CollabActionReplaceAll, RejectedItem: target, Items: map[string]string{target: "placeholder", "independent": "replaced"}}
		check := func(sends ...replaySend) error {
			return checkHandledReplay([]replayGolden{{Case: name, Turns: []replayOutcome{{Sends: sends}}}})
		}
		require.NoError(t, check(original, repaired))
		switched := repaired
		switched.Account = "second"
		switched.Action = resilience.CollabActionSwitchOriginal
		require.Error(t, check(original, repaired, switched), "a switch cannot escape a handled item endpoint")
		if protected {
			require.NoError(t, check(original, repaired, final))
			require.Error(t, check(original, repaired, final, final), "second replace-all after handled protected item")
			require.Error(t, check(original, repaired, repaired), "non-replace-all after handled protected item")
		} else {
			require.Error(t, check(original, repaired, repaired), "send after re-named handled reasoning")
			require.Error(t, check(original, repaired, final), "final disposal after re-named handled reasoning")
		}
	}
}

func TestCollabReplayDeliveredContent(t *testing.T) {
	sends := []replaySend{{Survived: []string{"task", "reasoning"}}, {Optional: true, Survived: []string{"reasoning"}}}
	require.Equal(t, sends[0].Survived, replayDeliveredContent(sends, -1), "restored error retains only the saved path observation")
	require.Equal(t, sends[1].Survived, replayDeliveredContent(sends, 1))
	require.Empty(t, replayDeliveredContent(sends[1:], -1), "discarded optional send has no surviving content")
	turn := replayOutcome{Sends: sends, AcceptedMillis: -1, Survived: sends[1].Survived}
	require.Error(t, checkReplayDelivery(turn), "discarded send must never supply Survived")
	turn.Survived = sends[0].Survived
	require.NoError(t, checkReplayDelivery(turn))
}

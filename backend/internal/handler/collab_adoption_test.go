//go:build unit

package handler

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabSavedEffectsRemainAfterDeliveredRecovery(t *testing.T) {
	quietReplayLogs(t)
	configureReplayTakeover = func(h *OpenAIGatewayHandler) { h.collabRescue = h.rescueCollab }
	defer func() { configureReplayTakeover = nil }()
	snapshot := resilience.Defaults()
	snapshot.Collab.Ladder = true
	raw, err := readReplayGolden()
	require.NoError(t, err)
	var baseline []replayGolden
	require.NoError(t, json.Unmarshal(raw, &baseline))
	for i, c := range collaborationReplayCorpus() {
		if c.Name != "unnamed_task/pass_false/stream_false/attempts_1" {
			continue
		}
		got := runCollaborationReplay(t, c, snapshot).Turns[0]
		want := baseline[i].Turns[0]
		require.True(t, got.Success)
		require.Equal(t, want.TerminalEffects, got.TerminalEffects)
		require.Equal(t, want.TerminalCounters, got.TerminalCounters)
		require.Equal(t, want.Avoidance, got.Avoidance)
		require.Equal(t, want.RateLimited, got.RateLimited)
	}
}

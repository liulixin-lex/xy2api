//go:build unit

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabSavedEffectsRemainAfterDeliveredRecovery(t *testing.T) {
	quietReplayLogs(t)
	replayGroupLookup = true
	t.Cleanup(func() { replayGroupLookup = false })
	snapshot := resilience.Defaults()
	for _, c := range collaborationReplayCorpus() {
		if c.Name != "unnamed_task/pass_false/stream_false/attempts_1" {
			continue
		}
		want := runCollaborationReplay(t, c, snapshot).Turns[0]
		setReplayHandler(t, func(h *OpenAIGatewayHandler, _ *openAIWSUsageHandlerUsageLogRepoStub) {
			h.collabRescue = h.rescueCollab
			h.collabRescuable = collabCanRescue
		})
		snapshot.Collab.Ladder = true
		got := runCollaborationReplay(t, c, snapshot).Turns[0]
		require.True(t, got.Success)
		require.Equal(t, want.TerminalEffects, got.TerminalEffects)
		require.Equal(t, want.TerminalCounters, got.TerminalCounters)
		require.Equal(t, want.Avoidance, got.Avoidance)
		require.Equal(t, want.RateLimited, got.RateLimited)
	}
}

func TestCollabUnmeteredErrorSettlesOnlyDeliveredRescue(t *testing.T) {
	quietReplayLogs(t)
	replayGroupLookup = true
	t.Cleanup(func() { replayGroupLookup = false })
	var rc replayCase
	for _, candidate := range collaborationReplayCorpus() {
		if candidate.Name == "envelope_stream_failure/pass_false/stream_false/attempts_1" {
			rc = candidate
		}
	}
	rc.First = []replayRule{{Kind: "unmetered_error"}}
	rc.Second = rc.First
	run := func(rescue bool) replayOutcome {
		setReplayHandler(t, func(h *OpenAIGatewayHandler, usage *openAIWSUsageHandlerUsageLogRepoStub) {
			usage.delay = 7 * time.Second
			h.collabRescuable = func(*collabSeam) bool { return true }
			h.collabRescue = func(c *gin.Context, s *collabSeam, _ *collabWithheld) {
				if !rescue {
					return
				}
				parent := resilience.StateFrom(c.Request.Context())
				c.Data(200, "application/json", []byte(seamTestJSON))
				require.Positive(t, parent.DeliveredBytes())
				rejections := s.observer.Rejections()
				require.NotEmpty(t, rejections)
				parent.NoteGatewaySend(rejections[0].AccountID)
				s.settle(&service.Account{ID: rejections[0].AccountID, Platform: service.PlatformOpenAI}, &service.OpenAIForwardResult{Model: "gpt-5.5", Usage: service.OpenAIUsage{InputTokens: 1, OutputTokens: 1}}, nil)
			}
		})
		snapshot := resilience.Defaults()
		snapshot.Collab.Ladder = true
		return runCollaborationReplay(t, rc, snapshot).Turns[0]
	}
	failure := run(false)
	require.False(t, failure.Success)
	require.Len(t, failure.UsageAccounts, 1)
	require.Less(t, failure.TerminalMillis, int64(7000))
	success := run(true)
	require.True(t, success.Success)
	require.Len(t, success.UsageAccounts, 1)
	require.Zero(t, success.UnbilledCount)
}

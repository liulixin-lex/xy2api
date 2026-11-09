//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestG2BCompatTimeoutStagedObservation(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, messages := range []bool{false, true} {
		t.Run(fmtBool(messages), func(t *testing.T) {
			logs, restore := captureStructuredLog(t)
			defer restore()
			c, rec := compatStreamContext("/v1/chat/completions", compatChatStreamBody)
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			svc := compatStreamService(&silentUpstream{headers: true, before: g2b355SyntheticFrame}, 1, 0, 0)
			var err error
			if messages {
				_, err = svc.ForwardAsAnthropic(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatMessagesStreamBody), "", "")
			} else {
				_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatChatStreamBody), "", "")
			}
			requireFirstOutputFailover(t, err)
			require.Empty(t, rec.Body.String())
			require.True(t, logs.ContainsMessage("staged=true"))
		})
	}
}

func TestFirstOutputTimeoutStagedObservationDoesNotLeakToNextAttempt(t *testing.T) {
	logs, restore := captureStructuredLog(t)
	defer restore()
	c, _ := compatStreamContext("/v1/responses", "")
	state := resilience.NewRequestState()
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
	started := time.Now()
	state.NotePreludeStaged(started.Add(-time.Second))
	svc := compatStreamService(nil, 1, 0, 0)
	_ = svc.newOpenAIFirstOutputTimeoutError(c.Request.Context(), c, rawChatCompletionsTestAccount(), nil, "", started,
		"model", "", time.Second, "response_headers", nil)
	require.True(t, logs.ContainsMessage("phase=response_headers staged=false"))
	require.False(t, logs.ContainsMessage("staged=true"))
}

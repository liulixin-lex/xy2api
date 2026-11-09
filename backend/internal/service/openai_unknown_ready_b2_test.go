//go:build unit

package service

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Shape from the recorded 355 event_keys; values are synthetic.
const g2b355SyntheticFrame = "event: sub2api.gateway_ready\ndata: {\"type\":\"sub2api.gateway_ready\",\"source\":\"synthetic-gateway\",\"model_output\":false}\n\n"

func TestG2BB2GatewayReadyAttemptBoundary(t *testing.T) {
	withResilienceSettings(t, nil)
	logs, restoreLogs := captureStructuredLog(t)
	defer restoreLogs()
	for _, path := range []struct {
		name        string
		passthrough bool
	}{{"native", false}, {"passthrough", true}} {
		t.Run(path.name, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/responses", `{}`)
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			account := rawChatCompletionsTestAccount()
			start := time.Now()
			state.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: start})
			reader, writer := io.Pipe()
			// Bound the negative control even if an old classifier stops the timer.
			closeTimer := time.AfterFunc(1500*time.Millisecond, func() { _ = writer.Close() })
			t.Cleanup(func() {
				closeTimer.Stop()
				_ = reader.Close()
				_ = writer.Close()
			})
			done := make(chan error, 1)
			go func() {
				_, err := runBlankPlaceholderAttempt(compatStreamService(nil, 1, 0, 0), c, account, path.passthrough, reader, start)
				done <- err
			}()
			_, err := io.WriteString(writer, g2b355SyntheticFrame)
			require.NoError(t, err)
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("ready-only attempt did not terminate")
			}
			var failover *UpstreamFailoverError
			t.Logf("ready-only: failover=%t bytes=%d delivered=%d first_output_zero=%t semantic_zero=%t release=%q", errors.As(err, &failover), rec.Body.Len(), state.DeliveredBytes(), state.FirstOutput().IsZero(), state.SemanticOutputAt().IsZero(), state.PreludeTrace().ReleaseEvent)
			requireFirstOutputFailover(t, err)
			require.Empty(t, rec.Body.String())
			require.Zero(t, state.DeliveredBytes())
			require.True(t, state.FirstOutput().IsZero())
			require.True(t, state.SemanticOutputAt().IsZero())
			require.Equal(t, start, state.PreludeTrace().StagedAt)
			require.Empty(t, state.PreludeTrace().ReleaseEvent)

			account.ID++
			recovered := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic-recovery\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"synthetic-recovery\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
			_, err = runBlankPlaceholderAttempt(compatStreamService(nil, 0, 0, 0), c, account, path.passthrough, io.NopCloser(strings.NewReader(recovered)), time.Now())
			require.NoError(t, err)
			require.Equal(t, recovered, rec.Body.String())
			require.NotContains(t, rec.Body.String(), "synthetic-gateway")
			require.Positive(t, state.DeliveredBytes())
			require.False(t, state.FirstOutput().IsZero())
			require.False(t, state.SemanticOutputAt().IsZero())
			t.Log("recovery: success=true failed_ready_leaked=false real_output=true")
		})
	}
	require.True(t, logs.ContainsMessageAtLevel("staged=true", "info"))
}

func TestG2BB2ProtocolStringsRemainVisible(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct{ name, payload string }{
		{"response unknown string", `{"type":"response.future","source":"synthetic"}`},
		{"codex unknown string", `{"type":"codex.future","source":"synthetic"}`},
		{"response unknown nested text", `{"type":"response.future","extra":{"text":"synthetic"}}`},
		{"codex unknown whitespace", `{"type":"codex.future","text":" "}`},
		{"unknown ciphertext", `{"type":"response.future","encrypted_content":"synthetic-cipher"}`},
		{"unknown tool identity", `{"type":"codex.future","item":{"type":"function_call","name":"synthetic-tool"}}`},
		{"known ciphertext", `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"synthetic-cipher"}}`},
		{"known tool", `{"type":"response.output_item.added","item":{"type":"web_search_call","id":"synthetic-tool"}}`},
		{"known real text", `{"type":"response.output_text.delta","delta":"synthetic-text"}`},
		{"known tool progress", `{"type":"response.web_search_call.in_progress"}`},
	} {
		for _, passthrough := range []bool{false, true} {
			t.Run(tc.name+"/"+fmtBool(passthrough), func(t *testing.T) {
				c, rec := compatStreamContext("/v1/responses", `{}`)
				frame := "data: " + tc.payload + "\n\n"
				failed := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"synthetic-busy\"}}}\n\n"
				_, err := runBlankPlaceholderAttempt(compatStreamService(nil, 0, 0, 0), c, rawChatCompletionsTestAccount(), passthrough, io.NopCloser(strings.NewReader(frame+failed)), time.Now())
				var failover *UpstreamFailoverError
				t.Logf("visible: failover=%t frame_delivered=%t", errors.As(err, &failover), strings.HasPrefix(rec.Body.String(), frame))
				require.Error(t, err)
				require.False(t, errors.As(err, &failover))
				require.True(t, strings.HasPrefix(rec.Body.String(), frame), rec.Body.String())
			})
		}
	}
}

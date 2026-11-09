//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const unknownReadyFrame = "event: sub2api.gateway_ready\ndata: {\"type\":\"sub2api.gateway_ready\",\"source\":\"relay\",\"model_output\":false}\n\n"

func unknownReadySuccessStream() string {
	return unknownReadyFrame +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ready\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
}

func TestResponsesUnknownReadyStallFailsOverBeforeOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmtBool(passthrough), func(t *testing.T) {
			c, recorder := compatStreamContext("/v1/responses", `{}`)
			svc := compatStreamService(nil, 1, 0, 0)
			account := rawChatCompletionsTestAccount()
			reader, writer := io.Pipe()
			closeAfterQuietWindow := time.AfterFunc(1500*time.Millisecond, func() { _ = writer.Close() })
			t.Cleanup(func() {
				closeAfterQuietWindow.Stop()
				_ = reader.Close()
				_ = writer.Close()
			})
			done := make(chan error, 1)
			go func() {
				_, err := runBlankPlaceholderAttempt(svc, c, account, passthrough, reader, time.Now())
				done <- err
			}()
			_, err := io.WriteString(writer, unknownReadyFrame)
			require.NoError(t, err)

			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("the event-only attempt did not reach its first-output deadline")
			}
			requireFirstOutputFailover(t, err)
			require.Empty(t, recorder.Body.String(), "the failed attempt's ready event must stay staged")

			account.ID++
			_, err = runBlankPlaceholderAttempt(svc, c, account, passthrough, io.NopCloser(strings.NewReader(unknownReadySuccessStream())), time.Now())
			require.NoError(t, err)
			body := recorder.Body.String()
			require.Less(t, strings.Index(body, "sub2api.gateway_ready"), strings.Index(body, "response.output_text.delta"))
			require.Less(t, strings.Index(body, "response.output_text.delta"), strings.Index(body, "response.completed"))
			require.Equal(t, 1, strings.Count(body, "event: sub2api.gateway_ready"))
		})
	}
}

func TestResponsesUnknownReadySuccessPreservesFrameOrder(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmtBool(passthrough), func(t *testing.T) {
			c, recorder := compatStreamContext("/v1/responses", `{}`)
			_, err := runBlankPlaceholderAttempt(compatStreamService(nil, 0, 0, 0), c,
				rawChatCompletionsTestAccount(), passthrough,
				io.NopCloser(strings.NewReader(unknownReadySuccessStream())), time.Now())
			require.NoError(t, err)
			require.Equal(t, unknownReadySuccessStream(), recorder.Body.String())
		})
	}
}

func TestResponsesUnknownReadyLongTextStaysStagedAndPreservesBytes(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := nativeTurnStateService(0, 0)
	c, w := nativeTurnStateContext()
	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
	attempt := runNativeTurnState(t, svc, c, resp, 0)
	longFrame := "event: relay.extension\ndata: {\"type\":\"relay.extension\",\"text\":\"" + strings.Repeat("x", 96*1024) + "\"}\n\n"
	_, err := io.WriteString(writer, longFrame)
	require.NoError(t, err)
	time.Sleep(75 * time.Millisecond)
	require.Empty(t, w.Body(), "unknown event bytes remain staged despite non-empty, large text")

	semanticAndTerminal := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ready\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	_, err = io.WriteString(writer, semanticAndTerminal)
	require.NoError(t, err)
	_ = writer.Close()
	attempt.await(t)
	require.NoError(t, attempt.err)
	body := w.Body()
	require.Less(t, strings.Index(body, longFrame), strings.Index(body, "event: response.output_text.delta"))
	require.Contains(t, body, "event: response.completed")
}

func TestResponsesUnknownReadyKeepsRequestHeartbeatWhileStaged(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := gwContext("/v1/responses", w)
	c.Request.Header.Set("User-Agent", "codex_cli_rs")
	c.Request.Header.Set("originator", "codex_cli_rs")
	state := resilience.NewRequestState()
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
	InstallDeliveryTracking(c, time.Now())
	before := keepaliveCommittedForResponses()
	stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, time.Hour, 20*time.Millisecond)
	defer stop()

	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	done := make(chan error, 1)
	go func() {
		_, err := runBlankPlaceholderAttempt(compatStreamService(nil, 0, 0, 0), c,
			rawChatCompletionsTestAccount(), false, reader, time.Now())
		done <- err
	}()
	require.Eventually(t, func() bool {
		k := keepaliveFrom(c)
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.stopped
	}, time.Second, 5*time.Millisecond)
	// Handler header setup temporarily suspends a pre-armed request heartbeat;
	// resume the same request-level keepalive once the SSE response is staged.
	StartPreOutputKeepalive(c, PreOutputProtocolResponses, 10*time.Millisecond, 20*time.Millisecond)
	_, err := io.WriteString(writer, unknownReadyFrame)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return PreOutputKeepaliveCommitted(c) }, time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return strings.Count(w.Body(), "event: ping") >= 2 }, time.Second, 5*time.Millisecond)
	require.NotContains(t, w.Body(), "sub2api.gateway_ready")
	require.Greater(t, keepaliveCommittedForResponses(), before)

	_ = writer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("staged stream did not stop after upstream close")
	}
}

func keepaliveCommittedForResponses() int64 {
	var count int64
	for _, row := range resilience.Snapshot(time.Hour) {
		if row.Name == resilience.MetricKeepaliveCommitted && row.Label == PreOutputProtocolResponses {
			count += row.Count
		}
	}
	return count
}

func TestResponsesUnknownReadyStageLimitMatchesExistingBehavior(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := nativeTurnStateService(0, 30)
	c, w := nativeTurnStateContext()
	const frameSize = 1024 * 1024
	frame := `data: {"type":"relay.ready","text":"` + strings.Repeat("x", frameSize-45) + `"}`
	body := strings.Repeat(frame+"\n\n", 9)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "model", "model")
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.SafeToFailoverAfterWrite)
	require.Contains(t, string(failover.ResponseBody), "staging limit exceeded")
	require.Empty(t, w.Body())
}

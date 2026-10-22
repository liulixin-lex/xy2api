//go:build unit

package service

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// PM instruction 5: the native (non-passthrough) OpenAI Responses path had
// the same 0.4.4 defect as DELTA-P1-2. A response.failed that matches an
// error passthrough rule after the stream's heartbeats committed 200 must
// end with response.created then response.failed carrying the rule's
// message, never a bare JSON body; before any commit the rule keeps its
// status code and JSON.

func runNativeRuleStream(t *testing.T, keepalive int, beforeFailed func(w *gwWriter)) (*gwWriter, error) {
	t.Helper()
	w := newGWWriter()
	c := ptContext(w)
	bindPassthroughRule(c, "openai", []string{"context_length_exceeded"}, 400)
	up := newGWUpstream(nil)
	done := make(chan error, 1)
	go func() {
		_, err := compatStreamService(nil, 10, 0, keepalive).handleStreamingResponse(c.Request.Context(), up.response(nil), c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
		done <- err
	}()
	up.send(t, ptCreated)
	beforeFailed(w)
	up.send(t, buildContextLengthFailedSSE())
	_ = up.pw.Close()
	select {
	case err := <-done:
		require.True(t, IsResponseCommitted(c))
		return w, err
	case <-time.After(4 * time.Second):
		t.Fatal("native stream did not return")
		return nil, nil
	}
}

func TestNativeResponsesRuleAfterHeartbeatEndsWithResponsesTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	w, err := runNativeRuleStream(t, 1, func(w *gwWriter) {
		require.Eventually(t, func() bool { return strings.HasPrefix(w.Body(), ":") }, 3*time.Second, 10*time.Millisecond)
	})
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	body := w.Body()
	require.Equal(t, http.StatusOK, w.rec.Code)
	require.NotContains(t, body, "\n{", "no bare JSON after the heartbeat")
	require.NotContains(t, body, "resp_pt", "the staged upstream opening is not released")
	var events []string
	for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if !strings.HasPrefix(block, ":") {
			events = append(events, block)
		}
	}
	require.Len(t, events, 2, body)
	require.True(t, strings.HasPrefix(events[0], "event: response.created\ndata: "), body)
	require.True(t, strings.HasPrefix(events[1], "event: response.failed\ndata: "), body)
	created := gjson.Parse(strings.TrimPrefix(events[0], "event: response.created\ndata: "))
	failed := gjson.Parse(strings.TrimPrefix(events[1], "event: response.failed\ndata: "))
	require.Equal(t, int64(0), created.Get("sequence_number").Int())
	require.Equal(t, int64(1), failed.Get("sequence_number").Int())
	require.Equal(t, created.Get("response.id").String(), failed.Get("response.id").String())
	require.Contains(t, failed.Get("response.error.message").String(), "context window", "the rule's message")
	require.False(t, failed.Get("response.error.headers").Exists(), "a 400 rule carries no retry hint")
}

// Counter-example: without a heartbeat the rule answers with its status
// code and JSON body, as before.
func TestNativeResponsesRuleWithoutHeartbeatKeepsJSON(t *testing.T) {
	withResilienceSettings(t, nil)
	w, err := runNativeRuleStream(t, 0, func(*gwWriter) {})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, w.rec.Code)
	require.Contains(t, gjson.Get(w.Body(), "error.message").String(), "context window")
	require.NotContains(t, w.Body(), "event:")
}

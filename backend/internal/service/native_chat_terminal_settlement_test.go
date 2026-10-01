//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type nativeChatReadCounter struct {
	io.ReadCloser
	reads atomic.Int32
}

func (b *nativeChatReadCounter) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.ReadCloser.Read(p)
}

func TestNativeChatTerminalSettlementRealStores(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	ctx := NewControlledRequestContext(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 1, Delivery: true}), "chat")
	group := int64(7)
	r, on, err := s.loadPolicy(ctx, &group, "test-model", "")
	require.NoError(t, err)
	require.True(t, on)
	t.Cleanup(r.Close)
	a := controlledPick(t, s, ctx, r, accounts[:1])
	serverDone := make(chan struct{})
	var stop sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_chat_settlement\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-q.Context().Done():
		case <-serverDone:
		}
	}))
	t.Cleanup(func() { stop.Do(func() { close(serverDone) }); upstream.Close() })
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader("{\"stream\":true}"))
	require.NoError(t, err)
	var counter *nativeChatReadCounter
	resp, err := s.roundTrip(req, a.ID, 10, func(q *http.Request) (*http.Response, error) {
		response, e := upstream.Client().Do(q)
		if e == nil {
			counter = &nativeChatReadCounter{ReadCloser: response.Body}
			response.Body = counter
		}
		return response, e
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	out := newNativeStreamTestRecorder()
	c, _ := gin.CreateTestContext(out)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	result, err := nativeRelayService().handleChatStreamingResponse(resp, c, a, "test-model", "test-model", "test-model", time.Now(), 128)
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Usage.OutputTokens)
	require.Contains(t, out.Body.String(), "OK")
	require.Equal(t, 1, strings.Count(out.Body.String(), "data: [DONE]"))
	// A complete protocol terminal must not start another network read. The
	// upstream deliberately stays open so closing cannot masquerade as EOF.
	time.Sleep(50 * time.Millisecond)
	reads := counter.reads.Load()
	require.NoError(t, resp.Body.Close())
	r.Close()
	var state, outcome string
	require.NoError(t, db.QueryRow("SELECT state,outcome FROM scheduling_attempts WHERE ticket_id=$1", r.currentDispatch.ticket.TicketID).Scan(&state, &outcome))
	t.Logf("client=OK+DONE reads=%d state=%s outcome=%s", reads, state, outcome)
	require.EqualValues(t, 1, reads, "reader continued beyond the complete success terminal")
	require.Equal(t, "settled", state)
	require.Equal(t, "completed", outcome, "local close must not penalize a successful upstream generation")
}

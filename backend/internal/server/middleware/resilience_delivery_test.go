//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func withMiddlewareResilience(t *testing.T, mutate func(*resilience.Settings)) {
	t.Helper()
	orig := *resilience.Current()
	s := resilience.Defaults()
	if mutate != nil {
		mutate(&s)
	}
	resilience.Publish(s)
	t.Cleanup(func() { resilience.Publish(orig) })
}

// disconnectFixture runs a gateway-like route over real connections: the
// handler optionally writes SSE blocks, then waits on a slow upstream
// through a context detached from the client like the forwarders do.
func disconnectFixture(t *testing.T, written string) (states chan *resilience.RequestState, upstreamCancelled chan time.Duration, url string) {
	t.Helper()
	upstreamCancelled = make(chan time.Duration, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		select {
		case <-r.Context().Done():
			upstreamCancelled <- time.Since(start)
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(upstream.Close)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	states = make(chan *resilience.RequestState, 1)
	engine.POST("/v1/responses", ClientRequestID(), func(c *gin.Context) {
		st := resilience.StateFrom(c.Request.Context())
		states <- st
		if written != "" {
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.WriteString(written)
			c.Writer.Flush()
		}
		ctx := resilience.UpstreamContextOnDisconnect(c.Request.Context(), context.WithoutCancel(c.Request.Context()))
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return states, upstreamCancelled, server.URL + "/v1/responses"
}

func clientLeavesAfter(t *testing.T, url string, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		// Streaming: read until the client deadline cuts the connection.
		buf := make([]byte, 256)
		for {
			if _, rerr := resp.Body.Read(buf); rerr != nil {
				break
			}
		}
		_ = resp.Body.Close()
	}
}

func TestUndeliveredDisconnectCancelsUpstream(t *testing.T) {
	// Spec: a CDN cut before any output cancels the upstream at once.
	withMiddlewareResilience(t, nil)
	states, cancelled, url := disconnectFixture(t, "")
	clientLeavesAfter(t, url, 300*time.Millisecond)
	select {
	case waited := <-cancelled:
		require.Less(t, waited, 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("upstream was not cancelled")
	}
	st := <-states
	require.Eventually(t, st.Undelivered, time.Second, 10*time.Millisecond)
}

func TestPartialDisconnectDrainsUpstream(t *testing.T) {
	// Spec: after partial delivery the upstream is read for at most the drain.
	withMiddlewareResilience(t, func(s *resilience.Settings) { s.Disconnect.PartialDrainMaxSeconds = 1 })
	states, cancelled, url := disconnectFixture(t, "event: response.created\ndata: {}\n\n"+
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	clientLeavesAfter(t, url, 300*time.Millisecond)
	select {
	case waited := <-cancelled:
		require.GreaterOrEqual(t, waited, 900*time.Millisecond, "kept reading for the drain")
		require.Less(t, waited, 3*time.Second)
	case <-time.After(6 * time.Second):
		t.Fatal("partial drain not bounded")
	}
	st := <-states
	disconnected, delivered, _ := st.Disconnect()
	require.True(t, disconnected)
	require.True(t, delivered)
}

func TestOpeningEventsOnlyDisconnectCancelsUpstream(t *testing.T) {
	// Review R03: opening events are no output; a client that leaves after
	// them gets the undelivered handling (upstream cancelled at once).
	withMiddlewareResilience(t, nil)
	states, cancelled, url := disconnectFixture(t, "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: ping\ndata: {\"type\": \"ping\"}\n\n")
	clientLeavesAfter(t, url, 300*time.Millisecond)
	select {
	case waited := <-cancelled:
		require.Less(t, waited, 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("upstream was not cancelled")
	}
	st := <-states
	require.Eventually(t, st.Undelivered, time.Second, 10*time.Millisecond)
}

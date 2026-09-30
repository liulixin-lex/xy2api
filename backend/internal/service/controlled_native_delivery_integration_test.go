//go:build unit

package service

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamControlledDeliveryRealStores(t *testing.T) {
	s, _, _, accounts := controlledIntegration(t, false)
	ctx := NewControlledRequestContext(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 1, Delivery: true}), "responses")
	group := int64(7)
	r, on, err := s.loadPolicy(ctx, &group, "test-model", "")
	require.NoError(t, err)
	require.True(t, on)
	r.Profile = scheduling.LatencyProfile{Name: scheduling.AccountPoolProfileName, AttemptTimeoutMS: 30000, TotalBudgetMS: 60000, MinAttemptWindowMS: 50}
	r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, r.Started, r.ClientDeadline)
	t.Cleanup(r.Close)
	a := controlledPick(t, s, ctx, r, accounts)
	started := time.Now()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		wait := func(d time.Duration) bool {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-q.Context().Done():
				return false
			case <-timer.C:
				return true
			}
		}
		if !wait(200 * time.Millisecond) {
			return
		}
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_timeline\"}}\n\n")
		w.(http.Flusher).Flush()
		if !wait(4800 * time.Millisecond) {
			return
		}
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"fixture\"}\n\n")
		w.(http.Flusher).Flush()
		if !wait(15 * time.Second) {
			return
		}
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_timeline\",\"status\":\"completed\"}}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader("{\"model\":\"test-model\",\"stream\":true}"))
	require.NoError(t, err)
	response, err := s.roundTrip(req, a.ID, 10, upstream.Client().Do)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Less(t, time.Since(started), time.Second, "dispatch must return before content appears at five seconds")
	reader := bufio.NewReader(response.Body)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writer := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
	writer.Header().Set("Content-Type", "text/event-stream")
	arrived := make([]time.Duration, 0, 3)
	for i := 0; i < 3; i++ {
		var frame strings.Builder
		var data string
		for {
			line, readErr := reader.ReadString('\n')
			require.NoError(t, readErr)
			frame.WriteString(line)
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
			if line == "\n" {
				break
			}
		}
		readAt := time.Now()
		require.NoError(t, CommitControlledOutput(ctx, []byte(data)))
		_, err = writer.Write([]byte(frame.String()))
		require.NoError(t, err)
		writer.Flush()
		flushedAt := time.Now()
		RecordNativeStreamFlush(ctx, readAt, flushedAt)
		arrived = append(arrived, flushedAt.Sub(started))
		if i == 0 {
			require.True(t, r.Ledger.Snapshot().Committed)
			require.False(t, ControlledStreamSnapshot(ctx).SemanticSeen)
		}
	}
	controlledConsume(t, response)
	require.Len(t, r.history, 1)
	require.Equal(t, "completed", r.history[0].Outcome)
	require.NotNil(t, r.history[0].FirstProtocolEventMS)
	require.NotNil(t, r.history[0].FirstContentMS)
	require.Less(t, *r.history[0].FirstProtocolEventMS, int64(1000))
	require.GreaterOrEqual(t, *r.history[0].FirstContentMS, int64(4500))
	require.Less(t, arrived[0], time.Second)
	require.GreaterOrEqual(t, arrived[1], 4500*time.Millisecond)
	require.Less(t, arrived[1], 7*time.Second)
	require.GreaterOrEqual(t, arrived[2], 19*time.Second)
	require.Less(t, arrived[2], 23*time.Second)
	require.Equal(t, 1, r.Ledger.Snapshot().Attempts)
	require.Equal(t, int64(3), r.flushCount)
	t.Logf("timeline created=%s content=%s completed=%s attempts=%d gateway_first_read_to_flush_ms=%.3f gateway_max_read_to_flush_ms=%.3f", arrived[0], arrived[1], arrived[2], r.Ledger.Snapshot().Attempts, *r.firstReadToFlushMS, r.maxReadToFlushMS)
}

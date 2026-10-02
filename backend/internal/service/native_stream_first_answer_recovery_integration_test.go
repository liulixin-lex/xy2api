//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNativeCommittedPreambleCannotFailoverRealStores(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, mode := range []string{"eof", "failed"} {
			t.Run(fmt.Sprintf("passthrough_%v/%s", passthrough, mode), func(t *testing.T) {
				s, db, _, accounts := controlledIntegration(t, false)
				parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx := NewControlledRequestContext(WithNativeStreamPolicy(parent, NativeStreamPolicy{Version: 1, Delivery: true, Recovery: true}), "responses")
				group := int64(7)
				r, on, err := s.loadPolicy(ctx, &group, "test-model", "")
				require.NoError(t, err)
				require.True(t, on)
				r.Profile = scheduling.LatencyProfile{Name: scheduling.AccountPoolProfileName, AttemptTimeoutMS: 1200, TotalBudgetMS: 4500, MinAttemptWindowMS: 50}
				r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, r.Started, r.ClientDeadline)
				defer r.Close()
				rec := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
				gateway := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize, StreamKeepaliveInterval: 1}}}
				first := controlledPick(t, s, ctx, r, accounts)
				cancelled := make(chan struct{})
				response, err := controlledHTTP(t, s, ctx, first.ID, func(w http.ResponseWriter, q *http.Request) {
					defer close(cancelled)
					_, _ = io.Copy(io.Discard, q.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_failed_candidate\"}}\n\n")
					if mode == "failed" {
						fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_failed_candidate\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream unavailable\"}}}\n\n")
					}
					w.(http.Flusher).Flush()
				})
				require.NoError(t, err)
				started := time.Now()
				forward := func(resp *http.Response, a *Account) (*int, error) {
					if passthrough {
						v, e := gateway.handleStreamingResponsePassthrough(ctx, resp, c, a, started, "test-model", "test-model")
						return v.firstTokenMs, e
					}
					v, e := gateway.handleStreamingResponse(ctx, resp, c, a, started, "test-model", "test-model")
					return v.firstTokenMs, e
				}
				firstToken, err := forward(response, first)
				_ = response.Body.Close()
				body := rec.Body.String()
				t.Logf("FIRST mode=%s passthrough=%v committed=%v bytes=%d error=%v", mode, passthrough, ControlledStreamSnapshot(ctx).AttemptCommitted, len(body), err)
				var failover *UpstreamFailoverError
				require.Error(t, err)
				require.False(t, errors.As(err, &failover), "delivered response identity forbids another generation")
				require.Nil(t, firstToken)
				require.Contains(t, body, "resp_failed_candidate")
				require.True(t, ControlledStreamSnapshot(ctx).AttemptCommitted)
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("upstream did not terminate")
				}
				require.Equal(t, 1, r.Ledger.Snapshot().Attempts)
				var attempts, active int
				require.NoError(t, db.QueryRow("SELECT count(*),count(*) FILTER(WHERE state='dispatched') FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&attempts, &active))
				require.Equal(t, 1, attempts)
				require.Zero(t, active)
				t.Logf("COMMITTED first=%d attempts=%d live_dispatches=%d", first.ID, attempts, active)
			})
		}
	}
}

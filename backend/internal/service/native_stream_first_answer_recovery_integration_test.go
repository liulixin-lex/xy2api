//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNativeFirstAnswerRecoveryRealStores(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, mode := range []string{"created_timeout", "reasoning_timeout", "message_timeout", "comment_timeout", "eof", "failed"} {
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
					if mode == "reasoning_timeout" {
						fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"sequence_number\":1,\"delta\":\"thinking\"}\n\n")
					}
					if mode == "message_timeout" {
						fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"item\":{\"id\":\"msg_failed\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
					}
					if mode == "comment_timeout" {
						fmt.Fprint(w, ": provider-heartbeat\n\n")
					}
					if mode == "failed" {
						fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_failed_candidate\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream unavailable\"}}}\n\n")
					}
					w.(http.Flusher).Flush()
					if strings.HasSuffix(mode, "timeout") {
						select {
						case <-q.Context().Done():
						case <-time.After(2500 * time.Millisecond):
						}
					}
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
				before := rec.Body.String()
				t.Logf("FIRST mode=%s passthrough=%v committed=%v bytes=%d error=%v", mode, passthrough, ControlledStreamSnapshot(ctx).AttemptCommitted, len(before), err)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Nil(t, firstToken)
				require.NotContains(t, before, "resp_failed_candidate")
				require.NotContains(t, before, "thinking")
				require.False(t, ControlledStreamSnapshot(ctx).AttemptCommitted)
				if strings.HasSuffix(mode, "timeout") {
					require.Contains(t, before, ":")
					require.Less(t, time.Since(started), 2*time.Second, "reasoning and message headers must not disable answer timeout")
				}
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("first upstream not cancelled before retry")
				}
				second := controlledPick(t, s, ctx, r, accounts)
				require.NotEqual(t, first.ID, second.ID)
				response, err = controlledHTTP(t, s, ctx, second.ID, func(w http.ResponseWriter, q *http.Request) {
					_, _ = io.Copy(io.Discard, q.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_recovered\"}}\n\n")
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"OK\"}\n\n")
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_recovered\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
				})
				require.NoError(t, err)
				answer, err := forward(response, second)
				_ = response.Body.Close()
				require.NoError(t, err)
				require.NotNil(t, answer)
				require.NotContains(t, rec.Body.String(), "resp_failed_candidate")
				require.Contains(t, rec.Body.String(), "resp_recovered")
				require.Contains(t, rec.Body.String(), `"delta":"OK"`)
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.completed"`))
				require.Equal(t, 2, r.Ledger.Snapshot().Attempts)
				var complete, active, attempts int
				require.NoError(t, db.QueryRow("SELECT count(*),count(*) FILTER(WHERE outcome='completed' AND state='settled'),count(*) FILTER(WHERE state='dispatched') FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&attempts, &complete, &active))
				require.Equal(t, 2, attempts)
				require.Equal(t, 1, complete)
				require.Zero(t, active)
				t.Logf("RECOVERED first=%d second=%d attempts=%d completed=%d live_dispatches=%d answer_ms=%d heartbeat=%v", first.ID, second.ID, attempts, complete, active, *answer, strings.Contains(before, ":"))
			})
		}
	}
}

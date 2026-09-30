package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamLocalTraceHeaderDoesNotCommitHeartbeat(t *testing.T) {
	for _, sub2api := range []bool{false, true} {
		for _, upstreamIdentity := range []bool{false, true} {
			name := "controlled_local_trace"
			if sub2api {
				name = "sub2api_local_trace"
			}
			if upstreamIdentity {
				name += "_upstream_replaced"
			}
			t.Run(name, func(t *testing.T) {
				e := gin.New()
				e.Use(func(c *gin.Context) {
					// Production RequestLogger writes this before scheduling admission.
					c.Header("X-Request-ID", "local_trace_only")
					c.Request = c.Request.WithContext(WithNativeStreamPolicy(c.Request.Context(), NativeStreamPolicy{Version: 1, Delivery: true}))
					c.Next()
				})
				if sub2api {
					e.Use(ControlledSchedulingMiddleware(func(context.Context) (scheduling.ModeSnapshot, error) {
						return scheduling.ModeSnapshot{Mode: scheduling.ModeSub2API}, nil
					}))
				} else {
					e.Use(ControlledSchedulingMiddleware())
				}
				e.GET("/responses", func(c *gin.Context) {
					r := controlledRequest(c.Request.Context())
					r.Ledger = scheduling.NewAttemptLedger(scheduling.RetryPolicy{MaxAttempts: 3, MaxPerAccount: 1, MaxPerTier: 3}, scheduling.LatencyProfile{}, time.Now(), time.Time{})
					require.NoError(t, r.Ledger.BeginAttempt(1, 0, time.Now(), true))
					c.Header("Content-Type", "text/event-stream")
					if upstreamIdentity {
						c.Header("X-Request-ID", "upstream_generation_identity")
					}
					_, err := c.Writer.Write([]byte(": keepalive\n\n"))
					require.NoError(t, err)
					c.Writer.Flush()
					state := ControlledStreamSnapshot(c.Request.Context())
					require.True(t, state.HTTPCommitted)
					require.False(t, state.SemanticSeen)
					require.Equal(t, upstreamIdentity, state.AttemptCommitted)
					if upstreamIdentity {
						require.ErrorIs(t, r.Ledger.BeginAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
					} else {
						require.NoError(t, r.Ledger.BeginAttempt(2, 0, time.Now(), true))
					}
				})
				e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/responses", nil))
			})
		}
	}
}

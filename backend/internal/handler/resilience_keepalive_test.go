//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func keepaliveCtx(t *testing.T, edge string, kind resilience.RequestKind) *gin.Context {
	t.Helper()
	c, _ := keepaliveCtxWithRecorder(t, edge, kind)
	return c
}

func TestArmPreOutputKeepaliveOAuthHandoff(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(strconv.FormatBool(committed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withHandlerResilience(t, func(s *resilience.Settings) {
					s.Keepalive.DelaySeconds = 5
					s.Keepalive.IntervalSeconds = 5
				})
				c, rec := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
				lockedWriter := &lockedKeepaliveResponseWriter{ResponseWriter: c.Writer, recorder: rec}
				c.Writer = lockedWriter
				c.Request.Header.Set("originator", "codex_cli_rs")
				defer stopPreOutputKeepalive(c)
				armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, &service.Account{Type: service.AccountTypeAPIKey})
				if committed {
					time.Sleep(6 * time.Second)
				} else {
					time.Sleep(time.Second)
				}
				synctest.Wait()
				c.Header("X-Attempt", "one")
				armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, &service.Account{Type: service.AccountTypeOAuth})
				time.Sleep(4 * time.Second)
				synctest.Wait()
				if committed {
					require.Equal(t, 2, strings.Count(lockedWriter.body(), "event: ping\n"))
					time.Sleep(5 * time.Second)
					synctest.Wait()
					require.Equal(t, 3, strings.Count(lockedWriter.body(), "event: ping\n"))
					require.Equal(t, -1, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
				} else {
					time.Sleep(15 * time.Second)
					synctest.Wait()
					require.Empty(t, lockedWriter.body())
					require.False(t, service.PreOutputKeepaliveCommitted(c))
				}
			})
		})
	}
}

type lockedKeepaliveResponseWriter struct {
	gin.ResponseWriter
	mu       sync.Mutex
	recorder *httptest.ResponseRecorder
}

func (w *lockedKeepaliveResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.Write(p)
}

func (w *lockedKeepaliveResponseWriter) WriteString(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.WriteString(s)
}

func (w *lockedKeepaliveResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
}

func (w *lockedKeepaliveResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.Flush()
}

func (w *lockedKeepaliveResponseWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recorder.Body.String()
}

func keepaliveCtxWithRecorder(t *testing.T, edge string, kind resilience.RequestKind) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := newResilienceTestContext(t)
	st := resilience.NewRequestState()
	st.SetEdge(edge)
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), kind, time.Now()))
	c.Request = c.Request.WithContext(ctx)
	return c, w
}

func armed(c *gin.Context) bool {
	_, ok := c.Get(ginKeyPreOutputKeepaliveStop)
	return ok
}

func TestArmPreOutputKeepaliveConditions(t *testing.T) {
	withHandlerResilience(t, nil)
	relay := &service.Account{ID: 1, Type: service.AccountTypeAPIKey}
	oauth := &service.Account{ID: 2, Type: service.AccountTypeOAuth}

	c := keepaliveCtx(t, "cf", resilience.KindText)
	armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, relay)
	require.True(t, armed(c))
	stopPreOutputKeepalive(c)

	for name, c := range map[string]*gin.Context{
		"direct request":      keepaliveCtx(t, "", resilience.KindText),
		"image endpoint":      keepaliveCtx(t, "cf", resilience.KindImage),
		"non-streaming input": nil,
	} {
		if c == nil {
			c = keepaliveCtx(t, "esa", resilience.KindText)
			armPreOutputKeepalive(c, service.PreOutputProtocolResponses, false, relay)
		} else {
			armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, relay)
		}
		require.False(t, armed(c), name)
	}

	// Official OAuth accounts are never committed early (D21).
	c = keepaliveCtx(t, "cf", resilience.KindText)
	armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, oauth)
	require.False(t, armed(c))

	// Switch off.
	withHandlerResilience(t, func(s *resilience.Settings) { s.Keepalive.Enabled = false })
	c = keepaliveCtx(t, "cf", resilience.KindText)
	armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, relay)
	require.False(t, armed(c))
}

func TestHeartbeatThenFailoverKeepsSwitching(t *testing.T) {
	// Spec: after heartbeats the gateway can still switch accounts, and a
	// later failure ends the committed stream with a terminal frame.
	withHandlerResilience(t, nil)
	c, rec := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
	c.Set(ginKeyPreOutputKeepaliveStop, service.StartPreOutputKeepalive(c, service.PreOutputProtocolAnthropic, 0, 5*time.Millisecond))
	defer stopPreOutputKeepalive(c)
	before := -1
	require.Eventually(t, func() bool { return service.PreOutputKeepaliveCommitted(c) }, time.Second, 5*time.Millisecond)
	require.Equal(t, before, service.OpenAICompactKeepaliveAdjustedWrittenSize(c), "heartbeats are not output")

	(&GatewayHandler{}).handleStreamingAwareError(c, 503, "overloaded_error", "Upstream service overloaded", false)
	stopPreOutputKeepalive(c)
	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, "event: ping\n"), body)
	require.True(t, strings.HasSuffix(body, "\n\n"), body)
	last := body[strings.LastIndex(body, "event: "):]
	require.True(t, strings.HasPrefix(last, "event: error\ndata: {\"type\":\"error\""), last)
	require.Contains(t, last, "api_error")
	require.NotContains(t, last, "overloaded_error")
}

func TestArmPreOutputKeepaliveResumesBetweenAPIKeyAttempts(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Keepalive.DelaySeconds = 0
		s.Keepalive.IntervalSeconds = 1
	})
	c, rec := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
	defer stopPreOutputKeepalive(c)
	armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, &service.Account{ID: 1, Type: service.AccountTypeAPIKey})
	require.Eventually(t, func() bool { return service.PreOutputKeepaliveCommitted(c) }, time.Second, time.Millisecond)
	before := c.Writer.Size()
	c.Header("X-Upstream-Request", "attempt-one")
	wrapped := c.Writer
	armPreOutputKeepalive(c, service.PreOutputProtocolResponses, true, &service.Account{ID: 2, Type: service.AccountTypeAPIKey})
	require.Same(t, wrapped, c.Writer)
	require.Eventually(t, func() bool { return c.Writer.Size() > before }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, -1, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	c.JSON(503, gin.H{"error": gin.H{"message": "busy"}})
	stopPreOutputKeepalive(c)
	require.Contains(t, rec.Body.String(), "event: response.failed")
}

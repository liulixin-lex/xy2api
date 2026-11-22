//go:build unit

package handler

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

var errLocalEndingConn = errors.New("synthetic connection failure")

type localEndingListener struct {
	net.Listener
	fail *atomic.Bool
}

func (l localEndingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return localEndingConn{Conn: conn, fail: l.fail}, nil
}

type localEndingConn struct {
	net.Conn
	fail *atomic.Bool
}

func (c localEndingConn) Write(p []byte) (int, error) {
	if c.fail.Load() {
		return 0, errLocalEndingConn
	}
	return c.Conn.Write(p)
}

// The writer has to work through the writers production installs on a real
// server: delivery tracking, ops capture, the seam and the keepalive.
func TestCollabLocalEndingThroughProductionWriters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                               string
		stream, fail, heartbeat, converted bool
	}{
		{name: "stream", stream: true},
		{name: "json"},
		{name: "failed flush", stream: true, fail: true},
		{name: "committed heartbeat", stream: true, heartbeat: true},
		{name: "converted error", stream: true, heartbeat: true, converted: true},
		{name: "failed flush after heartbeat", stream: true, heartbeat: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fail atomic.Bool
			type outcome struct {
				complete, committed, claimed bool
				err                          error
			}
			done := make(chan outcome, 1)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
				service.InstallDeliveryTracking(c, time.Now())
				c.Next()
			}, OpsErrorLoggerMiddleware(nil))
			engine.POST("/v1/responses", func(c *gin.Context) {
				var got outcome
				withSeam(c, func(c *gin.Context, _ *collabSeam, saved *collabWithheld) {
					got.committed = saved.committed
					fail.Store(tc.fail)
					state := service.CollabLocalEndingResponseState{}
					got.complete, got.err = service.WriteCollabLocalEnding(c.Request.Context(), c.Writer, state, "example-model", "notice", tc.stream, func() bool { return service.ClaimProtocolTerminal(c) })
				}, func() {
					// As in production, the keepalive's wrapper is still installed
					// and the error writer holds the terminal when a takeover starts.
					if tc.stream {
						delay := time.Hour
						if tc.heartbeat {
							delay = 0
						}
						c.Set(ginKeyPreOutputKeepaliveStop, service.StartPreOutputKeepalive(c, service.PreOutputProtocolResponses, delay, time.Millisecond))
						for range 1000 {
							if !tc.heartbeat || service.PreOutputKeepaliveCommitted(c) {
								break
							}
							time.Sleep(time.Millisecond)
						}
					}
					if tc.converted {
						c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "saved error"}})
						return
					}
					(&OpenAIGatewayHandler{}).errorResponse(c, http.StatusBadGateway, "upstream_error", "saved error")
					got.claimed = service.ProtocolTerminalClaimed(c)
				})
				done <- got
			})
			server := httptest.NewUnstartedServer(engine)
			server.Listener = localEndingListener{Listener: server.Listener, fail: &fail}
			server.Start()
			defer server.Close()

			resp, err := server.Client().Post(server.URL+"/v1/responses", "application/json", http.NoBody)
			if err == nil {
				defer func() { _ = resp.Body.Close() }()
			}
			got := <-done
			require.Equal(t, tc.heartbeat, got.committed)
			require.Equal(t, !tc.converted, got.claimed, "the saved error has to own the terminal")
			if tc.fail {
				require.False(t, got.complete, "a failed flush is never a complete delivery")
				require.Error(t, got.err)
				return
			}
			require.NoError(t, err)
			require.NoError(t, got.err)
			require.True(t, got.complete)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Regexp(t, `"id":"msg_[0-9a-f]{32}"`, string(body))
			require.NotContains(t, string(body), "saved error")
			require.NotContains(t, string(body), `"error":{`)
			if tc.stream {
				require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
				require.Equal(t, 1, strings.Count(string(body), "event: response.created"))
				require.Equal(t, 1, strings.Count(string(body), "event: response.completed"))
			}
		})
	}
}

//go:build unit

package handler

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// An optional feature that fails to start leaves the request as it is.
func TestCollabSeamArmingPanicLeavesTheRequestAlone(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Collab.Ladder = true })
	h := &OpenAIGatewayHandler{
		collabRescue:    func(*gin.Context, *collabSeam, *collabWithheld) {},
		collabRescuable: func(*collabSeam) *atomic.Bool { panic("eligibility") },
	}
	c, _ := seamTestTracked()
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
	original := c.Writer
	body := []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"opaque"}]}]}`)
	var seam *collabSeam
	require.NotPanics(t, func() { seam = h.armCollabSeam(c, body, "", service.PlatformOpenAI, false) })
	require.Nil(t, seam)
	require.True(t, original == c.Writer, "the writer was replaced")
}

// An error that stops being rescuable goes out with the next call.
func TestCollabSeamRevocationReleasesAHeldError(t *testing.T) {
	for name, next := range map[string]func(*gin.Context){
		"flush":      func(c *gin.Context) { c.Writer.Flush() },
		"header now": func(c *gin.Context) { c.Writer.WriteHeaderNow() },
		"write":      func(c *gin.Context) { _, _ = c.Writer.Write([]byte(" ")) },
		"string":     func(c *gin.Context) { _, _ = c.Writer.WriteString(" ") },
	} {
		c, wire := seamTestTracked()
		eligible, calls := seamTestFlag(true), 0
		withSeamWhen(c, func(*gin.Context, *collabSeam, *collabWithheld) { calls++ }, eligible, func(*collabSeam) {
			seamTestExistingError(c)()
			require.Zero(t, wire.Body.Len(), name)
			eligible.Store(false)
			next(c)
			require.Equal(t, seamTestHTTPFail, strings.TrimSuffix(wire.Body.String(), " "), name)
			require.Equal(t, http.StatusBadGateway, wire.Code, name)
			require.Equal(t, wire.Body.Len(), c.Writer.Size(), name)
		})
		require.Zero(t, calls, name)
		require.Equal(t, "7", wire.Header().Get("Retry-After"), name)
	}
}

// A commit cannot be taken back, but the failure is recorded as the one that
// was saved and not as a recovered request.
func TestCollabSeamLostCommitmentIsRecordedAsTheSavedFailure(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name string
		edge bool
		want int
		fail func(*gin.Context)
	}{
		{name: "error", want: http.StatusBadGateway},
		{name: "edge mapped error", edge: true, want: http.StatusBadGateway},
		{name: "failed frame", want: http.StatusServiceUnavailable, fail: func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.WriteString("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"refused\"}}}\n\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			defer resetOpsErrorLoggerStateForTest(t)
			c, wire := seamTestTracked()
			if tc.edge {
				resilience.StateFrom(c.Request.Context()).SetEdge("cf")
			}
			capture := acquireOpsCaptureWriter(c.Writer)
			capture.setContext(c)
			c.Writer = capture
			defer releaseOpsCaptureWriter(capture)
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Header("Content-Type", "application/json")
				wire.err = io.ErrClosedPipe
				_, _ = c.Writer.WriteString(seamTestJSON)
				wire.err = nil
			}, func() {
				if tc.fail != nil {
					tc.fail(c)
					return
				}
				service.SetOpsUpstreamError(c, http.StatusBadGateway, "saved upstream failure", "")
				service.WriteTargetProtocolError(c, service.PreOutputProtocolResponses, http.StatusBadGateway, "server_error", "server_error", "", "saved")
			})
			recordOpsErrorAfterRequest(c, service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), capture)
			require.Len(t, opsErrorLogQueue, 1)
			require.Equal(t, tc.want, (<-opsErrorLogQueue).entry.StatusCode)
		})
	}
}

// The existing path's own stream error marker does not keep a lost commitment
// from being recorded as the failure it was.
func TestCollabSeamLostCommitmentPromotesTheExistingMarker(t *testing.T) {
	withHandlerResilience(t, nil)
	setupOpsErrorLogTestQueue(t, 2)
	defer resetOpsErrorLoggerStateForTest(t)
	c, wire := seamTestTracked()
	capture := acquireOpsCaptureWriter(c.Writer)
	capture.setContext(c)
	c.Writer = capture
	defer releaseOpsCaptureWriter(capture)
	withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
		c.Header("Content-Type", "application/json")
		wire.err = io.ErrClosedPipe
		_, _ = c.Writer.WriteString(seamTestJSON)
		wire.err = nil
	}, func() {
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString(": comment\n")
		require.True(t, service.WriteTargetProtocolError(c, service.PreOutputProtocolResponses, http.StatusBadGateway, "server_error", "saved_code", "", "saved"))
	})
	markers := service.GetOpsStreamErrors(c)
	require.Len(t, markers, 1, "the existing marker is promoted, not joined by a second")
	require.Equal(t, "saved", markers[0].Message)
	recordOpsErrorAfterRequest(c, service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), capture)
	require.Len(t, opsErrorLogQueue, 1)
	require.Equal(t, http.StatusBadGateway, (<-opsErrorLogQueue).entry.StatusCode)
}

// A status change after a withheld error that stopped being rescuable lets
// the error go, and is then ignored as it is today.
func TestCollabSeamRevocationReleasesOnWriteHeader(t *testing.T) {
	c, wire := seamTestTracked()
	eligible := seamTestFlag(true)
	withSeamWhen(c, nil, eligible, func(*collabSeam) {
		seamTestExistingError(c)()
		eligible.Store(false)
		c.Writer.WriteHeader(http.StatusServiceUnavailable)
		require.Equal(t, seamTestHTTPFail, wire.Body.String())
	})
	require.Equal(t, http.StatusBadGateway, wire.Code)
}

// A takeover cannot deliver under a status that forbids a body: it is no
// delivery, and the response is still uncommitted for the saved error.
func TestCollabSeamTakeoverUnderABodilessStatus(t *testing.T) {
	server := newSeamTestServer(t)
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		got := server.do(t, func(c *gin.Context) {
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Header("Content-Type", "application/json")
				c.Writer.WriteHeader(status)
				_, _ = c.Writer.WriteString(seamTestJSON)
			}, seamTestExistingError(c))
		})
		require.Equal(t, http.StatusBadGateway, got.status, status)
		require.Equal(t, seamTestHTTPFail, got.body, status)
		require.Equal(t, "7", got.header.Get("Retry-After"), status)
	}
}

// The seam joins a takeover's parts, so a copied length must not cut them off.
func TestCollabSeamTakeoverDropsACopiedLength(t *testing.T) {
	server := newSeamTestServer(t)
	for contentType, body := range map[string]string{"application/json": seamTestJSON, "text/event-stream": seamTestCreated + seamTestDelta} {
		got := server.do(t, func(c *gin.Context) {
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Header("Content-Type", contentType)
				c.Header("Content-Length", "3")
				_, _ = c.Writer.WriteString(body)
			}, seamTestExistingError(c))
		})
		require.Equal(t, http.StatusOK, got.status, contentType)
		require.Equal(t, body, got.body, contentType)
	}
}

func TestCollabSeamMeteredCacheDetails(t *testing.T) {
	for _, usage := range []service.OpenAIUsage{{CacheCreation5mTokens: 1}, {CacheCreation1hTokens: 1}} {
		require.True(t, collabSeamMetered(&service.OpenAIForwardResult{Usage: usage}))
	}
}

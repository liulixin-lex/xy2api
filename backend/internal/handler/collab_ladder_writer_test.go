//go:build unit

package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCollabTakeoverContinuesDeliveredPrelude(t *testing.T) {
	server := newSeamTestServer(t)
	const original = "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_first\",\"created_at\":12}}\n\n"
	const replacement = "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_second\",\"created_at\":99}}\n\n"
	const completed = "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_second\",\"created_at\":99,\"status\":\"completed\"}}\n\n"
	for _, width := range []int{1, 7, 2048} {
		wire := server.do(t, func(c *gin.Context) {
			withSeam(c, func(c *gin.Context, s *collabSeam, _ *collabWithheld) {
				require.Equal(t, collabDeliveredPrelude{ID: "resp_first", CreatedAt: 12, Created: true, NextSequence: 1}, s.writer.deliveredPrelude())
				data := replacement + seamTestDelta + completed
				for len(data) > 0 {
					n := min(width, len(data))
					_, err := c.Writer.WriteString(data[:n])
					require.NoError(t, err)
					data = data[n:]
				}
			}, func() { seamTestStream(c, original, seamTestFailed) })
		})
		require.Equal(t, 1, strings.Count(wire.body, "response.created"))
		require.NotContains(t, wire.body, "resp_second")
		require.Contains(t, wire.body, `"created_at":12`)
		require.Contains(t, wire.body, `"sequence_number":1`)
		require.NotContains(t, wire.body, "response.failed")
	}
}

func TestCollabPreludeTracksFragmentsAndPreservesTrailingBytes(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	w := newCollabSeamWriter(c.Writer, func() bool { return false })
	w.Header().Set("Content-Type", "text/event-stream")
	created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_seen\"}}\n\n"
	for _, b := range []byte(created) {
		_, err := w.Write([]byte{b})
		require.NoError(t, err)
	}
	require.True(t, w.deliveredPrelude().Created)
	w.rescuable = seamTestAlways
	_, err := w.WriteString(seamTestFailed)
	require.NoError(t, err)
	require.NotNil(t, w.suspend())
	_, err = w.WriteString(seamTestDelta)
	require.NoError(t, err)
	_, err = w.WriteString(": tail")
	require.NoError(t, err)
	w.release()
	require.Equal(t, 0, len(w.continuation))
	require.True(t, strings.HasSuffix(recorder.Body.String(), ": tail"))
}

func TestCollabNoDeliveredPreludeKeepsFastOutputPath(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := newCollabSeamWriter(c.Writer, seamTestAlways)
	w.WriteHeader(502)
	_, err := w.WriteString(seamTestHTTPFail)
	require.NoError(t, err)
	require.NotNil(t, w.suspend())
	require.False(t, w.takeover.Load())
	w.Header().Set("Content-Type", "text/event-stream")
	_, err = w.WriteString(seamTestDelta)
	require.NoError(t, err)
	require.Equal(t, collabSeamPassing, w.current())
	require.False(t, w.takeover.Load())
}

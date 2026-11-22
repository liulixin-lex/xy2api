//go:build unit

package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// What stops being rescuable is not held back any longer.
func TestCollabSeamRevokedEligibility(t *testing.T) {
	t.Run("flush releases the prefix", func(t *testing.T) {
		c, wire := seamTestTracked()
		var eligible atomic.Bool
		eligible.Store(true)
		withSeamWhen(c, nil, &eligible, func(*collabSeam) {
			c.Header("Content-Type", "text/event-stream")
			const prefix = "event: response.created\n"
			_, err := c.Writer.WriteString(prefix)
			require.NoError(t, err)
			require.Zero(t, wire.Body.Len())
			eligible.Store(false)
			c.Writer.Flush()
			require.Equal(t, prefix, wire.Body.String())
		})
	})
	t.Run("held error is not taken over", func(t *testing.T) {
		c, wire := seamTestTracked()
		var eligible atomic.Bool
		eligible.Store(true)
		calls := 0
		withSeamWhen(c, func(*gin.Context, *collabSeam, *collabWithheld) { calls++ }, &eligible, func(*collabSeam) {
			seamTestExistingError(c)()
			eligible.Store(false)
		})
		require.Zero(t, calls)
		require.Equal(t, seamTestHTTPFail, wire.Body.String())
	})
}

func TestCollabSeamFailedTakeoverRestoresErrors(t *testing.T) {
	c, _ := seamTestTracked()
	withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
		_ = c.Error(errors.New("optional attempt"))
	}, func() {
		_ = c.Error(errors.New("existing path"))
		seamTestExistingError(c)()
	})
	require.Len(t, c.Errors, 1)
	require.Equal(t, "existing path", c.Errors[0].Error())
}

// Once an attempt committed the response the saved status cannot come back,
// so the saved body must not follow under another status. It is no delivery.
func TestCollabSeamCommitmentIsNotRolledBack(t *testing.T) {
	for contentType, body := range map[string]string{"text/event-stream": seamTestDelta, "application/json": seamTestJSON} {
		c, wire := seamTestTracked()
		withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
			c.Header("Content-Type", contentType)
			wire.err = io.ErrClosedPipe
			_, _ = c.Writer.WriteString(body)
			wire.err = nil
		}, func() {
			require.True(t, service.ClaimProtocolTerminal(c))
			c.Data(http.StatusBadGateway, "application/json", []byte(seamTestHTTPFail))
		})
		require.Equal(t, http.StatusOK, wire.Code, contentType)
		require.Empty(t, wire.Body.String(), contentType)
		require.True(t, service.ProtocolTerminalClaimed(c), "the existing path's outcome stands")
	}
}

func TestCollabSeamJSONAssemblyIsLinear(t *testing.T) {
	c, wire := seamTestTracked()
	const n = 16384
	parts := make([]string, 0, n+2)
	parts = append(parts, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"nested":[`)
	for i := range n {
		part := `{"a":"}]\"{"}`
		if i > 0 {
			part = "," + part
		}
		parts = append(parts, part)
	}
	parts = append(parts, `]}`)
	started := time.Now()
	withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
		c.Header("Content-Type", "application/json")
		for _, part := range parts {
			_, err := c.Writer.WriteString(part)
			require.NoError(t, err)
		}
	}, seamTestExistingError(c))
	require.Less(t, time.Since(started), 500*time.Millisecond)
	require.Equal(t, strings.Join(parts, ""), wire.Body.String())
}

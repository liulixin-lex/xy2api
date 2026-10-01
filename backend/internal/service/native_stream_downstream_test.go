package service

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamSchedulingTimeoutDoesNotDetachClient(t *testing.T) {
	for _, identityExposed := range []bool{false, true} {
		name := "late_provider_frame"
		if identityExposed {
			name = "heartbeat_after_identity"
		}
		t.Run(name, func(t *testing.T) {
			ctx, r, d := nativeCommitFixture(t)
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
			w.Header().Set("Content-Type", "text/event-stream")
			if identityExposed {
				w.Header().Set("Openai-Response-Id", "resp_original")
				_, err := WriteNativeStreamFrame(ctx, w, []byte("data: {\"type\":\"response.created\"}\n\n"))
				require.NoError(t, err)
			}
			before := out.Body.String()
			d.mu.Lock()
			d.timeout = true
			d.cancellationReason = ControlledContentTimeout
			d.cancel()
			d.mu.Unlock()
			frame := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"late\"}\n\n")
			if identityExposed {
				frame = []byte(":\n\n")
			}
			n, err := WriteNativeStreamFrame(ctx, w, frame)
			require.Zero(t, n)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NotErrorIs(t, err, context.Canceled)
			require.Equal(t, ControlledContentTimeout, ControlledStreamSnapshot(ctx).CancelReason)
			require.NoError(t, r.clientContext.Err())
			require.Equal(t, before, out.Body.String())
			n, err = WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
			require.NoError(t, err)
			require.Equal(t, len(controlledFailureTestFrame), n)
			require.Equal(t, before+controlledFailureTestFrame, out.Body.String())
			require.True(t, ControlledStreamFailureWritten(ctx))
			n, err = WriteNativeStreamFrame(ctx, w, frame)
			require.Zero(t, n)
			require.ErrorIs(t, err, scheduling.ErrCommitted)
			require.NotErrorIs(t, err, context.Canceled)
			require.Equal(t, ControlledContentTimeout, ControlledStreamSnapshot(ctx).CancelReason)
			require.Equal(t, before+controlledFailureTestFrame, out.Body.String())
		})
	}
}

func TestNativeStreamNetworkFailureStillCancelsLiveRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason ControlledCancelReason
	}{
		{name: "closed_pipe", err: io.ErrClosedPipe, reason: ControlledClientDetached},
		{name: "network_deadline", err: &net.OpError{Op: "write", Net: "tcp", Err: context.DeadlineExceeded}, reason: ControlledSlowConsumer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, _ := nativeCommitFixture(t)
			err := nativeStreamDownstreamError(ctx, tc.err)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, tc.reason, ControlledStreamSnapshot(ctx).CancelReason)
			require.False(t, ControlledStreamFailureWritten(ctx))
		})
	}
}

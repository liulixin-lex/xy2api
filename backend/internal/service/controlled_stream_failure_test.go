package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

const controlledFailureTestFrame = "event: error\ndata: {\"type\":\"error\",\"sequence_number\":2,\"code\":\"content_timeout\",\"message\":\"Upstream content timed out\",\"param\":null}\n\n"

func TestControlledStreamFailureExpiredAttemptRemainsWritable(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "before_identity"
		if committed {
			name = "after_identity"
		}
		t.Run(name, func(t *testing.T) {
			ctx, r, d := nativeCommitFixture(t)
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
			w.Header().Set("Content-Type", "text/event-stream")
			if committed {
				require.NoError(t, CommitControlledOutput(ctx, []byte(`{"type":"response.created"}`)))
				w.Header().Set("Openai-Response-Id", "resp_original")
			}
			d.mu.Lock()
			d.timeout = true
			d.cancellationReason = ControlledContentTimeout
			d.cancel()
			d.mu.Unlock()
			require.ErrorIs(t, CommitControlledOutput(ctx, []byte(`{"type":"response.output_text.delta","delta":"late"}`)), context.DeadlineExceeded)
			n, err := WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
			require.NoError(t, err)
			require.Equal(t, len(controlledFailureTestFrame), n)
			require.Equal(t, controlledFailureTestFrame, out.Body.String())
			require.True(t, out.Flushed)
			require.True(t, ControlledStreamFailureWritten(ctx))
			require.Equal(t, committed, ControlledStreamSnapshot(ctx).AttemptCommitted)
			require.False(t, ControlledStreamSnapshot(ctx).SemanticSeen)
			require.ErrorIs(t, r.Ledger.BeginAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
			_, err = w.Write([]byte("data: late-provider-frame\n\n"))
			require.ErrorIs(t, err, scheduling.ErrCommitted)
			n, err = WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
			require.NoError(t, err)
			require.Zero(t, n)
			require.Equal(t, controlledFailureTestFrame, out.Body.String())
			if committed {
				require.Equal(t, "resp_original", out.Header().Get("Openai-Response-Id"))
			}
		})
	}
}

func TestControlledStreamFailureRejectsControlCancellation(t *testing.T) {
	for _, reason := range []ControlledCancelReason{ControlledClientDetached, ControlledUserStop, ControlledAdminCancel, ControlledLeaseLost, ControlledSlowConsumer, ControlledPermissionRevoked, ControlledAuthorizationUnavailable, ControlledDeadline} {
		t.Run(string(reason), func(t *testing.T) {
			ctx, r, _ := nativeCommitFixture(t)
			require.NoError(t, CancelControlledRequest(ctx, reason))
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
			n, err := WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, n)
			require.Empty(t, out.Body.String())
			require.False(t, ControlledStreamFailureWritten(ctx))
		})
	}
}

func TestControlledStreamFailureParentAndAttemptLifetimes(t *testing.T) {
	t.Run("expired_attempt_live_parent", func(t *testing.T) {
		ctx, r, d := nativeCommitFixture(t)
		d.ctx = context.WithValue(d.ctx, controlledDispatchContextKey{}, d)
		d.timeout = true
		d.cancellationReason = ControlledStartupTimeout
		d.cancel()
		out := newNativeStreamTestRecorder()
		c, _ := gin.CreateTestContext(out)
		w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
		n, err := WriteControlledStreamFailure(d.ctx, w, []byte(controlledFailureTestFrame))
		require.NoError(t, err)
		require.Positive(t, n)
		require.True(t, ControlledStreamFailureWritten(ctx))
	})
	for _, deadline := range []bool{false, true} {
		name := "caller_cancelled"
		if deadline {
			name = "caller_deadline"
		}
		t.Run(name, func(t *testing.T) {
			var parent context.Context
			var cancel context.CancelFunc
			if deadline {
				parent, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				parent, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			ctx := NewControlledRequestContext(WithNativeStreamPolicy(parent, NativeStreamPolicy{Delivery: true}), "responses")
			cancel()
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: controlledRequest(ctx)}
			n, err := WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
			require.ErrorIs(t, err, parent.Err())
			require.Zero(t, n)
			require.Empty(t, out.Body.String())
		})
	}
}

func TestControlledStreamFailureConcurrentExactlyOnce(t *testing.T) {
	ctx, r, _ := nativeCommitFixture(t)
	out := newNativeStreamTestRecorder()
	c, _ := gin.CreateTestContext(out)
	w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
	var writes, failures atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
			if err != nil {
				failures.Add(1)
			}
			if n > 0 {
				writes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, writes.Load())
	require.Zero(t, failures.Load())
	require.Equal(t, controlledFailureTestFrame, out.Body.String())
}

func TestControlledStreamFailureSuspendsOwnedCompactWrapper(t *testing.T) {
	ctx, r, d := nativeCommitFixture(t)
	out := newNativeStreamTestRecorder()
	c, _ := gin.CreateTestContext(out)
	c.Request = (&http.Request{}).WithContext(ctx)
	c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
	stop := startOpenAISSEKeepalive(c, time.Hour)
	defer stop()
	wrapper, ok := c.Writer.(*openAICompactKeepaliveWriter)
	require.True(t, ok)
	d.mu.Lock()
	d.timeout = true
	d.cancellationReason = ControlledStartupTimeout
	d.cancel()
	d.mu.Unlock()
	n, err := WriteControlledStreamFailure(ctx, c.Writer, []byte(controlledFailureTestFrame))
	require.NoError(t, err)
	require.Equal(t, len(controlledFailureTestFrame), n)
	require.Equal(t, controlledFailureTestFrame, out.Body.String())
	require.True(t, ControlledStreamFailureWritten(ctx))
	wrapper.k.mu.Lock()
	stopped := wrapper.k.stopped
	wrapper.k.mu.Unlock()
	require.True(t, stopped)
}

func TestControlledStreamFailureFailedFlushCannotClaimDelivery(t *testing.T) {
	ctx, r, _ := nativeCommitFixture(t)
	out := &reviewFlushErrorWriter{header: make(http.Header)}
	c, _ := gin.CreateTestContext(out)
	w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
	n, err := WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
	require.Equal(t, len(controlledFailureTestFrame), n)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, ControlledStreamFailureWritten(ctx))
	n, err = WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, 1, out.writes)
}

type controlledFailureBlockedWriter struct {
	header  http.Header
	started chan struct{}
	expired chan struct{}
	once    sync.Once
}

func (w *controlledFailureBlockedWriter) Header() http.Header { return w.header }
func (w *controlledFailureBlockedWriter) WriteHeader(int)     {}
func (w *controlledFailureBlockedWriter) Flush()              {}
func (w *controlledFailureBlockedWriter) SetWriteDeadline(at time.Time) error {
	if !at.IsZero() && !at.After(time.Now()) {
		w.once.Do(func() { close(w.expired) })
	}
	return nil
}
func (w *controlledFailureBlockedWriter) Write([]byte) (int, error) {
	close(w.started)
	<-w.expired
	return 0, io.ErrClosedPipe
}

func TestControlledStreamFailureControlInterruptsBlockedWrite(t *testing.T) {
	for _, reason := range []ControlledCancelReason{ControlledAdminCancel, ControlledLeaseLost, ControlledPermissionRevoked} {
		t.Run(string(reason), func(t *testing.T) {
			ctx, r, _ := nativeCommitFixture(t)
			out := &controlledFailureBlockedWriter{header: make(http.Header), started: make(chan struct{}), expired: make(chan struct{})}
			c, _ := gin.CreateTestContext(out)
			w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
			done := make(chan error, 1)
			go func() {
				_, err := WriteControlledStreamFailure(ctx, w, []byte(controlledFailureTestFrame))
				done <- err
			}()
			select {
			case <-out.started:
			case <-time.After(time.Second):
				t.Fatal("failure write never started")
			}
			require.NoError(t, CancelControlledRequest(ctx, reason))
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("control did not interrupt blocked terminal write")
			}
			require.False(t, ControlledStreamFailureWritten(ctx))
		})
	}
}

func TestControlledStreamFailureOnlyAcceptsFailureFrames(t *testing.T) {
	for _, frame := range []string{
		controlledFailureTestFrame,
		"data: {\"error\":{\"type\":\"upstream_error\",\"message\":\"Upstream failed\"}}\n\n",
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_original\",\"status\":\"failed\",\"error\":{\"code\":\"content_timeout\",\"message\":\"Upstream failed\"}}}\n\n",
	} {
		require.True(t, validControlledStreamFailureFrame([]byte(frame)))
	}
	for _, frame := range []string{
		"data: {\"type\":\"response.created\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"injected\"}\n\n",
		"data: {\"type\":\"error\",\"type\":\"response.created\",\"message\":\"bad\"}\n\n",
		controlledFailureTestFrame + "data: [DONE]\n\n",
		controlledFailureTestFrame[:len(controlledFailureTestFrame)-1],
	} {
		ctx, r, _ := nativeCommitFixture(t)
		out := newNativeStreamTestRecorder()
		c, _ := gin.CreateTestContext(out)
		w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
		_, err := WriteControlledStreamFailure(ctx, w, []byte(frame))
		require.True(t, errors.Is(err, errControlledStreamFailureFrame), "frame=%q error=%v", frame, err)
		require.False(t, r.Ledger.Snapshot().Committed)
		require.Empty(t, out.Body.String())
	}
}

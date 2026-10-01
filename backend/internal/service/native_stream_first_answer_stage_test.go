package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNativeResponsesPreAnswerBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "created", payload: `{"type":"response.created","response":{"id":"resp_1"}}`, want: false},
		{name: "reasoning", payload: `{"type":"response.reasoning_text.delta","delta":"thinking"}`, want: false},
		{name: "empty text part", payload: `{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`, want: false},
		{name: "answer delta", payload: `{"type":"response.output_text.delta","delta":"hello"}`, want: true},
		{name: "tool delta", payload: `{"type":"response.function_call_arguments.delta","delta":"{\"q\":1}"}`, want: true},
		{name: "terminal", payload: `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1}}}`, want: true},
		{name: "unknown", payload: `{"type":"future.output","payload":1}`, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, nativeResponsesPreAnswerBoundary([]byte(tc.payload)))
		})
	}
}

func TestNativeFirstAnswerRecoveryStagesPreambleUntilAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := NewControlledRequestContext(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 1, Delivery: true, Recovery: true}), "responses")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	rec := newNativeStreamTestRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	upstreamReader := newPreambleBlockingReadCloser([]string{
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_first\"}}\n\n",
		"data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_first\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
	}, 2)
	defer upstreamReader.Release()

	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	resultCh := make(chan error, 1)
	go func() {
		_, err := svc.handleStreamingResponse(ctx, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: upstreamReader}, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
		resultCh <- err
	}()

	select {
	case <-upstreamReader.blocked:
	case <-time.After(time.Second):
		t.Fatal("upstream reader did not block after the preamble")
	}
	// The handler is blocked in Read, so inspecting the recorder cannot race a
	// concurrent downstream write. The preamble is still private and retryable.
	require.Empty(t, rec.Body.Bytes())
	require.False(t, ControlledStreamSnapshot(ctx).AttemptCommitted)

	upstreamReader.Release()
	select {
	case err := <-resultCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not finish after the answer boundary")
	}

	body := rec.Body.String()
	createdAt := strings.Index(body, "response.created")
	reasoningAt := strings.Index(body, "response.reasoning_text.delta")
	answerAt := strings.Index(body, "response.output_text.delta")
	completedAt := strings.Index(body, "response.completed")
	require.GreaterOrEqual(t, createdAt, 0)
	require.Greater(t, reasoningAt, createdAt)
	require.Greater(t, answerAt, reasoningAt)
	require.Greater(t, completedAt, answerAt)
	require.True(t, ControlledStreamSnapshot(ctx).AttemptCommitted)
}

// preambleBlockingReadCloser makes the next scanner read observable. It avoids
// a timing assertion while the handler owns the recorder writer.
type preambleBlockingReadCloser struct {
	mu             sync.Mutex
	frames         [][]byte
	preambleFrames int
	pending        []byte
	next           int
	passedBlock    bool
	blocked        chan struct{}
	release        chan struct{}
	releaseOnce    sync.Once
}

func newPreambleBlockingReadCloser(frames []string, preambleFrames int) *preambleBlockingReadCloser {
	encoded := make([][]byte, len(frames))
	for i, frame := range frames {
		encoded[i] = []byte(frame)
	}
	return &preambleBlockingReadCloser{
		frames:         encoded,
		preambleFrames: preambleFrames,
		blocked:        make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (r *preambleBlockingReadCloser) Read(p []byte) (int, error) {
	for {
		r.mu.Lock()
		if len(r.pending) != 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			r.mu.Unlock()
			return n, nil
		}
		if !r.passedBlock && r.next == r.preambleFrames {
			r.passedBlock = true
			close(r.blocked)
			r.mu.Unlock()
			<-r.release
			continue
		}
		if r.next == len(r.frames) {
			r.mu.Unlock()
			return 0, io.EOF
		}
		r.pending = r.frames[r.next]
		r.next++
		r.mu.Unlock()
	}
}

func (r *preambleBlockingReadCloser) Close() error {
	r.Release()
	return nil
}

func (r *preambleBlockingReadCloser) Release() {
	r.releaseOnce.Do(func() { close(r.release) })
}

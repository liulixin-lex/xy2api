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

func TestNativeResponsesFirstAnswerOutputExcludesReasoning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "created", payload: `{"type":"response.created","response":{"id":"resp_1"}}`, want: false},
		{name: "reasoning delta", payload: `{"type":"response.reasoning_text.delta","delta":"thinking"}`, want: false},
		{name: "reasoning summary part added", payload: `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":"thinking"}}`, want: false},
		{name: "reasoning summary part done", payload: `{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text","text":"thinking"}}`, want: false},
		{name: "content summary part added", payload: `{"type":"response.content_part.added","part":{"type":"summary_text","text":"thinking"}}`, want: false},
		{name: "content summary part done", payload: `{"type":"response.content_part.done","part":{"type":"summary_text","text":"thinking"}}`, want: false},
		{name: "content answer part", payload: `{"type":"response.content_part.done","part":{"type":"output_text","text":"hello"}}`, want: true},
		{name: "reasoning item summary", payload: `{"type":"response.output_item.done","item":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}}`, want: false},
		{name: "reasoning only completed", payload: `{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}]}}`, want: false},
		{name: "message answer", payload: `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"hello"}]}}`, want: true},
		{name: "answer delta", payload: `{"type":"response.output_text.delta","delta":"hello"}`, want: true},
		{name: "tool delta", payload: `{"type":"response.function_call_arguments.delta","delta":"{\"q\":1}"}`, want: true},
		{name: "completed with answer", payload: `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, nativeResponsesFirstAnswerOutput([]byte(tc.payload)))
		})
	}
}

func TestNativeRecoveryDeliversPreambleBeforeAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "converted", true: "passthrough"}[passthrough], func(t *testing.T) {
			ctx := NewControlledRequestContext(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 1, Delivery: true, Recovery: true}), "responses")
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			rec := &notifyingNativeStreamRecorder{nativeStreamTestRecorder: newNativeStreamTestRecorder(), writes: make(chan []byte, 8)}
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
				response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: upstreamReader}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, response, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
				} else {
					_, err = svc.handleStreamingResponse(ctx, response, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
				}
				resultCh <- err
			}()

			select {
			case <-upstreamReader.blocked:
			case <-time.After(time.Second):
				t.Fatal("upstream reader did not block after the preamble")
			}
			// The handler may still be processing the preamble when Read first blocks.
			// Wait for both actual downstream writes without releasing the answer frame.
			before := ""
			for !strings.Contains(before, "response.reasoning_text.delta") {
				select {
				case frame := <-rec.writes:
					before += string(frame)
				case <-time.After(time.Second):
					t.Fatal("complete preamble was not flushed before the answer frame")
				}
			}
			require.Contains(t, before, "response.created")
			require.Contains(t, before, "response.reasoning_text.delta")
			require.NotContains(t, before, "response.output_text.delta")
			require.True(t, ControlledStreamSnapshot(ctx).AttemptCommitted)

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
		})
	}
}

type notifyingNativeStreamRecorder struct {
	*nativeStreamTestRecorder
	writes chan []byte
}

func (r *notifyingNativeStreamRecorder) Write(p []byte) (int, error) {
	n, err := r.nativeStreamTestRecorder.Write(p)
	if n > 0 {
		r.writes <- append([]byte(nil), p[:n]...)
	}
	return n, err
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

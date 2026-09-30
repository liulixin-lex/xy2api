package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type reviewCommentFrameWriter struct {
	*nativeStreamTestRecorder
	ctx       context.Context
	request   *ControlledRequest
	states    []ControlledStreamState
	retryErrs []error
	flushErr  error
}

func (w *reviewCommentFrameWriter) FlushError() error {
	w.Flush()
	w.states = append(w.states, ControlledStreamSnapshot(w.ctx))
	w.retryErrs = append(w.retryErrs, w.request.Ledger.CanAttempt(2, 0, time.Now(), true))
	return w.flushErr
}

func reviewRelayCommentInput(t *testing.T, passthrough bool, input string, flushErr error) (*reviewCommentFrameWriter, error) {
	t.Helper()
	ctx, request, _ := nativeCommitFixture(t)
	writer := &reviewCommentFrameWriter{
		nativeStreamTestRecorder: newNativeStreamTestRecorder(),
		ctx:                      ctx,
		request:                  request,
		flushErr:                 flushErr,
	}
	c, _ := gin.CreateTestContext(writer)
	c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: request}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(input))}
	svc := nativeRelayService()
	var err error
	if passthrough {
		_, err = svc.handleStreamingResponsePassthrough(ctx, response, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
	} else {
		_, err = svc.handleStreamingResponse(ctx, response, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
	}
	return writer, err
}

func TestReviewNativeStreamUpstreamCommentDoesNotCommitAttempt(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, comment := range []string{": ping\n\n", ": first\n: second\n\n"} {
			t.Run(fmt.Sprintf("passthrough_%v_multiline_%v", passthrough, strings.Contains(comment, "second")), func(t *testing.T) {
				created := nativeRelaySSE("response.created", "\"sequence_number\":0,\"response\":{\"id\":\"resp_comment\"}")
				terminal := nativeRelaySSE("response.completed", "\"sequence_number\":1,\"response\":{\"id\":\"resp_comment\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
				writer, err := reviewRelayCommentInput(t, passthrough, comment+created+terminal, nil)
				t.Logf("upstream_comment relay_error=%v flushes=%d states=%+v body=%q", err, len(writer.states), writer.states, writer.Body.String())
				require.NoError(t, err, "an upstream SSE comment is legal liveness, not an invalid protocol event")
				require.Len(t, writer.states, 3, "comment, created, and terminal each flush immediately")
				require.True(t, strings.HasPrefix(writer.Body.String(), comment+created), "original comment and created retain order and bytes")
				require.Equal(t, 1, strings.Count(writer.Body.String(), "event: response.completed"))
				require.True(t, writer.states[0].HTTPCommitted)
				require.False(t, writer.states[0].AttemptCommitted)
				require.False(t, writer.states[0].SemanticSeen)
				require.NoError(t, writer.retryErrs[0], "liveness cannot lock a generation attempt")
				require.True(t, writer.states[1].AttemptCommitted)
				require.False(t, writer.states[1].SemanticSeen, "created is identity, not content")
				require.ErrorIs(t, writer.retryErrs[1], scheduling.ErrCommitted)
			})
		}
	}
}

func TestReviewNativeStreamCommentWriteFailureDoesNotCommitAttempt(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%v", passthrough), func(t *testing.T) {
			writer, err := reviewRelayCommentInput(t, passthrough, ": ping\n\n"+nativeRelaySSE("response.created", "\"response\":{\"id\":\"resp_never_sent\"}"), io.ErrClosedPipe)
			t.Logf("comment_flush_failure relay_error=%v flushes=%d states=%+v body=%q", err, len(writer.states), writer.states, writer.Body.String())
			require.ErrorIs(t, err, context.Canceled, "actual downstream failure retains client-detached attribution")
			require.Len(t, writer.states, 1)
			require.Equal(t, ": ping\n\n", writer.Body.String())
			require.True(t, writer.states[0].HTTPCommitted)
			require.False(t, writer.states[0].AttemptCommitted)
			require.False(t, writer.states[0].SemanticSeen)
			require.NoError(t, writer.retryErrs[0])
		})
	}
}

func TestReviewNativeStreamEmptyDataIsNotComment(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for index, input := range []string{"data:\n\n", "data:   \n\n", "data: {broken}\n\n"} {
			t.Run(fmt.Sprintf("passthrough_%v_case_%d", passthrough, index), func(t *testing.T) {
				writer, err := reviewRelayCommentInput(t, passthrough, input, nil)
				t.Logf("invalid_data relay_error=%v flushes=%d body=%q", err, len(writer.states), writer.Body.String())
				require.Error(t, err)
				require.Empty(t, writer.states, "empty or invalid data must be rejected before Write/Flush")
				require.Empty(t, writer.Body.String())
				require.False(t, ControlledStreamSnapshot(writer.ctx).AttemptCommitted)
				require.NoError(t, writer.request.Ledger.CanAttempt(2, 0, time.Now(), true))
			})
		}
	}
}

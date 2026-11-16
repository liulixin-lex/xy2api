//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type uploadReadNetError struct{ timeout bool }

func (e uploadReadNetError) Error() string   { return "request body transport failure" }
func (e uploadReadNetError) Timeout() bool   { return e.timeout }
func (e uploadReadNetError) Temporary() bool { return false }

func TestM8UploadTimeoutResponse(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	tests := []struct {
		name    string
		ctx     context.Context
		err     error
		kind    string
		status  int
		code    string
		message string
	}{
		{"timeout", nil, uploadReadNetError{timeout: true}, "upload_timeout", 408, "request_upload_timeout", "Timed out reading request body"},
		{"wrapped_timeout", nil, fmt.Errorf("read: %w", uploadReadNetError{timeout: true}), "upload_timeout", 408, "request_upload_timeout", "Timed out reading request body"},
		{"transport", nil, uploadReadNetError{}, "transport", 400, "", "Failed to read request body"},
		{"deadline_error", nil, context.DeadlineExceeded, "upload_timeout", 408, "request_upload_timeout", "Timed out reading request body"},
		{"request_deadline", deadline, io.ErrUnexpectedEOF, "truncated_body", 408, "request_upload_timeout", "Timed out reading request body"},
		{"max_bytes", deadline, &http.MaxBytesError{Limit: 10}, "max_bytes", 413, "", "Request body too large, limit is 10B"},
		{"decode", deadline, fmt.Errorf("decode Content-Encoding gzip: %w", uploadReadNetError{timeout: true}), "decode_content_encoding", 400, "", "Failed to read request body"},
		{"unsupported_encoding", nil, errors.New("decode Content-Encoding br: unsupported Content-Encoding"), "unsupported_content_encoding", 400, "", "Failed to read request body"},
		{"truncated", nil, io.ErrUnexpectedEOF, "truncated_body", 400, "", "Failed to read request body"},
		{"eof", nil, io.EOF, "io_read", 400, "", "Failed to read request body"},
		{"cancel_error", nil, context.Canceled, "client_disconnect", 499, "", ""},
		{"request_canceled", canceled, uploadReadNetError{timeout: true}, "upload_timeout", 499, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tt.ctx != nil {
				req = req.WithContext(tt.ctx)
			}
			require.Equal(t, tt.kind, requestBodyReadErrorKind(tt.err))
			status, errType, code, message := requestBodyReadFailureResponse(req, tt.err)
			require.Equal(t, tt.status, status)
			require.Equal(t, tt.code, code)
			require.Equal(t, tt.message, message)
			if status == statusClientClosedRequest {
				require.Empty(t, errType)
			} else {
				require.Equal(t, "invalid_request_error", errType)
			}
		})
	}
	t.Run("seven_ingress_paths", func(t *testing.T) {
		upstream := &openAIResponsesFailoverCancelUpstream{}
		openai := newOpenAIResponsesFailoverTestHandler(t, upstream)
		gateway := newGatewayConvertUsageHandler(t, &gwUsageLogRepo{}, upstream, 0)
		for _, endpoint := range []struct {
			name, path string
			anthropic  bool
			call       func(*gin.Context)
		}{
			{"openai_responses", "/v1/responses", false, openai.Responses},
			{"openai_chat", "/v1/chat/completions", false, openai.ChatCompletions},
			{"openai_messages", "/v1/messages", true, openai.Messages},
			{"gateway_responses", "/v1/responses", false, gateway.Responses},
			{"gateway_chat", "/v1/chat/completions", false, gateway.ChatCompletions},
			{"gateway_messages", "/v1/messages", true, gateway.Messages},
			{"count_tokens", "/v1/messages/count_tokens", true, gateway.CountTokens},
		} {
			for _, failure := range []struct {
				name     string
				err      error
				status   int
				deadline bool
			}{
				{"timeout", uploadReadNetError{timeout: true}, 408, false},
				{"deadline", context.DeadlineExceeded, 408, true},
				{"transport", uploadReadNetError{}, 400, false},
				{"max_bytes", &http.MaxBytesError{Limit: 10}, 413, false},
				{"truncated", io.ErrUnexpectedEOF, 400, false},
				{"cancel", context.Canceled, 499, false},
			} {
				t.Run(endpoint.name+"/"+failure.name, func(t *testing.T) {
					ctx := context.Background()
					if failure.deadline {
						var cancel context.CancelFunc
						ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
						defer cancel()
					}
					c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
					key, _ := middleware.GetAPIKeyFromContext(c)
					key.Group.AllowMessagesDispatch = true
					c.Request.URL.Path = endpoint.path
					c.Request.Body = io.NopCloser(gwUsageReadError{err: failure.err})
					endpoint.call(c)
					require.Empty(t, upstream.calls(), "failed upload must never call upstream")
					require.Equal(t, failure.status, c.Writer.Status())
					if failure.status == 499 {
						require.Empty(t, rec.Body.String())
						require.False(t, c.Writer.Written())
						return
					}
					require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
					if failure.status == 408 {
						require.Equal(t, "Timed out reading request body", gjson.Get(rec.Body.String(), "error.message").String())
						if !endpoint.anthropic {
							require.Equal(t, "request_upload_timeout", gjson.Get(rec.Body.String(), "error.code").String())
						}
					}
					if endpoint.anthropic {
						require.Equal(t, "error", gjson.Get(rec.Body.String(), "type").String())
					}
				})
			}
		}
	})

}

func TestLogRequestBodyReadFailureClassifiesWithoutPayload(t *testing.T) {
	log, logs := newObservedLogger(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("secret-payload-marker"))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")
	req.ContentLength = 21

	logRequestBodyReadFailure(log, req, errors.New(`decode Content-Encoding "gzip": unexpected EOF secret-payload-marker`))

	entries := logs.All()
	require.Len(t, entries, 1)
	require.Equal(t, "read request body failed", entries[0].Message)
	fields := entries[0].ContextMap()
	require.Equal(t, "decode_content_encoding", fields["error_kind"])
	require.Equal(t, "gzip", fields["content_encoding"])
	require.EqualValues(t, 21, fields["content_length"])
	require.NotContains(t, entries[0].Message+fmt.Sprint(fields), "secret-payload-marker")
}

func TestRequestBodyReadErrorKind(t *testing.T) {
	require.Equal(t, "unsupported_content_encoding", requestBodyReadErrorKind(errors.New(`decode Content-Encoding "br": unsupported Content-Encoding`)))
	require.Equal(t, "truncated_body", requestBodyReadErrorKind(io.ErrUnexpectedEOF))
	require.Equal(t, "max_bytes", requestBodyReadErrorKind(&http.MaxBytesError{Limit: 10}))
	require.Equal(t, "other", requestContentEncodingCategory("private-payload-marker"))
}

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

	"github.com/stretchr/testify/require"
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

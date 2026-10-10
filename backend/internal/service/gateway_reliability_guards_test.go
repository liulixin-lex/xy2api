//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNonstreamReadErrorSafety(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"closed", net.ErrClosed, true},
		{"attempt_timeout", context.DeadlineExceeded, true},
		{"cancelled", context.Canceled, false},
		{"too_large", ErrUpstreamResponseBodyTooLarge, false},
		{"unknown", errors.New("invalid payload"), false},
		{"success", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nonstreamReadError(context.Background(), &http.Response{StatusCode: 200}, tc.err)
			var failover *UpstreamFailoverError
			require.Equal(t, tc.retry, errors.As(got, &failover))
			if tc.err != nil {
				require.ErrorIs(t, got, tc.err)
			} else {
				require.NoError(t, got)
			}
		})
	}
	for _, name := range []string{"cancelled", "unsafe", "request_error", "nil_response"} {
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := WithRequestReplaySafety(parent, []byte(`{"model":"m","stream":false}`), false)
			resp := &http.Response{StatusCode: 200}
			switch name {
			case "cancelled":
				cancel()
			case "unsafe":
				ctx = WithRequestReplaySafety(parent, []byte(`{"model":"m","tools":[{"type":"web_search"}]}`), false)
			case "request_error":
				resp.StatusCode = 400
			case "nil_response":
				resp = nil
			}
			require.Same(t, io.ErrUnexpectedEOF, nonstreamReadError(ctx, resp, io.ErrUnexpectedEOF))
		})
	}
}

func TestRequestBodyReplaySafety(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		gemini bool
		safe   bool
	}{
		{"plain", `{"model":"m","stream":true,"messages":[]}`, false, true},
		{"null_fields", `{"model":null,"stream":null,"tools":null}`, false, true},
		{"empty_tools", `{"tools":[]}`, false, true},
		{"function_tools", `{"tools":[{"type":"function","function":{"name":"f"}},{"type":"custom","name":"c"}]}`, false, true},
		{"server_tool", `{"tools":[{"type":"function"},{"type":"web_search_20250305"}]}`, false, false},
		{"untyped_tool", `{"tools":[{"name":"f"}]}`, false, false},
		{"tools_object", `{"tools":{"type":"function"}}`, false, false},
		{"gemini_tools", `{"contents":[],"tools":[{"functionDeclarations":[]}]}`, true, false},
		{"gemini_empty_tools", `{"contents":[],"tools":[]}`, true, true},
		{"bad_model", `{"model":5}`, false, false},
		{"bad_stream", `{"stream":"yes"}`, false, false},
		{"not_object", `[1,2]`, false, false},
		{"invalid", `{"model":`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.safe, requestBodyReplaySafe([]byte(tc.body), tc.gemini))
			ctx := WithRequestReplaySafety(context.Background(), []byte(tc.body), tc.gemini)
			require.Equal(t, !tc.safe, requestReplayUnsafe(ctx))
		})
	}
	require.False(t, requestReplayUnsafe(context.Background()), "requests without a recorded body stay replay-safe")
}

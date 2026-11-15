//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFoundationHTTPContinuationErrors(t *testing.T) {
	var absent *HTTPContinuationError
	if absent.Error() != "" {
		t.Fatal("nil error message")
	}
	for _, tc := range []struct {
		family                      resilience.StateFamily
		value, code, message, param string
		status                      int
	}{
		{resilience.StateContinuationOwnerMismatch, "continuation_owner_mismatch", "previous_response_owner_mismatch", "previous_response_id is not available for this user", "previous_response_id", 400},
		{resilience.StateHTTPContinuationUnsupported, "http_continuation_unsupported", "http_continuation_unsupported", "The selected upstream does not support previous_response_id over HTTP; send complete input history", "previous_response_id", 400},
		{resilience.StateToolOutputMissingCallID, "tool_output_missing_call_id", "tool_output_missing_call_id", "input[3] (custom_tool_call_output) requires a non-empty string call_id; only a named function_call_output may omit call_id or use null", "input[3].call_id", 400},
		{resilience.StateUpstreamContinuationExpired, "upstream_continuation_expired", "upstream_continuation_expired", "The upstream continuation state is no longer available; send complete input history", "previous_response_id", 410},
	} {
		if string(tc.family) != tc.value {
			t.Fatal(tc)
		}
		original := &HTTPContinuationError{Status: tc.status, Code: tc.code, Message: tc.message, Param: tc.param, Family: tc.family}
		var got *HTTPContinuationError
		if !errors.As(fmt.Errorf("wrapped: %w", original), &got) || got != original || got.Error() != tc.message || got.Param != tc.param || got.Status != tc.status || got.Code != tc.code || got.Family != tc.family {
			t.Fatal(got)
		}
	}
}

func TestNativeHTTPContinuationErrorClassification(t *testing.T) {
	for _, tc := range []struct{ name, payload, code string }{
		{"expired_code", `{"error":{"code":"previous_response_not_found","message":"not found"}}`, "upstream_continuation_expired"},
		{"expired_param", `{"error":{"param":"previous_response_id","message":"No response found with id resp_old"}}`, "upstream_continuation_expired"},
		{"unsupported_param", `{"error":{"code":"unsupported_parameter","param":"previous_response_id"}}`, "http_continuation_unsupported"},
		{"unsupported_message", `{"error":{"message":"previous_response_id is not supported over HTTP"}}`, "http_continuation_unsupported"},
		{"inband", `{"type":"response.failed","response":{"error":{"code":"previous_response_not_found"}}}`, "upstream_continuation_expired"},
		{"plain_404", `{"error":{"code":"not_found","message":"Not found"}}`, ""},
		{"plain_409", `{"error":{"code":"conflict","message":"Conflict"}}`, ""},
		{"unrelated_mention", `{"error":{"code":"model_not_found","message":"Model not found; previous_response_id was provided"}}`, ""},
		{"different_param", `{"error":{"code":"unsupported_parameter","param":"store","message":"previous_response_id is not supported"}}`, ""},
		{"vague_previous", `{"error":{"message":"Invalid previous_response_id"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejection := classifyHTTPContinuationError([]byte(tc.payload))
			if tc.code == "" {
				require.Nil(t, rejection)
				return
			}
			require.Equal(t, tc.code, rejection.Code)
			require.Equal(t, "previous_response_id", rejection.Param)
			req := newWebSearchReplayRequest(t)
			beginOpenAIStateRejectionAttempt(req.ctx, req.c, nil, nil)
			captureOpenAIStateRejection(req.c, nil, []byte(tc.payload), "", "native_http", false, nil)
			value, _ := req.c.Get(openAIStateRejectionAttemptKey)
			require.Equal(t, rejection.Family, value.(*openAIStateRejectionAttempt).rejection.Family)
		})
	}
	for _, store := range []string{"", `,"store":false`, `,"store":true`} {
		t.Run("store"+store, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.5","previous_response_id":"resp_parent","input":"hello"` + store + `}`)
			svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
			account := webSearchReplayAccount(false)
			c := adaptiveProtocolTestContext("/v1/responses", body)
			request, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "fixture-key", false, "", false)
			require.NoError(t, err)
			defer request.Body.Close()
			wire, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			require.Equal(t, "resp_parent", gjson.GetBytes(wire, "previous_response_id").String())
			require.Equal(t, gjson.GetBytes(body, "store").Raw, gjson.GetBytes(wire, "store").Raw)
		})
	}
}

// Red/blue review input: stateless DeepSeek/Kimi Responses targets keep the
// baseline normalization (pointer removed, store=false) and succeed.
func TestNativeHTTPContinuationCNTargetsKeepBaseline(t *testing.T) {
	for _, platform := range []string{PlatformDeepseek, PlatformKimi} {
		for _, store := range []string{"", `,"store":false`, `,"store":true`} {
			t.Run(platform+store, func(t *testing.T) {
				withResilienceSettings(t, nil)
				req := newWebSearchReplayRequest(t)
				body := []byte(`{"model":"deepseek-v4","input":"hello","previous_response_id":"resp_old","stream":false` + store + `}`)
				account := webSearchReplayAccount(false)
				account.Platform = platform
				account.Credentials["api_protocol"] = APIProtocolResponses
				up := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_ok","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))}}}
				svc := newOpenAIImageGenerationControlTestService(up)
				_, err := svc.Forward(req.ctx, req.c, account, body)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, req.rec.Code)
				require.Len(t, up.bodies, 1)
				require.False(t, gjson.GetBytes(up.bodies[0], "previous_response_id").Exists())
				require.Equal(t, "false", gjson.GetBytes(up.bodies[0], "store").Raw)
			})
		}
	}
}

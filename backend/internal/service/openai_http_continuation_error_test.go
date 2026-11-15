//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
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
			require.True(t, svc.SupportsOpenAIHTTPContinuation(context.Background(), account, "gpt-5.5"))
			account.Extra["openai_responses_supported"] = false
			require.False(t, svc.SupportsOpenAIHTTPContinuation(context.Background(), account, "gpt-5.5"))
			for _, platform := range []string{PlatformDeepseek, PlatformKimi} {
				account = &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_protocol": APIProtocolResponses, "api_key": "fixture-key"}}
				c = adaptiveProtocolTestContext("/v1/responses", body)
				request, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "fixture-key", false, "", false)
				var rejection *HTTPContinuationError
				require.ErrorAs(t, err, &rejection)
				require.Nil(t, request)
				require.Equal(t, "http_continuation_unsupported", rejection.Code)
				require.Equal(t, 400, c.Writer.Status())
				require.Equal(t, "resp_parent", gjson.GetBytes(body, "previous_response_id").String())
			}
		})
	}
}

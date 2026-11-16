//go:build unit

package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
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

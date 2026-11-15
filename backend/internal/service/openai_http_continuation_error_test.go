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
		family      resilience.StateFamily
		value, code string
		status      int
	}{
		{resilience.StateContinuationOwnerMismatch, "continuation_owner_mismatch", "previous_response_owner_mismatch", 400},
		{resilience.StateHTTPContinuationUnsupported, "http_continuation_unsupported", "http_continuation_unsupported", 400},
		{resilience.StateToolOutputMissingCallID, "tool_output_missing_call_id", "tool_output_missing_call_id", 400},
		{resilience.StateUpstreamContinuationExpired, "upstream_continuation_expired", "upstream_continuation_expired", 410},
	} {
		if string(tc.family) != tc.value {
			t.Fatal(tc)
		}
		original := &HTTPContinuationError{Status: tc.status, Code: tc.code, Message: "rejected", Param: "previous_response_id", Family: tc.family}
		var got *HTTPContinuationError
		if !errors.As(fmt.Errorf("wrapped: %w", original), &got) || got != original || got.Error() != "rejected" {
			t.Fatal(got)
		}
	}
}

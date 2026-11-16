//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Exercise M8's typed classifier with the evidence supplied by M9.
func TestSelectionExhaustionClassification(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		members, models, requests int
		known                     bool
		reasons                   map[service.SelectionReason]int
		cause                     error
		status                    int
		errType                   string
	}{
		{"empty", 0, 0, 0, true, map[service.SelectionReason]int{service.SelectionReasonNoMembers: 1}, nil, 503, "api_error"},
		{"model", 2, 0, 0, true, map[service.SelectionReason]int{service.SelectionReasonModelNotSupported: 2}, nil, 404, "model_not_found"},
		{"responses", 2, 2, 0, true, map[service.SelectionReason]int{service.SelectionReasonResponsesRequired: 2}, nil, 400, "invalid_request_error"},
		{"rate", 2, 2, 2, true, map[service.SelectionReason]int{service.SelectionReasonRateLimited: 2}, nil, 429, "rate_limit_error"},
		{"slots", 2, 2, 2, true, map[service.SelectionReason]int{service.SelectionReasonConcurrencyFull: 2}, nil, 429, "rate_limit_error"},
		{"mixed", 2, 2, 2, true, map[service.SelectionReason]int{service.SelectionReasonRateLimited: 1, service.SelectionReasonBalanceExhausted: 1}, nil, 503, "api_error"},
		{"stream_only", 2, 2, 0, true, map[service.SelectionReason]int{service.SelectionReasonCapabilityStreamOnly: 2}, nil, 503, "api_error"},
		{"owner", 2, 2, 2, true, map[service.SelectionReason]int{service.SelectionReasonOwnerFixed: 1}, nil, 503, "api_error"},
		{"profit", 2, 2, 2, true, map[service.SelectionReason]int{service.SelectionReasonProfitThreshold: 2}, nil, 503, "api_error"},
		{"dependency", 0, 0, 0, false, map[service.SelectionReason]int{service.SelectionReasonDependencyError: 1}, errors.New("repository unavailable"), 503, "api_error"},
		{"unknown", 0, 0, 0, false, nil, nil, 503, "api_error"},
		{"last_upstream", 0, 0, 0, true, map[service.SelectionReason]int{service.SelectionReasonNoMembers: 1}, &service.UpstreamFailoverError{StatusCode: 429, ResponseBody: []byte(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`)}, 429, "rate_limit_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, nil)
			e := &service.SelectionExhaustion{Known: tc.known, Members: tc.members, Eligible: tc.members, ModelSupported: tc.models, RequestSupported: tc.requests, Reasons: tc.reasons, LastCause: tc.cause}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := service.WithChatRequestRequirement(context.Background(), service.ChatRequestRequirement{OriginalModel: "gpt-6.1-sol", DispatchModel: "gpt-6.1-sol", HasTools: true, ToolField: "tools"})
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
			fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: tc.known, Members: tc.members, ModelSupported: tc.models, RequestSupported: tc.requests, HasAccountsInPool: tc.members > 0, HasModelSupport: tc.models > 0}}
			cls := classifyNoAccountErrorFromGin(c, fd, &service.APIKey{GroupID: ptrInt64(7)}, "gpt-6.1-sol", "gpt-6.1-sol", service.PlatformOpenAI, e)
			require.Equal(t, tc.status, cls.Status)
			require.Equal(t, tc.errType, cls.ErrType)
			if tc.name == "responses" {
				require.Contains(t, cls.Message, `"tools"`)
				require.Equal(t, "responses_required", cls.Code)
				require.Equal(t, "tools", cls.Param)
			}
			if tc.name == "stream_only" {
				require.True(t, cls.CapabilityOnly)
			}
			if tc.cause != nil {
				require.ErrorIs(t, e, tc.cause)
			}
		})
	}
}

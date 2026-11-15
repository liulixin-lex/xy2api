package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func writeM7RequestConversionError(c *gin.Context, protocol string, err error) {
	status, kind, param := http.StatusBadGateway, "server_error", ""
	var unsupported *apicompat.UnsupportedConversionError
	var syntax *json.SyntaxError
	var field *json.UnmarshalTypeError
	switch {
	case errors.As(err, &unsupported):
		status, kind, param = http.StatusBadRequest, "invalid_request_error", unsupported.Field
	case errors.As(err, &syntax):
		status, kind = http.StatusBadRequest, "invalid_request_error"
	case errors.As(err, &field):
		status, kind, param = http.StatusBadRequest, "invalid_request_error", field.Field
	}
	WriteTargetProtocolError(c, protocol, status, kind, "", param, err.Error())
}

// Keep unconverted extensions in the raw Responses-shaped request while applying
// the supplied final-wire preparation to its known stop and output-limit fields.
func prepareM7NativeResponsesBody(ctx context.Context, body []byte) ([]byte, error) {
	var req apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	prepared, err := apicompat.PrepareNativeResponsesRequest(req, m7ConversionOptions(ctx))
	if err != nil {
		return nil, err
	}
	if gjson.GetBytes(body, "stop").Exists() {
		body, err = sjson.DeleteBytes(body, "stop")
		if err != nil {
			return nil, err
		}
	}
	if prepared.MaxOutputTokens != nil && (req.MaxOutputTokens == nil || *prepared.MaxOutputTokens != *req.MaxOutputTokens) {
		body, err = sjson.SetBytes(body, "max_output_tokens", *prepared.MaxOutputTokens)
	}
	return body, err
}

// Apply the supplied policy only at an API-key leaf's resolved target. Legacy
// OAuth policy calls remain at their existing sites.
func applyM7FinalReasoning(ctx context.Context, c *gin.Context, account *Account, body []byte, model, wireProtocol, inboundProtocol string) ([]byte, error) {
	if account.Type != AccountTypeAPIKey {
		return body, nil
	}
	updated, _, err := applyFinalReasoningEffortForAccount(ctx, c, account, body, model, wireProtocol)
	if err != nil {
		if IsReasoningEffortPolicyDenied(err) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			WriteTargetProtocolError(c, inboundProtocol, http.StatusForbidden, "permission_error", "", "", err.Error())
		} else {
			writeM7RequestConversionError(c, inboundProtocol, err)
		}
	}
	return updated, err
}

// The returned completion runs after recursive repair and only marks a resend
// when the transport observed a new send boundary after this refusal.
func recordM7RejectedFieldReplay(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, payload, before, after []byte) func() {
	event := recordOpenAIResponsesRejectedFieldRetry(c, account, resp, payload)
	sequence := resilience.StateFrom(ctx).OpsUpstreamEventSequence()
	recordOpenAIRejectedOutputLimitOmission(ctx, before, after)
	return func() {
		if event != nil {
			if boundary, sent := resilience.StateFrom(ctx).OpsUpstreamSendBoundary(account.ID); sent && sequence > 0 && boundary >= sequence {
				event.Resent = true
			}
		}
	}
}

// Provider conversion may change effort after the pre-conversion validation.
// Reproject an explicitly configured group policy on the final body. M1 caches
// its mapping for this target, so this does not apply mappings a second time.
func finalizeM7ReasoningPolicy(ctx context.Context, c *gin.Context, account *Account, body []byte, model, wireProtocol, inboundProtocol string) ([]byte, error) {
	policy, _ := ctx.Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy)
	if policy.maxEffort == "" && len(policy.mappings) == 0 && c != nil && c.Request != nil {
		policy, _ = c.Request.Context().Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy)
	}
	if policy.maxEffort == "" && len(policy.mappings) == 0 {
		return body, nil
	}
	return applyM7FinalReasoning(ctx, c, account, body, model, wireProtocol, inboundProtocol)
}

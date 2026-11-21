package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const OpenAIConversionIdentityReason GatewayFailureReason = "openai_conversion_identity_conflict"

type openAIConversionRetryKey struct{}
type openAIConversionRetryState struct {
	openAIConversionWire
	used, resent, logged, replayOutput   bool
	protocolRejected, wireRejected       atomic.Bool
	failure                              *UpstreamFailoverError
	dispatchModel, model, param, message string
}

// WithOpenAIConversionRetry gives the Chat handler ownership of a single replay.
func WithOpenAIConversionRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAIConversionRetryKey{}, &openAIConversionRetryState{})
}

func openAIConversionRetryFrom(ctx context.Context) *openAIConversionRetryState {
	state, _ := ctx.Value(openAIConversionRetryKey{}).(*openAIConversionRetryState)
	return state
}

func openAIConversionRetryError(c *gin.Context, resp *http.Response, account *Account, model string, usage bool, param, message string, err error) *UpstreamFailoverError {
	var conversion *apicompat.UpstreamConversionError
	if !errors.As(err, &conversion) || conversion == nil || conversion.SourceProtocol != "responses" || conversion.Reason != "conflicting output identity" || c == nil || c.Request == nil {
		return nil
	}
	state := openAIConversionRetryFrom(c.Request.Context())
	if state == nil {
		return nil
	}
	if state.used {
		state.replayOutput = conversionClientOutputStarted(c)
		return nil
	}
	state.param, state.message = param, message
	state.failure = &UpstreamFailoverError{Reason: OpenAIConversionIdentityReason, StatusCode: resp.StatusCode,
		Scope: GatewayFailureScopeRequest, ClientStatusCode: http.StatusBadGateway,
		SafeToFailoverAfterWrite: true, cause: err, conversionRetry: state}
	// DispatchModel is the same channel-resolved input the Chat handler gives selection.
	requirement, _ := ChatRequestRequirementFromContext(c.Request.Context())
	state.dispatchModel = requirement.DispatchModel
	state.model = OpenAIAttemptModel(account, state.dispatchModel)
	if wireModel := gjson.GetBytes(state.wire, "model"); wireModel.Type == gjson.String && wireModel.Str != "" {
		model = wireModel.Str
	}
	reason := ""
	safe, marked := c.Request.Context().Value(requestReplaySafetyContextKey{}).(bool)
	budget := resilience.StateFrom(c.Request.Context()).Budget()
	protected, _ := c.Request.Context().Value(openAITransferProtectedKey{}).(bool)
	_, guardian := openAIGuardianParentAffinityFromContext(c.Request.Context())
	switch {
	case c.Request.Context().Err() != nil || GatewayDeliveryFailed(c):
		reason = "client_canceled"
	case conversionClientOutputStarted(c):
		reason = "output_started"
	case usage:
		reason = "usage_seen"
	case !marked:
		reason = "missing_replay_marker"
	case !safe:
		reason = "replay_marker_false"
	case protected || guardian || openAINoStickyMigration(c.Request.Context()) || openAIRejectionScope(c.Request.Context()).preserveBinding:
		reason = "state_continuation"
	case len(state.wire) == 0:
		reason = "missing_wire_body"
	case budget == nil:
		reason = "missing_budget"
	case !budget.CanAttempt(time.Now()) || budget.Attempts()+1 >= budget.MaxAttempts():
		reason = "budget_exhausted"
	default:
		reason = conversionReplayBodyReason(state.wire)
		if reason == "" && model != state.model {
			reason = "wire_model_mapped"
		}
	}
	if reason != "" {
		result := "not_retried"
		if reason == "client_canceled" {
			result = "canceled"
		}
		state.log(false, reason, result)
		return nil
	}
	return state.failure
}

func conversionClientOutputStarted(c *gin.Context) bool {
	if ProtocolTerminalClaimed(c) || IsResponseCommitted(c) {
		return true
	}
	if writer := deliveryWriterFrom(c); writer != nil {
		return writer.semanticBytes() > 0
	}
	return OpenAICompactKeepaliveAdjustedWrittenSize(c) > 0
}

// StartOpenAIConversionRetry consumes the original attempt without a session,
// health or avoidance failure. The existing budget is neither reset nor extended.
func (s *OpenAIGatewayService) StartOpenAIConversionRetry(c *gin.Context, account *Account, failure *UpstreamFailoverError) bool {
	state := failure.conversionRetry
	ctx := c.Request.Context()
	budget := resilience.StateFrom(ctx).Budget()
	reason := ""
	switch {
	case state == nil || state.used:
		reason = "replay_limit"
	case ctx.Err() != nil || GatewayDeliveryFailed(c):
		reason = "client_canceled"
	case conversionClientOutputStarted(c):
		reason = "output_started"
	case budget == nil || !budget.ConsumeInPlaceRetry(account.ID, false, time.Now()):
		reason = "budget_exhausted"
	}
	if reason != "" {
		WriteOpenAIConversionRetryFailure(c, failure, reason)
		return false
	}
	state.used = true
	state.replay = state.wire
	filter := func(candidate *Account) bool {
		if candidate == nil || candidate.Platform == PlatformGrok || s.resolveUpstreamProtocolFor(ctx, candidate, state.inbound, state.model) != APIProtocolResponses {
			state.protocolRejected.Store(true)
			return false
		}
		// Selection has already matched this candidate's OpenAIAttemptModel
		// to state.model. Chat also applies account mappings on passthrough rows.
		_, chatModel := resolveOpenAICompatForwardModels(candidate, state.dispatchModel, "")
		if chatModel != state.model || !s.matchesConversionWire(ctx, candidate, state) {
			state.wireRejected.Store(true)
			return false
		}
		return true
	}
	ctx = WithOpenAISelectionCandidateConstraint(ctx, account.Priority, state.model, account.ID, filter)
	c.Request = c.Request.WithContext(WithOpenAISelectionNoWait(ctx))
	return true
}

// WriteOpenAIConversionRetryFailure preserves the original conversion terminal
// when no replay can be dispatched; a canceled client gets no terminal frame.
func WriteOpenAIConversionRetryFailure(c *gin.Context, failure *UpstreamFailoverError, reason string) {
	state := failure.conversionRetry
	if state == nil {
		return
	}
	if c.Request.Context().Err() != nil || GatewayDeliveryFailed(c) {
		state.log(false, "client_canceled", "canceled")
		return
	}
	if reason == "no_candidate" {
		switch {
		case state.wireRejected.Load():
			reason = "candidate_wire"
		case state.protocolRejected.Load():
			reason = "candidate_protocol"
		}
	}
	writeUpstreamConversionError(c, &http.Response{StatusCode: failure.StatusCode}, "responses", PreOutputProtocolChat, "server_error", state.param, state.message, failure.cause)
	state.log(false, reason, "not_retried")
}

// CompleteOpenAIConversionRetry records the only replay's outcome after the
// forwarding attempt has finished, including failures after semantic output.
func CompleteOpenAIConversionRetry(c *gin.Context, result *OpenAIForwardResult, err error) {
	state := openAIConversionRetryFrom(c.Request.Context())
	if state == nil || !state.used || state.logged {
		return
	}
	outcome := "success"
	var conversion *apicompat.UpstreamConversionError
	switch {
	case c.Request.Context().Err() != nil || GatewayDeliveryFailed(c) || result != nil && result.ClientDisconnect:
		outcome = "canceled"
	case err != nil && (state.replayOutput || result != nil && result.FirstTokenMs != nil):
		outcome = "interrupted"
	case errors.As(err, &conversion):
		outcome = "conversion_error"
	case err != nil:
		outcome = "upstream_error"
	}
	reason := ""
	if !state.resent {
		reason = "prepare_failed"
		if outcome == "canceled" {
			reason = "client_canceled"
		}
	}
	state.log(state.resent, reason, outcome)
}

func (state *openAIConversionRetryState) log(retried bool, reason, result string) {
	if state.logged {
		return
	}
	state.logged = true
	fields := []zap.Field{zap.Bool("retried", retried), zap.String("skip_reason", reason), zap.String("result", result), zap.Int("upstream_status", state.failure.StatusCode)}
	var conversion *apicompat.UpstreamConversionError
	if errors.As(state.failure, &conversion) {
		diagnostic := conversion.Diagnostic()
		fields = append(fields, zap.String("source_protocol", diagnostic.SourceProtocol), zap.String("conversion_field", diagnostic.Field), zap.String("conversion_reason", diagnostic.Reason), zap.String("identity_conflict", diagnostic.IdentityConflict))
	}
	logger.L().Info("openai_conversion_retry", fields...)
}

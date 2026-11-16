package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"

	"github.com/liulixin-lex/xy2api/internal/service"
)

// inferenceNotFoundExhaustedMessage shares the existing no-model wording
// across inference protocols. The upstream 404 remains provider evidence;
// this is not a local group-configuration/business-limit classification.
func inferenceNotFoundExhaustedMessage(c *gin.Context, failure *service.UpstreamFailoverError) (string, bool) {
	if failure == nil || failure.NonInference || failure.StatusCode != http.StatusNotFound ||
		service.ClassifyFailoverError(failure).Category != resilience.CategoryModelUnsupported {
		return "", false
	}
	if c.GetBool(ginKeyResilienceFinalized) {
		return "", false
	}
	suppressResilienceRetryAdvice(c, "model_not_found")
	service.SetOpsUpstreamError(c, failure.StatusCode, service.ExtractUpstreamErrorMessage(failure.ResponseBody), "")
	return fmt.Sprintf("Model %q is not supported by any configured account in this group", exhaustedRetryModel(c)), true
}

// anthropicFailoverError projects recoverable capacity failures without the
// overload marker that makes Claude Code stop retrying early.
func anthropicFailoverError(status int, errType, message string, failure *service.UpstreamFailoverError) (int, string, string) {
	if !resilience.Current().ErrorsOn() {
		return status, errType, message
	}
	if (status == http.StatusTooManyRequests || status >= http.StatusInternalServerError) && service.ClassifyFailoverError(failure).Category == resilience.CategoryCapacityShed {
		message = strings.ReplaceAll(message, "overloaded_error", "api_error")
		if status >= http.StatusInternalServerError {
			return http.StatusServiceUnavailable, "api_error", message
		}
	}
	if status >= http.StatusInternalServerError {
		errType = "api_error"
	}
	return status, errType, message
}

func anthropicLocalErrorType(status int, errType string) string {
	if resilience.Current().ErrorsOn() && status >= http.StatusInternalServerError {
		switch errType {
		case "", "upstream_error", "server_error", "overloaded_error":
			return "api_error"
		}
	}
	return errType
}

// noAccountErrorClassification describes the HTTP response to emit when
// account selection failed with ErrNoAvailableAccounts. Handlers obtain it
// via classifyNoAccountError and choose between:
//
//   - 404 model_not_found — the group has accounts, but none of them are
//     configured to serve the requested model (config / typo / unsupported
//     model). Returning 503 here misleads operators and trips reverse-proxy
//     health checks; 404 lets the client surface the real problem.
//
//   - 503 api_error — accounts that could serve the model exist but are
//     temporarily exhausted (rate limit, quota auto-pause, runtime block) OR
//     the group has no accounts at all. Both stay on 503 because retrying
//     after a backoff can plausibly succeed (or, in the empty-pool case, the
//     operator may be in the middle of adding accounts).
type noAccountErrorClassification struct {
	Status         int
	Code           string
	Param          string
	LastCause      error
	ErrType        string
	Message        string
	ModelNotFound  bool // true when this is a 404 model_not_found classification
	CapabilityOnly bool // every candidate was excluded for non-streaming capability
}

// classifySelectionFailureError consumes actual selection evidence; diagnostic
// prose is never evidence that every candidate is temporarily rate limited.
func classifySelectionFailureError(err error, fallback noAccountErrorClassification) noAccountErrorClassification {
	if err == nil {
		return fallback
	}
	e, known := service.SelectionExhaustionFromError(err)
	if known && e.LastCause != nil {
		result := noAccountErrorClassification{Status: 503, ErrType: "api_error", Message: "Service temporarily unavailable", LastCause: e.LastCause}
		var failure *service.UpstreamFailoverError
		if errors.As(e.LastCause, &failure) {
			result.Status, result.ErrType, result.Message = (&OpenAIGatewayHandler{}).mapUpstreamError(failure.StatusCode)
			if failure.ClientStatusCode > 0 {
				result.Status = failure.ClientStatusCode
			}
			if failure.ClientMessage != "" {
				result.Message = failure.ClientMessage
			}
		}
		return result
	}
	if service.IsCapabilityOnlySelectionError(err) {
		return noAccountErrorClassification{Status: 503, ErrType: "api_error", Message: service.CapabilityOnlyMessage, CapabilityOnly: true}
	}
	if !known {
		return fallback
	}
	result := noAccountErrorClassification{Status: 503, ErrType: "api_error", Message: "Service temporarily unavailable"}
	if !e.Known {
		return result
	}
	persistentOnly, capabilityOnly, capacityOnly := true, true, true
	capacity, capabilities, streamOnly := 0, 0, 0
	for reason, count := range e.Reasons {
		if count <= 0 {
			continue
		}
		switch reason {
		case service.SelectionReasonNoMembers, service.SelectionReasonPlatformMismatch, service.SelectionReasonModelNotSupported, service.SelectionReasonAccountModelNotOwned:
		case service.SelectionReasonResponsesRequired, service.SelectionReasonCapabilityMismatch, service.SelectionReasonTransportIncompatible:
			persistentOnly = false
			capacityOnly = false
			capabilities += count
		case service.SelectionReasonCapabilityStreamOnly:
			persistentOnly = false
			capacityOnly = false
			streamOnly += count
		case service.SelectionReasonRateLimited, service.SelectionReasonModelRateLimited, service.SelectionReasonConcurrencyFull, service.SelectionReasonQueueFull, service.SelectionReasonQueueTimeout:
			persistentOnly = false
			capabilityOnly = false
			capacity += count
		default:
			persistentOnly = false
			capabilityOnly = false
			capacityOnly = false
		}
	}
	if persistentOnly && (e.Members == 0 || e.ModelSupported == 0 && len(e.Reasons) > 0) {
		result.Status, result.ErrType, result.Code, result.ModelNotFound = 404, "model_not_found", "model_not_found", true
		result.Message = "Model is not supported by any configured account in this group"
		if fallback.ModelNotFound {
			result.Message = fallback.Message
		}
		return result
	}
	if capabilityOnly && e.ModelSupported > 0 && e.RequestSupported == 0 {
		if streamOnly > 0 && capabilities == 0 {
			result.CapabilityOnly = true
			result.Message = service.CapabilityOnlyMessage
			return result
		}
		if capabilities > 0 && streamOnly == 0 {
			result.Status, result.ErrType = 400, "invalid_request_error"
			if e.Reasons[service.SelectionReasonResponsesRequired] > 0 {
				result.Code, result.Param = "responses_required", "tools"
				result.Message = `Request field "tools" requires a Responses-capable upstream for this model and reasoning_effort; no configured account can serve it.`
			} else {
				result.Message = "No configured account supports the required request capability"
			}
			return result
		}
	}
	if capacityOnly && capacity > 0 && e.RequestSupported > 0 && capacity >= e.RequestSupported {
		result.Status, result.ErrType = 429, "rate_limit_error"
		result.Message = "All available accounts are currently rate-limited. Please retry later."
	}
	return result
}

// classifyNoAccountError decides between 404 model_not_found and 503
// api_error for "no available accounts" failures.
//
// The classifier intentionally does not consume the original error: the
// selection layer never tells us *why* the pool came up empty (rate-limited
// vs. unsupported model are both wrapped as ErrNoAvailableAccounts). Instead
// we re-check pool composition through DiagnoseModelAvailabilityForPlatform.
// Its dedicated database query considers only persistent eligibility
// (active status + schedulable setting) and model_mapping, bypassing scheduler
// snapshots and transient filters. That guarantees a 404 is only returned
// when persistent account/group/model configuration must change before the
// request can succeed.
//
// routingModel is the model name that account selection actually compared
// against (i.e. after group-level dispatch mapping). displayModel is the
// raw model the caller asked for; it is used only in the user-facing error
// message so that internal mapping details don't leak. Most callers pass
// the same value for both.
//
// platform is the platform the request was routed to (use
// service.PlatformOpenAI / PlatformAnthropic / PlatformGemini). It is
// required because Anthropic/Gemini routes additionally surface
// mixed-scheduled Antigravity accounts; passing the wrong platform would
// flip a legitimate 503 to a misleading 404 (or vice versa).
func classifyNoAccountError(
	ctx context.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
	platform string,
) noAccountErrorClassification {
	fallback := noAccountErrorClassification{
		Status:  http.StatusServiceUnavailable,
		ErrType: "api_error",
		Message: "Service temporarily unavailable",
	}

	routingModel = strings.TrimSpace(routingModel)
	displayModel = strings.TrimSpace(displayModel)
	if displayModel == "" {
		displayModel = routingModel
	}
	if diag == nil || apiKey == nil || apiKey.GroupID == nil || routingModel == "" {
		return fallback
	}

	result := diag.DiagnoseModelAvailabilityForPlatform(ctx, apiKey.GroupID, routingModel, platform)
	missingModel := result.Known && (result.Members == 0 || result.ModelSupported == 0)
	// Legacy diagnosers report a successful scan through these two booleans.
	// Preserve their 404 until all producers also populate the typed counts.
	if !result.Known {
		missingModel = result.HasAccountsInPool && !result.HasModelSupport
	}
	if result.Err == nil && missingModel {
		return noAccountErrorClassification{
			Status:        http.StatusNotFound,
			ErrType:       "model_not_found",
			Message:       fmt.Sprintf("Model %q is not supported by any configured account in this group", displayModel),
			ModelNotFound: true,
		}
	}
	return fallback
}

// classifyNoAccountErrorFromGin is a thin wrapper that forwards the gin
// context's underlying request context. Most call sites already have a
// *gin.Context handy, so this keeps the call sites uncluttered.
func classifyNoAccountErrorFromGin(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
	platform string,
	selectionErrors ...error,
) noAccountErrorClassification {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	classification := noAccountErrorClassification{Status: 503, ErrType: "api_error", Message: "Service temporarily unavailable"}
	if len(selectionErrors) > 0 {
		if _, typed := service.SelectionExhaustionFromError(selectionErrors[0]); typed || service.IsCapabilityOnlySelectionError(selectionErrors[0]) {
			classification = classifySelectionFailureError(selectionErrors[0], classification)
		} else {
			classification = classifyNoAccountError(ctx, diag, apiKey, routingModel, displayModel, platform)
		}
	} else {
		classification = classifyNoAccountError(ctx, diag, apiKey, routingModel, displayModel, platform)
	}
	if classification.ModelNotFound {
		model := strings.TrimSpace(displayModel)
		if model == "" {
			model = strings.TrimSpace(routingModel)
		}
		classification.Message = fmt.Sprintf("Model %q is not supported by any configured account in this group", model)
	}
	if classification.Code == "responses_required" {
		if req, ok := service.ChatRequestRequirementFromContext(ctx); ok && req.ToolField == "functions" {
			classification.Param = "functions"
			classification.Message = strings.Replace(classification.Message, `"tools"`, `"functions"`, 1)
		}
	}

	if classification.ModelNotFound {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalModelConfiguration)
	}
	// xy2api resilience: Retry-After / x-should-retry (design §9.1).
	applyNoAccountRetryHints(c, diag, apiKey, routingModel, classification)
	return classification
}

// fallbackGateNoAccount answers service.GroupGateNoAccount like a no-account
// failure of the key's own group (PM decision P2-3): 503 with the no-account
// retry hints, never naming the availability fallback group.
func fallbackGateNoAccount(c *gin.Context, diag any, apiKey *service.APIKey, model string) noAccountErrorClassification {
	cls := noAccountErrorClassification{Status: http.StatusServiceUnavailable, ErrType: "api_error", Message: "Service temporarily unavailable"}
	if c != nil && c.Request != nil {
		apiKey = service.BillingAPIKey(c.Request.Context(), apiKey)
	}
	applyNoAccountRetryHints(c, diag, apiKey, model, cls)
	return cls
}

func classifyOpenAICompatibleNoAccountErrorFromGin(
	c *gin.Context,
	diag service.ModelAvailabilityDiagnoser,
	apiKey *service.APIKey,
	routingModel string,
	displayModel string,
	selectionErrors ...error,
) noAccountErrorClassification {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	return classifyNoAccountErrorFromGin(
		c,
		diag,
		apiKey,
		routingModel,
		displayModel,
		openAICompatibleRequestPlatform(ctx, apiKey),
		selectionErrors...,
	)
}

func openAICompatibleSelectionErrorForLog(err error, platform string) error {
	if err == nil || platform != service.PlatformGrok {
		return err
	}
	message := strings.ReplaceAll(err.Error(), "OpenAI accounts", "Grok accounts")
	if message == err.Error() {
		return err
	}
	return fmt.Errorf("%s", message)
}

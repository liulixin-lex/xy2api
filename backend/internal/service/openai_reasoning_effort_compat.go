package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type finalReasoningRequestKey struct{}
type finalReasoningRequest struct {
	original, field  string
	policy           openAIReasoningEffortPolicy
	policySet        bool
	accountID        int64
	model, applied   string
	appliedSet       bool
	noneOmitted      bool
	omittedAccountID int64
	omittedModel     string
}

// WithFinalReasoningEffortRequest freezes the inbound intention, including its
// absence. Bind before any converter can synthesize a default reasoning level.
func WithFinalReasoningEffortRequest(ctx context.Context, body []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(finalReasoningRequestKey{}).(*finalReasoningRequest); ok {
		return ctx
	}
	state := newFinalReasoningRequest(body)
	return context.WithValue(ctx, finalReasoningRequestKey{}, state)
}

func newFinalReasoningRequest(body []byte) *finalReasoningRequest {
	state := &finalReasoningRequest{}
	for _, path := range []string{"reasoning_effort", "reasoning.effort", "reasoning", "output_config.effort"} {
		field := gjson.GetBytes(body, path)
		if field.Type == gjson.String && strings.TrimSpace(field.String()) != "" {
			if (path == "reasoning" || path == "reasoning.effort") && gjson.GetBytes(body, "messages").Exists() && normalizeReasoningEffortMappingSource(field.String()) == "" {
				continue // Unknown Chat extensions were ignored before S-08.
			}
			state.original, state.field = strings.TrimSpace(field.String()), path
			break
		}
	}
	if state.original == "" && gjson.GetBytes(body, "thinking.type").String() == "disabled" {
		state.original, state.field = "none", "thinking.type"
	}
	if state.original == "" {
		if value := CanonicalRequestedReasoningEffort(body); value != nil {
			state.original = *value
		}
	}
	if canonical := normalizeReasoningEffortMappingSource(state.original); canonical != "" {
		state.original = canonical
	}
	return state
}

// supportedInferenceEffortsForAccount only reports a complete restriction when
// a provider validator/converter proves it. A nonempty catalog alone is not one.
func supportedInferenceEffortsForAccount(account *Account, model string) ([]string, bool) {
	var supported []string
	reliable := false
	switch {
	case openai.IsGPT61SolModelSpelling(model):
		supported, reliable = []string{"low", "medium", "high", "xhigh", "max"}, true
	case openai.IsGPT61TerraModelSpelling(model):
		supported, reliable = []string{"none", "low", "medium", "high", "xhigh", "max"}, true
	case claude.IsClaudeModel(model):
		supported = claude.EffortLevelsForModel(model)
		reliable = len(supported) > 0
		if reliable {
			supported = append([]string{"minimal"}, supported...) // Existing converters express minimal as adaptive/low, not disabled.
		}
		spec, _ := claude.Claude55(model)
		if claude.SupportsDisabledThinking(model) || spec.BetweenTools {
			supported = append([]string{"none"}, supported...)
		}
	case grokSupportsReasoningEffort(model):
		supported, reliable = []string{"none", "low", "medium", "high"}, true
		if GrokSupportsXHighReasoningEffort(model) {
			supported = append(supported, "xhigh")
		}
	case openai.IsGPT6SolOrLunaModelSpelling(model):
		supported = []string{"none", "low", "medium", "high", "xhigh", "max"}
	}
	if account != nil && !reliable {
		if metadata, ok := account.GetUpstreamModelMetadata(model); ok {
			for _, effort := range metadata.SupportedReasoningLevels {
				// Do not let a catalog override a known negative provider contract.
				if normalizeReasoningEffortMappingSource(effort) == effort && effort != "" && !slices.Contains(supported, effort) {
					supported = append(supported, effort)
				}
			}
		}
	}
	return supported, reliable
}

// applyFinalReasoningEffortForAccount runs at the HTTP leaf after resolving the
// final account/model. protocol describes the current body: responses/chat/anthropic.
// Reentry projects the saved group result onto the current repaired body only.
func applyFinalReasoningEffortForAccount(ctx context.Context, c *gin.Context, account *Account, body []byte, model, protocol string) ([]byte, bool, error) {
	if account == nil || account.Type != AccountTypeAPIKey {
		return ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
	}
	var state *finalReasoningRequest
	if ctx != nil {
		state, _ = ctx.Value(finalReasoningRequestKey{}).(*finalReasoningRequest)
	}
	if c != nil && c.Request != nil && c.Request.Context().Value(finalReasoningRequestKey{}) != nil {
		state, _ = c.Request.Context().Value(finalReasoningRequestKey{}).(*finalReasoningRequest)
	}
	if c != nil {
		if value, ok := c.Get("xy_final_reasoning_request"); ok {
			state, _ = value.(*finalReasoningRequest)
		}
	}
	if state == nil {
		state = newFinalReasoningRequest(body)
	}
	if c != nil {
		c.Set("xy_final_reasoning_request", state)
	}
	if state.noneOmitted && state.omittedAccountID == account.ID && state.omittedModel == model {
		return body, false, nil
	}
	if state.original == "" || (protocol == "anthropic" && state.field == "thinking.type" && !claude.IsClaudeModel(model)) {
		return body, false, nil
	}
	if !state.policySet {
		if ctx != nil && state.field != "" {
			state.policy, _ = ctx.Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy)
		}
		if state.field != "" && c != nil && c.Request != nil && state.policy.maxEffort == "" && len(state.policy.mappings) == 0 {
			state.policy, _ = c.Request.Context().Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy)
		}
		state.policy.mappings = append([]ReasoningEffortMapping(nil), state.policy.mappings...)
		state.policySet = true
	}
	if !state.appliedSet || state.accountID != account.ID || state.model != model {
		input, err := json.Marshal(map[string]any{"model": model, "reasoning_effort": state.original})
		if err != nil {
			return nil, false, err
		}
		mapped, _, err := ApplyReasoningEffortPolicy(input, state.policy.maxEffort, state.policy.mappings, state.policy.overLimit)
		if err != nil {
			return body, false, err
		}
		state.applied = gjson.GetBytes(mapped, "reasoning_effort").String()
		state.accountID, state.model, state.appliedSet = account.ID, model, true
	}
	effort := state.applied
	if grokSupportsReasoningEffort(model) {
		if normalized, ok := normalizeGrokReasoningEffortValue(effort, model); ok {
			effort = normalized
		}
	}
	supported, reliable := supportedInferenceEffortsForAccount(account, model)
	if protocol == "responses" && state.field != "" && !reliable && effort == "minimal" {
		effort = "none" // Preserve the existing Responses compatibility normalization on reentry too.
	}
	if reliable && normalizeReasoningEffortMappingSource(effort) != "" {
		if maxRank, hasMax := reasoningEffortRank(state.policy.maxEffort); hasMax {
			candidates := make([]string, 0, len(supported))
			for _, value := range supported {
				rank, _ := reasoningEffortRank(value)
				if rank <= maxRank {
					candidates = append(candidates, value)
				}
			}
			if len(candidates) == 0 {
				return body, false, &ReasoningEffortOverLimitError{Requested: effort, Max: state.policy.maxEffort}
			}
			supported = candidates
		}
		var err error
		effort, err = openai.NearestSupportedReasoningEffort(effort, supported)
		if err != nil {
			return body, false, err
		}
	}
	if state.field == "" && effort == state.original {
		return body, false, nil // Suffixes are requested metadata, not explicit wire fields.
	}
	out, err := projectFinalReasoningEffort(body, model, protocol, effort, state.original != "none" && effort != "none")
	if err != nil {
		return body, false, err
	}
	if effort != state.original {
		field := state.field
		if field == "" {
			field = "reasoning.effort"
		}
		resilience.RecordConversionNotices(ctx, apicompat.ConversionNotice{Field: field, Action: "map"})
	}
	return out, !bytes.Equal(out, body), nil
}

func projectFinalReasoningEffort(body []byte, model, protocol, effort string, preserveAnthropicMode bool) ([]byte, error) {
	switch protocol {
	case "responses", "chat":
		path := "reasoning.effort"
		if protocol == "chat" {
			path = "reasoning_effort"
		}
		out, err := sjson.SetBytes(body, path, effort)
		if err != nil {
			return nil, err
		}
		// Keep an already-present compatibility alias consistent for validators.
		for _, alias := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
			if alias != path && gjson.GetBytes(out, alias).Exists() {
				out, err = sjson.SetBytes(out, alias, effort)
				if err != nil {
					return nil, err
				}
			}
		}
		if gjson.GetBytes(out, "thinking.type").String() == "disabled" && effort != "none" {
			out, err = sjson.DeleteBytes(out, "thinking")
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case "anthropic":
		if preserveAnthropicMode || !claude.IsClaudeModel(model) {
			return sjson.SetBytes(body, "output_config.effort", effort)
		}
		req := &apicompat.ResponsesRequest{Model: model, Input: json.RawMessage(`""`), Reasoning: &apicompat.ResponsesReasoning{Effort: effort}}
		if limit := gjson.GetBytes(body, "max_tokens"); limit.Exists() && limit.Type == gjson.Number {
			value := int(limit.Int())
			req.MaxOutputTokens = &value
		}
		projected, err := apicompat.ResponsesToAnthropicRequest(req)
		if err != nil {
			return nil, err
		}
		out := body
		if projected.Thinking != nil {
			out, err = sjson.SetBytes(out, "thinking", projected.Thinking)
		} else {
			out, err = sjson.DeleteBytes(out, "thinking")
		}
		if err != nil {
			return nil, err
		}
		if projected.OutputConfig != nil && projected.OutputConfig.Effort != "" {
			return sjson.SetBytes(out, "output_config.effort", projected.OutputConfig.Effort)
		}
		return sjson.DeleteBytes(out, "output_config.effort")
	default:
		return nil, fmt.Errorf("unsupported reasoning body protocol %q", protocol)
	}
}

func writeFinalReasoningPolicyError(c *gin.Context, protocol string, err error) error {
	if c == nil {
		return err
	}
	status, typ := 400, "invalid_request_error"
	if IsReasoningEffortPolicyDenied(err) {
		status, typ = 403, "permission_error"
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	}
	payload := gin.H{"error": gin.H{"type": typ, "message": err.Error()}}
	if protocol == "anthropic" {
		payload["type"] = "error"
	}
	c.JSON(status, payload)
	return err
}

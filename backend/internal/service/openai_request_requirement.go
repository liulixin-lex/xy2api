package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// ChatRequestRequirement preserves the original request intent for selection.
type ChatRequestRequirement struct {
	OriginalModel, DispatchModel, ReasoningEffort string
	HasTools                                      bool
	ToolField                                     string
}

type chatRequestRequirementKey struct{}

func WithChatRequestRequirement(ctx context.Context, req ChatRequestRequirement) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, chatRequestRequirementKey{}, req)
}

func ChatRequestRequirementFromContext(ctx context.Context) (ChatRequestRequirement, bool) {
	if ctx == nil {
		return ChatRequestRequirement{}, false
	}
	req, ok := ctx.Value(chatRequestRequirementKey{}).(ChatRequestRequirement)
	return req, ok
}

type openAISelectionNoWaitKey struct{}

func WithOpenAISelectionNoWait(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAISelectionNoWaitKey{}, true)
}

func openAISelectionNoWait(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	noWait, _ := ctx.Value(openAISelectionNoWaitKey{}).(bool)
	return noWait
}

// OpenAIQueueReselectionAllowed preserves the existing hard owner/migration
// guards when the HTTP admission path considers one local free-slot retry.
func OpenAIQueueReselectionAllowed(ctx context.Context) bool {
	return ctx != nil && !openAINoStickyMigration(ctx) && !openAISelectionExempt(ctx)
}

// The scheduling recheck may be capped; the provider's original deadline is not.
func accountRetryNotBeforeBlocked(ctx context.Context, accountID int64) bool {
	hint, ok := resilience.StateFrom(ctx).AccountRetryNotBefore(accountID)
	return ok && time.Now().Before(hint.NotBefore)
}

type chatRequestInboundKey struct{}

// WithChatRequestRequirementFromBody binds intent before selection and its cache.
// The public requirement stays a value; the private shape only selects the same
// inbound protocol that ForwardAsChatCompletions uses for adaptive accounts.
func WithChatRequestRequirementFromBody(ctx context.Context, body []byte, dispatchModel string) context.Context {
	req := ChatRequestRequirement{OriginalModel: gjson.GetBytes(body, "model").String(), DispatchModel: dispatchModel}
	if len(gjson.GetBytes(body, "tools").Array()) > 0 {
		req.HasTools, req.ToolField = true, "tools"
	} else if len(gjson.GetBytes(body, "functions").Array()) > 0 {
		req.HasTools, req.ToolField = true, "functions"
	}
	// Preserve M1's distinction between explicit wire fields and derived intent.
	// An unchanged thinking mode or model suffix does not write a wire effort.
	effort := newFinalReasoningRequest(body)
	if effort.field != "" && effort.field != "thinking.type" {
		req.ReasoningEffort = effort.original
	}
	ctx = WithChatRequestRequirement(ctx, req)
	inbound := APIProtocolChatCompletions
	if !gjson.GetBytes(body, "messages").Exists() && gjson.GetBytes(body, "input").Exists() {
		inbound = APIProtocolResponses
	}
	return context.WithValue(ctx, chatRequestInboundKey{}, inbound)
}

func ChatRequiresResponses(finalModel string, hasTools bool, effort string) bool {
	return hasTools && (openai.RequiresResponsesForToolCalls(finalModel) ||
		openai.IsGPT6SolOrLunaModelSpelling(finalModel) && !strings.EqualFold(strings.TrimSpace(effort), "none"))
}

func chatRequestHasTools(ctx context.Context) bool {
	req, ok := ChatRequestRequirementFromContext(ctx)
	return ok && req.HasTools
}

func (s *OpenAIGatewayService) chatRequestCapabilityFailureReason(ctx context.Context, account *Account) string {
	req, ok := ChatRequestRequirementFromContext(ctx)
	if !ok || !req.HasTools || account == nil {
		return ""
	}
	model := req.DispatchModel
	if model == "" {
		model = req.OriginalModel
	}
	finalModel := normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, model, ""))
	// Use M1's policy on an isolated candidate state. Selection must not mutate
	// the cached final wire policy or emit conversion notices for rejected accounts.
	body, _ := json.Marshal(map[string]any{"model": finalModel, "reasoning_effort": req.ReasoningEffort})
	state := newFinalReasoningRequest(body)
	if original, _ := ctx.Value(finalReasoningRequestKey{}).(*finalReasoningRequest); original != nil {
		clone := *original
		clone.policy.mappings = append([]ReasoningEffortMapping(nil), original.policy.mappings...)
		state = &clone
	}
	candidateCtx := context.WithValue(ctx, finalReasoningRequestKey{}, state)
	candidateCtx = resilience.WithRequestState(candidateCtx, resilience.NewRequestState())
	mapped, _, err := applyFinalReasoningEffortForAccount(candidateCtx, nil, account, body, finalModel, "chat")
	if err != nil {
		return ""
	} // Keep the existing policy-denial response at the leaf.
	effort := gjson.GetBytes(mapped, "reasoning_effort").String()
	if !ChatRequiresResponses(finalModel, req.HasTools, effort) {
		return ""
	}
	inbound, _ := ctx.Value(chatRequestInboundKey{}).(string)
	if inbound == "" {
		inbound = APIProtocolChatCompletions
	}
	routingModel := ""
	if account.routesByModel() {
		routingModel = finalModel
	}
	endpointCapable := account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses)
	if account.routesByModel() || account.RoutesProtocolByInbound() {
		// These routers deliberately give explicit protocol configuration priority
		// over asynchronous Responses probes, just as the actual forwarder does.
		endpointCapable = account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions)
	}
	if !endpointCapable || s.resolveUpstreamProtocolFor(ctx, account, inbound, routingModel) != APIProtocolResponses {
		return string(SelectionReasonResponsesRequired)
	}
	return ""
}

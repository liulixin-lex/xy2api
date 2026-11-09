package service

import (
	"bytes"
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	codexAutoReviewModel      = "codex-auto-review"
	openAISubagentHeader      = "x-openai-subagent"
	codexParentThreadIDHeader = "x-codex-parent-thread-id"
	codexTurnMetadataHeader   = "x-codex-turn-metadata"
)

type openAIGuardianParentAffinityContextKey struct{}

type openAIGuardianParentAffinity struct {
	currentSessionHash string
	legacySessionHash  string
}

// WithOpenAIGuardianParentAffinity records parent-thread affinity for Codex
// sub-agents: automatic reviews and child threads carrying encrypted agent_message.
// The hint is resolved against the current group's sticky-session namespace
// later; client headers never carry an account ID.
func (s *OpenAIGatewayService) WithOpenAIGuardianParentAffinity(ctx context.Context, c *gin.Context, body []byte, requestedModel string) context.Context {
	ctx = WithOpenAITerminalRejectionScope(ctx, c, body)
	if ctx == nil || c == nil {
		return ctx
	}

	encryptedAgentMessage := openAIRequestHasEncryptedAgentMessage(body)
	reviewModel := strings.EqualFold(strings.TrimSpace(requestedModel), codexAutoReviewModel)
	if !reviewModel && !encryptedAgentMessage {
		return ctx
	}
	headerMetadata := c.GetHeader(codexTurnMetadataHeader)
	payload := openAIRequestPayloadView(body)
	bodyMetadata := payload.Get("client_metadata.x-codex-turn-metadata").String()
	review := reviewModel && hasUnambiguousOpenAICodexReviewSubagent(
		c.GetHeader(openAISubagentHeader),
		codexSubagentKindFromMetadata(headerMetadata),
		codexSubagentKindFromMetadata(bodyMetadata),
	)
	if !review && !encryptedAgentMessage {
		return ctx
	}

	parentID := ""
	for _, candidate := range []string{
		strings.TrimSpace(c.GetHeader(codexParentThreadIDHeader)),
		codexParentThreadIDFromMetadata(headerMetadata),
		codexParentThreadIDFromMetadata(bodyMetadata),
		strings.TrimSpace(payload.Get("client_metadata.x-codex-parent-thread-id").String()),
	} {
		if candidate == "" {
			continue
		}
		if parentID != "" && parentID != candidate {
			return ctx
		}
		parentID = candidate
	}
	if parentID == "" {
		return ctx
	}

	currentHash, legacyHash := s.openAISessionHashesForSeed(c, body, parentID)
	if currentHash == "" {
		return ctx
	}
	return context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{
		currentSessionHash: currentHash,
		legacySessionHash:  legacyHash,
	})
}

func openAIRequestHasEncryptedAgentMessage(body []byte) bool {
	if !bytes.Contains(body, []byte(`"agent_message"`)) {
		return false
	}
	for _, item := range openAIRequestPayloadView(body).Get("input").Array() {
		if item.Get("type").String() != "agent_message" {
			continue
		}
		if openAIContentHasEncryptedPart(item.Get("content")) {
			return true
		}
	}
	return false
}

func openAIContentHasEncryptedPart(content gjson.Result) bool {
	for _, part := range content.Array() {
		if part.Get("type").String() == "encrypted_content" {
			return true
		}
	}
	return false
}

// WithOpenAIEncryptedContentMigrationGuard keeps issuer-bound collaboration and
// tool output on their sticky owner. Hard admission and failure rebinds still apply.
func WithOpenAIEncryptedContentMigrationGuard(ctx context.Context, body []byte) context.Context {
	if openAIRequestHasEncryptedState(body) {
		return withOpenAINoStickyMigration(ctx)
	}
	return ctx
}

func openAIRequestHasEncryptedState(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	input := openAIRequestPayloadView(body).Get("input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		var content gjson.Result
		switch item.Get("type").String() {
		case "agent_message":
			content = item.Get("content")
		case "function_call_output":
			content = item.Get("output")
		default:
			continue
		}
		if content.IsArray() && openAIContentHasEncryptedPart(content) {
			return true
		}
	}
	return false
}

func codexParentThreadIDFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "parent_thread_id").String())
}

func codexSubagentKindFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "subagent_kind").String())
}

func hasUnambiguousOpenAICodexReviewSubagent(candidates ...string) bool {
	subagent := ""
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if subagent != "" && subagent != candidate {
			return false
		}
		subagent = candidate
	}
	return subagent == "guardian" || subagent == "review"
}

func openAIGuardianParentAffinityFromContext(ctx context.Context) (openAIGuardianParentAffinity, bool) {
	if ctx == nil {
		return openAIGuardianParentAffinity{}, false
	}
	affinity, ok := ctx.Value(openAIGuardianParentAffinityContextKey{}).(openAIGuardianParentAffinity)
	return affinity, ok && affinity.currentSessionHash != ""
}

func preserveOpenAIGuardianParentBinding(ctx context.Context, sessionHash string) bool {
	if scope := openAIRejectionScope(ctx); scope.preserveBinding && sessionHash != "" && sessionHash != scope.session {
		return true
	}
	affinity, ok := openAIGuardianParentAffinityFromContext(ctx)
	if !ok {
		return false
	}
	sessionHash = strings.TrimSpace(sessionHash)
	return sessionHash != "" && (sessionHash == affinity.currentSessionHash || sessionHash == affinity.legacySessionHash)
}

func (s *OpenAIGatewayService) resolveOpenAIGuardianParentAccountID(ctx context.Context, groupID *int64) int64 {
	if s == nil || s.cache == nil {
		return 0
	}
	affinity, ok := openAIGuardianParentAffinityFromContext(ctx)
	if !ok {
		return 0
	}
	lookupCtx := withOpenAILegacySessionHash(ctx, affinity.legacySessionHash)
	accountID, err := s.getStickySessionAccountID(lookupCtx, groupID, affinity.currentSessionHash)
	if err != nil || accountID <= 0 {
		return 0
	}
	return accountID
}

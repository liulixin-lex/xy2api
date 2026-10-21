package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const (
	capabilityLearnedResponsesKey = "capability_learned_responses"
	capabilityLearnedChatKey      = "capability_learned_chat"
	// CapabilityManualExtraKey is reserved for separately managed overrides.
	// This release neither writes nor interprets manual capability settings.
	CapabilityManualExtraKey = "capability_manual"
)

func capabilityExtraKey(protocol string) string {
	switch protocol {
	case resilience.CapabilityProtocolResponses:
		return capabilityLearnedResponsesKey
	case resilience.CapabilityProtocolChat:
		return capabilityLearnedChatKey
	}
	return ""
}

func normalizeCapabilityProtocol(protocol string) string {
	if protocol == APIProtocolChatCompletions {
		return resilience.CapabilityProtocolChat
	}
	return protocol
}

func capabilityAccountEligible(account *Account) bool {
	return account != nil && (account.Platform == PlatformOpenAI || account.Platform == PlatformGrok) &&
		!(account.Platform == PlatformOpenAI && account.IsOpenAIOAuthLike())
}

// capabilityFingerprint binds observations to the upstream identity that
// produced them. A late failure after an administrator edits credentials
// cannot make the new configuration ineligible. Only the digest is stored.
func capabilityFingerprint(account *Account) string {
	if account == nil {
		return ""
	}
	raw, err := json.Marshal(struct {
		Platform, Type string
		Credentials    map[string]any
	}{account.Platform, account.Type, account.Credentials})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// CapabilityLearned returns active observations regardless of feature gates,
// so administrators can inspect and clear them while learning is disabled.
func (a *Account) CapabilityLearned(now time.Time) []resilience.LearnedCapability {
	result := []resilience.LearnedCapability{}
	if !capabilityAccountEligible(a) {
		return result
	}
	fingerprint := capabilityFingerprint(a)
	if fingerprint == "" {
		return result
	}
	for _, protocol := range []string{resilience.CapabilityProtocolResponses, resilience.CapabilityProtocolChat} {
		record, ok := a.Extra[capabilityExtraKey(protocol)].(map[string]any)
		if !ok || record["fingerprint"] != fingerprint {
			continue
		}
		text, _ := record["expires_at"].(string)
		expiry, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || !expiry.After(now) {
			continue
		}
		result = append(result, resilience.LearnedCapability{Protocol: protocol, Mode: resilience.CapabilityModeStreamOnly, ExpiresAt: expiry})
	}
	return result
}

// CapabilityExclusion returns the active observation for the actual upstream
// protocol the candidate would use. Forced-stream OpenAI OAuth is excluded.
func (a *Account) CapabilityExclusion(ctx context.Context, model string, now time.Time) *resilience.LearnedCapability {
	if !resilience.Current().StreamOnlyLearningOn() || !capabilityAccountEligible(a) {
		return nil
	}
	protocol, stream, ok := resilience.CapabilityRequestFrom(ctx)
	if !ok || stream {
		return nil
	}
	if a.Platform == PlatformOpenAI {
		inbound := APIProtocolResponses
		if protocol == resilience.CapabilityProtocolChat {
			inbound = APIProtocolChatCompletions
		}
		protocol = normalizeCapabilityProtocol(resolveUpstreamProtocol(a, inbound, a.GetMappedModel(model), nil))
	}
	for _, record := range a.CapabilityLearned(now) {
		if record.Protocol == protocol {
			return &record
		}
	}
	return nil
}

func capabilityEligibilityReason(ctx context.Context, account *Account, model string, now time.Time) string {
	if account.CapabilityExclusion(ctx, model, now) != nil {
		resilience.NoteCapabilityExcluded(ctx, account.ID)
		return resilience.ReasonCapabilityStreamOnly
	}
	return ""
}

// LearnStreamOnlyFailure observes a deterministic upstream 400 before any
// side effects or client output. Learning is independent of immediate retry.
func (s *OpenAIGatewayService) LearnStreamOnlyFailure(ctx context.Context, account *Account, protocol string, upstreamStream bool, status int, body []byte) *UpstreamFailoverError {
	cfg := resilience.Current()
	protocol = normalizeCapabilityProtocol(protocol)
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 || !capabilityAccountEligible(account) ||
		upstreamStream || capabilityExtraKey(protocol) == "" || !cfg.StreamOnlyLearningOn() || !resilience.IsStreamOnlyFailure(status, body) {
		return nil
	}
	if _, stream, known := resilience.CapabilityRequestFrom(ctx); known && stream {
		return nil
	}
	fingerprint := capabilityFingerprint(account)
	if fingerprint == "" {
		return nil
	}
	expires := time.Now().Add(time.Duration(cfg.Capability.LearnTTLSeconds) * time.Second).UTC()
	record := map[string]any{"expires_at": expires.Format(time.RFC3339Nano), "fingerprint": fingerprint}
	// UpdateExtra is an atomic JSONB key merge and refreshes scheduler snapshots;
	// separate protocol keys prevent concurrent chat/responses observations from
	// replacing each other. Never mutate the shared Account supplied by selection.
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(stateCtx, account.ID, map[string]any{capabilityExtraKey(protocol): record}); err != nil {
		slog.Warn("resilience.capability_learning_failed", "account_id", account.ID, "protocol", protocol, "error", err)
	} else {
		s.resilience.invalidateAccountGroups(account)
		slog.Info("resilience.capability_learned", "account_id", account.ID, "protocol", protocol, "expires_at", expires)
	}
	if !cfg.StreamOnlyFailoverOn() {
		return nil
	}
	resilience.NoteCapabilityExcluded(ctx, account.ID)
	return &UpstreamFailoverError{
		StatusCode: status, ResponseBody: body,
		Scope: GatewayFailureScopeRequest, Reason: GatewayFailureReason(resilience.ReasonCapabilityStreamOnly),
		NextAccountAction: NextAccountRetry,
		ClientStatusCode:  503, ClientMessage: "No available account supports this non-streaming request",
	}
}

func capabilitySelectionError(ctx context.Context, err error) error {
	if isNoAccountSelectionError(err) && resilience.CapabilityExcluded(ctx, 0) &&
		!strings.Contains(err.Error(), resilience.ReasonCapabilityStreamOnly) {
		return fmt.Errorf("%w (%s)", err, resilience.ReasonCapabilityStreamOnly)
	}
	return err
}

func (e *UpstreamFailoverError) IsCapabilityStreamOnly() bool {
	return e != nil && e.Reason == GatewayFailureReason(resilience.ReasonCapabilityStreamOnly)
}

func (s *adminServiceImpl) ClearAccountCapabilityLearning(ctx context.Context, id int64) error {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err = s.accountRepo.UpdateExtra(ctx, id, map[string]any{capabilityLearnedResponsesKey: nil, capabilityLearnedChatKey: nil}); err != nil {
		return err
	}
	Resilience().invalidateAccountGroups(account)
	return nil
}

// MergeCapabilityLearningExtra preserves managed observations from the latest
// locked database row, never from a client edit or copied/imported account.
func MergeCapabilityLearningExtra(next, current map[string]any) map[string]any {
	result := maps.Clone(next)
	for _, key := range []string{capabilityLearnedResponsesKey, capabilityLearnedChatKey} {
		delete(result, key)
		if value, ok := current[key]; ok {
			if result == nil {
				result = map[string]any{}
			}
			result[key] = value
		}
	}
	return result
}

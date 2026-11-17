package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	capabilityLearnedResponsesKey = "capability_learned_responses"
	capabilityLearnedChatKey      = "capability_learned_chat"
	// CapabilityManualExtraKey is reserved for separately managed overrides.
	// This release neither writes nor interprets manual capability settings.
	CapabilityManualExtraKey = "capability_manual"
	CapabilityOnlyMessage    = `No available account in this group supports non-streaming requests; retry with "stream": true`
	CapabilityMarksExtraKey  = resilience.CapabilityMarksExtraKey
	CapabilityMarkTTL        = resilience.CapabilityMarkTTL
	CapabilityMarkLimit      = 32
)

type CapabilityMark = resilience.CapabilityMark

// A separate top-level key per tuple lets UpdateExtra merge concurrent learns.
func capabilityMarkExtraKey(model, protocol, shape string) string {
	raw, _ := json.Marshal([3]string{strings.TrimSpace(model), normalizeCapabilityProtocol(protocol), shape})
	digest := sha256.Sum256(raw)
	return CapabilityMarksExtraKey + "_" + hex.EncodeToString(digest[:])
}

func validCapabilityMark(m CapabilityMark) bool {
	return m.Model != "" && (m.Protocol == resilience.CapabilityProtocolResponses || m.Protocol == resilience.CapabilityProtocolChat) &&
		(m.Shape == "stream" || m.Shape == "non_stream" || m.Shape == "*") &&
		(m.Reason == "model_unsupported" || m.Reason == "request_unsupported")
}

func capabilityMarkFromExtra(value any) *CapabilityMark {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var mark CapabilityMark
	if json.Unmarshal(raw, &mark) != nil || !validCapabilityMark(mark) {
		return nil
	}
	return &mark
}

func (a *Account) CapabilityMarked(model, protocol, shape string, now time.Time) *CapabilityMark {
	if a == nil {
		return nil
	}
	model, protocol = strings.TrimSpace(model), normalizeCapabilityProtocol(protocol)
	fingerprint := ""
	for _, candidate := range []string{shape, "*"} {
		value := a.Extra[capabilityMarkExtraKey(model, protocol, candidate)]
		if value == nil {
			continue
		}
		mark := capabilityMarkFromExtra(value)
		if mark == nil || mark.Model != model || mark.Protocol != protocol || mark.Shape != candidate || !mark.ExpiresAt.After(now) {
			continue
		}
		if fingerprint == "" {
			fingerprint = capabilityFingerprint(a)
		}
		if fingerprint != "" && mark.Fingerprint == fingerprint {
			return mark
		}
	}
	return nil
}

func (s *OpenAIGatewayService) LearnCapabilityMark(ctx context.Context, account *Account, mark CapabilityMark) error {
	mark.Model, mark.Protocol = strings.TrimSpace(mark.Model), normalizeCapabilityProtocol(mark.Protocol)
	if mark.Reason == "model_unsupported" {
		mark.Shape = "*"
	}
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 || !validCapabilityMark(mark) {
		return errors.New("invalid capability mark or account")
	}
	if account.routesByModel() {
		return nil
	}
	mark.Fingerprint = capabilityFingerprint(account)
	if mark.Fingerprint == "" {
		return errors.New("cannot fingerprint capability account")
	}
	mark.ExpiresAt = time.Now().UTC().Add(CapabilityMarkTTL)
	raw, err := json.Marshal(mark)
	if err != nil {
		return err
	}
	var record map[string]any
	if err = json.Unmarshal(raw, &record); err != nil {
		return err
	}
	// Numeric expiry also allows atomic database pruning without parsing arbitrary
	// administrator-supplied strings as PostgreSQL timestamps.
	record["expires_at_unix"] = float64(mark.ExpiresAt.UnixNano()) / float64(time.Second)
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if err = s.accountRepo.UpdateExtra(stateCtx, account.ID, map[string]any{capabilityMarkExtraKey(mark.Model, mark.Protocol, mark.Shape): record}); err != nil {
		return err
	}
	s.resilience.invalidateAccountGroups(account)
	return nil
}

// LimitCapabilityMarkUpdates must use the locked database row, in the same
// transaction as the merge. Renewals remain allowed when the account is full.
func LimitCapabilityMarkUpdates(current, updates map[string]any, accountID int64, now time.Time) map[string]any {
	result, accepted := maps.Clone(updates), maps.Clone(current)
	if accepted == nil {
		accepted = map[string]any{}
	}
	for key, value := range updates {
		if !strings.HasPrefix(key, CapabilityMarksExtraKey+"_") || value == nil {
			continue
		}
		incoming := capabilityMarkFromExtra(value)
		if incoming == nil {
			continue
		}
		count, renewal := 0, false
		for existingKey, existing := range accepted {
			if !strings.HasPrefix(existingKey, CapabilityMarksExtraKey+"_") {
				continue
			}
			mark := capabilityMarkFromExtra(existing)
			if mark != nil && mark.ExpiresAt.After(now) && mark.Fingerprint == incoming.Fingerprint {
				count++
				renewal = renewal || existingKey == key
			}
		}
		if !renewal && count >= CapabilityMarkLimit {
			delete(result, key)
			slog.Info("sched_quality.capability_mark_limit", "account_id", accountID, "limit", CapabilityMarkLimit)
			continue
		}
		accepted[key] = value
	}
	return result
}

// The send binds the final outbound tuple; events only supply refusal evidence.
// Stream-only observations keep their existing separate TTL and feature gates.
func (s *OpenAIGatewayService) capabilityFailureLearner(ctx context.Context, account *Account, body []byte, protocol string) func(int, []byte) {
	if s == nil || s.accountRepo == nil || account == nil || account.routesByModel() || !gjson.ValidBytes(body) {
		return nil
	}
	mark := CapabilityMark{Model: strings.TrimSpace(gjson.GetBytes(body, "model").String()), Protocol: normalizeCapabilityProtocol(protocol),
		Shape: "non_stream", Reason: "request_unsupported"}
	if IsExplicitImageGenerationIntent("", mark.Model, body) || HasCompactionTriggerInInput(body) {
		return nil
	}
	if gjson.GetBytes(body, "stream").Bool() {
		mark.Shape = "stream"
	}
	if !validCapabilityMark(mark) {
		return nil
	}
	var once sync.Once
	state := resilience.StateFrom(ctx)
	outputStart := state.ClientOutputBytes()
	return func(status int, refusal []byte) {
		if state.ClientOutputBytes() > outputStart || !IsUpstreamCapabilityFailure(status, refusal) || resilience.IsStreamOnlyFailure(status, refusal) {
			return
		}
		fields := schedErrorFields(refusal)
		code := strings.ToLower(fields.Get("code").String() + " " + fields.Get("type").String())
		message := strings.ToLower(schedErrorMessage(refusal))
		// Input modality and frontend identity are absent from the tuple.
		// These rejections remain ClassCapability but must not exclude text.
		if strings.Contains(code, "unsupported_input_capability") || strings.Contains(code, "unsupported_frontend") ||
			strings.Contains(message, "unsupported input capability") || strings.Contains(message, "unsupported frontend") {
			return
		}
		once.Do(func() {
			if resilience.IsModelUnsupportedResponse(status, refusal) || strings.Contains(code, "model") ||
				schedUnsupportedModelMessage.MatchString(strings.ToLower(strings.Join(strings.Fields(message), " "))) {
				mark.Reason = "model_unsupported"
			}
			if err := s.LearnCapabilityMark(ctx, account, mark); err != nil {
				slog.Warn("resilience.capability_mark_failed", "account_id", account.ID, "error", err)
			}
		})
	}
}

func (s *OpenAIGatewayService) capabilityWSFailureLearner(ctx context.Context, account *Account, body []byte) func(int, []byte) {
	wire, err := sjson.SetBytes(body, "stream", true)
	if err != nil {
		return nil
	}
	return s.capabilityFailureLearner(ctx, account, wire, resilience.CapabilityProtocolResponses)
}

func observeCapabilityFailure(learn func(int, []byte), payload []byte) {
	if learn != nil && ClassifyOpenAIResponsesTerminal("", payload).QualityFailure() {
		status := http.StatusBadRequest
		if explicit, ok := openAIModelAvailabilityExplicitStatus(payload); ok {
			status = explicit
		}
		learn(status, payload)
	}
}

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
		(account.Platform != PlatformOpenAI || !account.IsOpenAIOAuthLike())
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
	if !capabilityAccountEligible(a) || (a.Extra[capabilityLearnedResponsesKey] == nil && a.Extra[capabilityLearnedChatKey] == nil) {
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

// CapabilityExclusion only filters direct protocol paths whose upstream stream
// mode follows the client. Conversion and ambiguous bridge paths stay eligible.
func (a *Account) CapabilityExclusion(ctx context.Context, model string, now time.Time) *resilience.LearnedCapability {
	// Model-routed accounts may consult a live protocol catalog during actual
	// forwarding. Selection cannot reproduce that route without I/O.
	if !resilience.Current().StreamOnlyLearningOn() || !capabilityAccountEligible(a) || a.routesByModel() {
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
		if normalizeCapabilityProtocol(resolveUpstreamProtocol(a, inbound, a.GetMappedModel(model), nil)) != protocol {
			return nil
		}
	}
	// Grok OAuth Chat may use its forced-stream Responses bridge depending on
	// the request body. Selection has no body, so never exclude it by a raw
	// Chat observation. Native Responses and API-key raw Chat follow the client.
	if a.IsGrokOAuth() && protocol == resilience.CapabilityProtocolChat {
		return nil
	}
	for _, record := range a.CapabilityLearned(now) {
		if record.Protocol == protocol {
			return &record
		}
	}
	return nil
}

func capabilityEligibilityReason(ctx context.Context, account *Account, model string, now time.Time) string {
	if protocol, stream, ok := resilience.CapabilityRequestFrom(ctx); ok && account != nil && !account.routesByModel() {
		_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, false)
		inbound := APIProtocolResponses
		if protocol == resilience.CapabilityProtocolChat {
			inbound = APIProtocolChatCompletions
		}
		if account.Platform == PlatformOpenAI {
			protocol = normalizeCapabilityProtocol(resolveUpstreamProtocol(account, inbound, upstreamModel, nil))
			// Chat-to-Responses and Codex OAuth force streaming on the wire.
			stream = stream || protocol == resilience.CapabilityProtocolResponses && (inbound == APIProtocolChatCompletions || account.IsOpenAIOAuthLike())
		}
		shape := "non_stream"
		if stream {
			shape = "stream"
		}
		if account.CapabilityMarked(upstreamModel, protocol, shape, now) != nil {
			resilience.NoteCapabilityExcluded(ctx, account.ID)
			return resilience.ReasonCapabilityMarked
		}
	}
	if account.CapabilityExclusion(ctx, model, now) != nil {
		resilience.NoteCapabilityExcluded(ctx, account.ID)
		return resilience.ReasonCapabilityStreamOnly
	}
	return ""
}

// LearnStreamOnlyFailure observes a deterministic upstream 400 before any
// side effects or client output. Learning is independent of immediate retry.
func (s *OpenAIGatewayService) LearnStreamOnlyFailure(ctx context.Context, account *Account, protocol string, upstreamStream bool, status int, body []byte, outboundBodies ...[]byte) *UpstreamFailoverError {
	// Grok's dedicated HTTP forwarder reaches this hook without a quality
	// capture. The final wire body still identifies its exact rejected tuple.
	if account != nil && account.Platform == PlatformGrok && normalizeCapabilityProtocol(protocol) == resilience.CapabilityProtocolResponses && len(outboundBodies) > 0 {
		if learn := s.capabilityFailureLearner(ctx, account, outboundBodies[0], protocol); learn != nil {
			learn(status, body)
		}
	}
	cfg := resilience.Current()
	protocol = normalizeCapabilityProtocol(protocol)
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 || !capabilityAccountEligible(account) ||
		upstreamStream || capabilityExtraKey(protocol) == "" || !cfg.StreamOnlyLearningOn() || !resilience.IsStreamOnlyFailure(status, body, outboundBodies...) {
		return nil
	}
	if _, stream, known := resilience.CapabilityRequestFrom(ctx); !known || stream {
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

// capabilitySelectionError names the stream-only exclusion on a no-account
// error when it is the cause: every failed attempt of the request was such an
// exclusion, the rule of IsCapabilityOnlyExhaustion. Accounts the selection
// filter dropped for it are already in the summary; an exclusion earlier in
// the request (or in the group before a fallback) must not relabel another
// cause such as rate limits.
func capabilitySelectionError(ctx context.Context, err error) error {
	if isNoAccountSelectionError(err) && resilience.CapabilityOnlyAttempts(ctx) &&
		!strings.Contains(err.Error(), resilience.ReasonCapabilityStreamOnly) {
		return fmt.Errorf("%w (%s)", err, resilience.ReasonCapabilityStreamOnly)
	}
	return err
}

// IsCapabilityOnlySelectionError is true only when every candidate was
// excluded for this capability; diagnostic text alone is not authoritative.
func IsCapabilityOnlySelectionError(err error) bool {
	var marker interface{ CapabilityOnly() bool }
	return errors.As(err, &marker) && marker.CapabilityOnly()
}

func IsCapabilityOnlyExhaustion(ctx context.Context, err *UpstreamFailoverError) bool {
	return err.IsCapabilityStreamOnly() && resilience.CapabilityOnlyAttempts(ctx)
}

func (e *UpstreamFailoverError) IsCapabilityStreamOnly() bool {
	return e != nil && e.Reason == GatewayFailureReason(resilience.ReasonCapabilityStreamOnly)
}

func (s *adminServiceImpl) ClearAccountCapabilityLearning(ctx context.Context, id int64) error {
	return s.clearAccountCapabilityExtra(ctx, id, true, true)
}

func (s *adminServiceImpl) ClearAccountCapabilityMarks(ctx context.Context, id int64) error {
	return s.clearAccountCapabilityExtra(ctx, id, false, true)
}

func (s *adminServiceImpl) ClearAccountStreamOnlyLearning(ctx context.Context, id int64) error {
	return s.clearAccountCapabilityExtra(ctx, id, true, false)
}

func (s *adminServiceImpl) clearAccountCapabilityExtra(ctx context.Context, id int64, streamOnly, marks bool) error {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	updates := map[string]any{}
	if streamOnly {
		updates[capabilityLearnedResponsesKey], updates[capabilityLearnedChatKey] = nil, nil
	}
	for key := range account.Extra {
		if marks && strings.HasPrefix(key, CapabilityMarksExtraKey+"_") {
			updates[key] = nil
		}
	}
	if err = s.accountRepo.UpdateExtra(ctx, id, updates); err != nil {
		return err
	}
	Resilience().invalidateAccountGroups(account)
	return nil
}

// MergeCapabilityLearningExtra preserves managed observations from the latest
// locked database row, never from a client edit or copied/imported account.
func MergeCapabilityLearningExtra(next, current map[string]any) map[string]any {
	result := maps.Clone(next)
	managed := func(key string) bool {
		return key == capabilityLearnedResponsesKey || key == capabilityLearnedChatKey || strings.HasPrefix(key, CapabilityMarksExtraKey+"_")
	}
	for key := range result {
		if managed(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if !managed(key) {
			continue
		}
		delete(result, key)
		if result == nil {
			result = map[string]any{}
		}
		result[key] = value
	}
	return result
}

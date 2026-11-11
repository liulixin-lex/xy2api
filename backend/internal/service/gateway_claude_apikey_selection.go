package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type anthropicCompatDispatchModelKey struct{}

// WithAnthropicCompatDispatchModel freezes the same channel mapping that the
// handler applies to its outbound logical body, before account-level mapping.
func WithAnthropicCompatDispatchModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, anthropicCompatDispatchModelKey{}, model)
}

type anthropicCompatRequestEligibilityKey struct{}

// PrepareAnthropicCompatibilityForSelection retains only a capability decision,
// not a second copy of the body. Provenance resolution still owns signed history.
func (s *GatewayService) PrepareAnthropicCompatibilityForSelection(ctx context.Context, userID, keyID int64, body []byte, entry, explicitSession string, headers ...http.Header) (context.Context, error) {
	ctx, err := s.PrepareAnthropicCompatibility(ctx, userID, keyID, body, entry, explicitSession, headers...)
	if err != nil {
		return ctx, err
	}
	if scope := compatScope(ctx); scope != nil && scope.HistoryErr != nil {
		return ctx, scope.HistoryErr
	}
	// Invalid Messages field types are user errors regardless of the account.
	// They must not become retryable candidate rejections.
	if entry == "messages" || entry == "count_tokens" {
		if value := gjson.GetBytes(body, "system"); value.Exists() && value.Type != gjson.Null && value.Type != gjson.String && !value.IsArray() {
			return ctx, compatInvalid("anthropic_compat_request_unsupported", "system must be text or a block array")
		}
		if value := gjson.GetBytes(body, "metadata"); value.Exists() && value.Type != gjson.Null && !value.IsObject() {
			return ctx, compatInvalid("anthropic_compat_request_unsupported", "metadata must be an object")
		}
	}
	if err := compatUnsupportedRequest(body, entry); err != nil {
		ctx = context.WithValue(ctx, anthropicCompatRequestEligibilityKey{}, err)
	}
	return ctx, nil
}

func anthropicCompatDispatchModel(ctx context.Context, model string) string {
	if dispatch, ok := ctx.Value(anthropicCompatDispatchModelKey{}).(string); ok && dispatch != "" {
		return dispatch
	}
	return model
}

// Persistent support excludes request-specific beta and identity readiness gates.
// Those affect scheduling, not the model-not-found diagnosis.
func anthropicCompatModelSupported(account *Account, model string) bool {
	profile, err := configuredAnthropicCompatProfile(account)
	return err == nil && (profile == nil || (account.IsModelSupported(model) && profile.SupportsModel(account.GetMappedModel(model))))
}

func (s *GatewayService) anthropicCompatCandidateEligible(ctx context.Context, account *Account, model string) bool {
	return s.anthropicCompatCandidateEligibility(ctx, account, model) == nil
}

// Apply account-local constraints before load/slot acquisition. Only unbound
// requests can receive a candidate-ineligible error and try another account.
func (s *GatewayService) anthropicCompatCandidateEligibility(ctx context.Context, account *Account, model string) error {
	scope := compatScope(ctx)
	profile, err := resolveAnthropicCompatProfile(ctx, account)
	if err != nil {
		return compatCandidateError(scope, compatInvalid("anthropic_compat_configuration_conflict", err.Error()))
	}
	if profile == nil {
		return nil
	}
	if (scope == nil || scope.Record == nil) && !anthropicCompatAcceptsNewSessions(account) {
		return compatCandidateError(scope, compatUnavailable("Compatibility account is draining existing sessions"))
	}
	if scope != nil && scope.Record == nil && ((profile.ID == AnthropicCompatProfileClaudeCodeV1 && scope.NativeCompatible) || (profile.ID == AnthropicCompatProfileClaudeCodeV2 && scope.NativeCompatibleV2)) {
		return nil
	}
	if err := compatChatProfileContinuationError(scope, profile); err != nil {
		return compatCandidateError(scope, err)
	}
	if err, _ := ctx.Value(anthropicCompatRequestEligibilityKey{}).(error); err != nil {
		return compatCandidateError(scope, err)
	}
	model = anthropicCompatDispatchModel(ctx, model)
	if !account.IsModelSupported(model) {
		return compatCandidateError(scope, compatInvalid("anthropic_compat_model_unsupported", "Account mapping does not admit this model"))
	}
	model = account.GetMappedModel(model)
	if !profile.SupportsModel(model) {
		return compatCandidateError(scope, compatInvalid("anthropic_compat_model_unsupported", "Model is outside the compatibility profile capability range"))
	}
	if scope != nil && scope.Record != nil && scope.Record.Model != "" && scope.Record.Model != model {
		return compatInvalid("anthropic_compat_history_incompatible", "Signed history requires its original model")
	}
	if account.AnthropicCompatIdentitySeed() == "" {
		return compatCandidateError(scope, compatUnavailable("Account compatibility identity has not been initialized"))
	}
	betas := strings.Join(profile.RequiredBetas, ",")
	if scope != nil {
		betas += "," + scope.ClientBeta
	}
	if override, ok := account.HeaderOverrideValue("anthropic-beta"); ok {
		betas += "," + override
	}
	policy := s.evaluateBetaPolicy(ctx, betas, account, model)
	if policy.blockErr != nil {
		return compatCandidateError(scope, compatInvalid("anthropic_compat_configuration_conflict", "Beta policy blocks this compatibility request"))
	}
	for _, beta := range strings.Split(betas, ",") {
		if _, filtered := policy.filterSet[strings.TrimSpace(beta)]; filtered {
			return compatCandidateError(scope, compatInvalid("anthropic_compat_configuration_conflict", "Beta policy filters a required compatibility or client capability"))
		}
	}
	return nil
}

// A provenance binding is a routing constraint, not a sticky preference. Use
// the regular snapshot, health, quota, channel and slot checks for its sole owner.
func (s *GatewayService) selectAnthropicCompatBoundAccount(ctx context.Context, groupID *int64, model string, excluded map[int64]struct{}) (*Account, context.Context, bool, error) {
	scope := compatScope(ctx)
	if scope == nil || scope.Record == nil {
		return nil, ctx, false, nil
	}
	if _, found := excluded[scope.Record.AccountID]; found {
		return nil, ctx, true, compatUnavailable("The account bound to signed history is temporarily unavailable")
	}
	group, resolvedID, err := s.checkClaudeCodeRestriction(ctx, groupID)
	if err != nil {
		return nil, ctx, true, err
	}
	groupID = resolvedID
	ctx = s.withGroupContext(ctx, group)
	ctx = s.withGatewayProfitControlGate(ctx, groupID)
	platform, forced, err := s.resolvePlatform(ctx, groupID, group, model)
	if err != nil {
		return nil, ctx, true, err
	}
	accounts, mixed, err := s.listSchedulableAccounts(ctx, groupID, platform, forced)
	if err != nil {
		return nil, ctx, true, compatUnavailable("The account bound to signed history cannot be loaded")
	}
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)
	gates := s.newGatewayCandidateGates(ctx, groupID, platform, mixed, model)
	for i := range accounts {
		account := &accounts[i]
		if account.ID != scope.Record.AccountID {
			continue
		}
		if err := s.guardAnthropicCompatAccount(ctx, account); err != nil {
			return nil, ctx, true, err
		}
		if err := s.anthropicCompatCandidateEligibility(ctx, account, model); err != nil {
			return nil, ctx, true, err
		}
		reason, _ := gates.candidateReason(ctx, account, true, time.Now())
		if reason != "" || s.checkChannelPricingRestriction(ctx, groupID, model) {
			return nil, ctx, true, compatUnavailable("The account bound to signed history is temporarily unavailable")
		}
		hydrated, err := s.hydrateSelectedAccount(ctx, account)
		if err != nil {
			return nil, ctx, true, compatUnavailable("The account bound to signed history cannot be loaded")
		}
		if err := s.guardAnthropicCompatAccount(ctx, hydrated); err != nil {
			return nil, ctx, true, err
		}
		if err := s.anthropicCompatCandidateEligibility(ctx, hydrated, model); err != nil {
			return nil, ctx, true, err
		}
		return hydrated, ctx, true, nil
	}
	return nil, ctx, true, compatUnavailable("The account bound to signed history is outside the available account pool")
}

func (s *GatewayService) selectAnthropicCompatBoundWithLoad(ctx context.Context, groupID *int64, model string, excluded map[int64]struct{}) (*AccountSelectionResult, bool, error) {
	account, ctx, handled, err := s.selectAnthropicCompatBoundAccount(ctx, groupID, model, excluded)
	if !handled || err != nil {
		return nil, handled, err
	}
	if s.concurrencyService == nil {
		result, err := s.newSelectionResult(ctx, account, true, func() {}, nil)
		return result, true, err
	}
	slot, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if err != nil {
		return nil, true, compatUnavailable("The account bound to signed history has no available slot")
	}
	if slot.Acquired {
		result, err := s.newSelectionResult(ctx, account, true, slot.ReleaseFunc, nil)
		return result, true, err
	}
	cfg := s.schedulingConfig()
	result, err := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{AccountID: account.ID, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting})
	return result, true, err
}

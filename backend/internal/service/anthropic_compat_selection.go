package service

import (
	"context"
	"strings"
)

func anthropicCompatModelSupported(account *Account, model string) bool {
	profile, err := configuredAnthropicCompatProfile(account)
	return err == nil && (profile == nil || (account.IsModelSupported(model) && profile.SupportsModel(account.GetMappedModel(model))))
}

func (s *GatewayService) anthropicCompatReadiness(ctx context.Context, account *Account, profile *AnthropicCompatProfile, model, clientBeta string) string {
	if account.AnthropicCompatIdentitySeed() == "" {
		return "identity_uninitialized"
	}
	betas := strings.Join(profile.RequiredBetas, ",") + "," + clientBeta
	if override, ok := account.HeaderOverrideValue("anthropic-beta"); ok {
		betas += "," + override
	}
	policy := s.evaluateBetaPolicy(ctx, betas, account, model)
	if policy.blockErr != nil {
		return "beta_policy_blocked"
	}
	for _, required := range profile.RequiredBetas {
		if _, filtered := policy.filterSet[required]; filtered {
			return "required_beta_filtered"
		}
	}
	return ""
}

func (s *GatewayService) anthropicCompatCandidateEligible(ctx context.Context, account *Account, model string) bool {
	profile, err := ResolveAnthropicCompatProfile(account)
	reason := ""
	if err != nil {
		reason = "configuration_conflict"
	} else if profile == nil {
		return true
	} else {
		model = anthropicCompatDispatchModel(ctx, model)
		owner, _ := ctx.Value(anthropicCompatStickyKey{}).(int64)
		if !anthropicCompatAcceptsNewSessions(account) && owner != account.ID {
			reason = "draining"
		} else if model != "" && (!account.IsModelSupported(model) || !profile.SupportsModel(account.GetMappedModel(model))) {
			reason = "model_unsupported"
		} else {
			clientBeta := ""
			if request := compatRequest(ctx); request != nil {
				clientBeta = request.ClientBeta
			}
			reason = s.anthropicCompatReadiness(ctx, account, profile, account.GetMappedModel(model), clientBeta)
		}
	}
	if reason != "" {
		logAnthropicCompatAttempt(ctx, account, model, "ineligible", reason)
	}
	return reason == ""
}

// Reuse the first ordinary sticky lookup, before scheduling can replace the
// binding. Compatibility admission never performs its own cache read.
func withAnthropicCompatStickyOwner(ctx context.Context, owner int64) context.Context {
	if _, ok := ctx.Value(anthropicCompatStickyKey{}).(int64); ok {
		return ctx
	}
	return context.WithValue(ctx, anthropicCompatStickyKey{}, owner)
}

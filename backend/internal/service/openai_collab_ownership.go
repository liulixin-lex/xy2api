package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Ownership capacity is local to an account/model only in lossless-first.
// A persistence failure must not widen this into an account-wide cooldown.
func (s *OpenAIGatewayService) collabOwnershipCooldown(ctx context.Context, account *Account, status int, headers http.Header, body []byte, model string) bool {
	if gate, _ := ctx.Value(collabLosslessKey{}).(*CollabLosslessGate); gate == nil {
		return false
	}
	if !resilience.StateFrom(ctx).CollabSettingsSnapshot().CollabLosslessFirstOn() || !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() || account == nil || s == nil || s.accountRepo == nil || strings.TrimSpace(model) == "" {
		return false
	}
	evidence := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: body, HTTPStatus: status})
	if evidence.Ownership != resilience.CollabOwnershipClassCapacity {
		return false
	}
	if headers != nil && headers.Get("Retry-After") == "" {
		headers.Set("Retry-After", "5")
	}
	_ = s.accountRepo.SetModelRateLimit(ctx, account.ID, model, time.Now().Add(5*time.Second), "collab_ownership_capacity")
	return true
}

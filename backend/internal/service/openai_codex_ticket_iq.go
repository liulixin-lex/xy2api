package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
)

func codexTicketIdentity(t *openAICodexTicket) string {
	if t == nil {
		return ""
	}
	return receiptForCodexTicket(t).identity()
}
func (r codexTicketReceipt) identity() string {
	sum := sha256.Sum256([]byte(hex.EncodeToString(r.stateHash[:]) + "\x00" + r.revision + "\x00" + r.capturedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

// CodexTicketIQResultCurrent runs under the account row lock on IQ completion.
func CodexTicketIQResultCurrent(a *Account, result iqcheck.Result, now time.Time) bool {
	if a == nil {
		return false
	}
	if result.StateFingerprint != "" && (result.StateFingerprint != codexTicketFixedProxyFingerprint(a) || result.StateRevision != codexAccountTicketConfigOf(a).Revision) {
		return false
	}
	if result.StateTicketID == "" {
		return true
	}
	ac := codexAccountTicketConfigOf(a)
	t := parseOpenAICodexTicketFromAny(a.ID, result.StateModel, a.Extra[openAICodexTicketExtraKey(result.StateModel)])
	return ac.manages(result.StateModel) && t.validFor(a, ac, now) && codexTicketIdentity(t) == result.StateTicketID
}

func CodexTicketIQIdentitiesCurrent(a *Account, identities []IQTicketIdentity, now time.Time) bool {
	for _, identity := range identities {
		result := iqcheck.Result{
			StateTicketID: identity.StateTicketID, StateFingerprint: identity.StateFingerprint,
			StateRevision: identity.StateRevision, StateModel: identity.StateModel,
		}
		if identity.StateTicketID == "" && identity.StateFingerprint == "" {
			continue
		}
		if !CodexTicketIQResultCurrent(a, result, now) {
			return false
		}
	}
	return true
}

func (s *OpenAIGatewayService) codexTicketIQWait(ctx context.Context, account *Account, outboundModel string) (string, time.Time) {
	now := time.Now()
	if !s.openAICodexTicketEnabledContext(ctx) {
		return "", now
	}
	config := codexAccountTicketConfigOf(account)
	if !config.manages(outboundModel) {
		return "", now
	}
	runtime := codexTicketRuntimes(account)[outboundModel]
	if runtime.LeaseUntil != nil && runtime.LeaseUntil.After(now) {
		return "waiting_state", now.Add(6 * time.Second)
	}
	if config.MissingPolicy != "allow_unprotected" && !s.lookupOpenAICodexTicket(account, outboundModel).validFor(account, config, now) {
		next := now.Add(6 * time.Second)
		if runtime.RetryAfter != nil && runtime.RetryAfter.After(next) {
			next = *runtime.RetryAfter
		}
		return "waiting_state", next
	}
	return "", now
}

func (s *OpenAIGatewayService) recoverCodexFromIQTarget(ctx context.Context, claim IQTargetClaim, result iqcheck.Result, identities []IQTicketIdentity) {
	if result.Status != "degraded" || result.Reason != "wrong_answer" && result.Reason != "model_mismatch" {
		return
	}
	a, err := s.codexTicketAccountByID(ctx, claim.AccountID)
	if err != nil || !CodexTicketIQIdentitiesCurrent(a, identities, time.Now()) {
		return
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if identity.StateModel == "" {
			continue
		}
		if identity.StateTicketID != "" {
			key := identity.StateModel + "\x00" + identity.StateTicketID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			ticket := parseOpenAICodexTicketFromAny(a.ID, identity.StateModel, a.Extra[openAICodexTicketExtraKey(identity.StateModel)])
			if codexTicketIdentity(ticket) == identity.StateTicketID {
				s.invalidateCodexTicketFromResponse(receiptForCodexTicket(ticket), "iq_degraded")
			}
			continue
		}
		if identity.StateProtection != "unprotected" {
			continue
		}
		key := "unprotected\x00" + identity.StateModel
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		_, err = s.mutateCodexTicket(ctx, a.ID, 0, func(live *Account, _ int, now time.Time) (bool, error) {
			if !CodexTicketIQIdentitiesCurrent(live, []IQTicketIdentity{identity}, now) || !codexAccountTicketConfigOf(live).manages(identity.StateModel) {
				return false, nil
			}
			rt := codexTicketRuntimes(live)[identity.StateModel]
			if rt.IQRecoveryAt != nil && rt.IQRecoveryAt.After(now.Add(-15*time.Minute)) {
				return false, nil
			}
			rt.IQRecoveryAt = &now
			rt.Requested = true
			rt.event(now, "iq_unprotected_recovery")
			saveCodexTicketRuntime(live, identity.StateModel, rt)
			return true, nil
		})
		if err == nil {
			s.startCodexAccountTicketJob(ctx, a.ID, false, identity.StateModel)
		}
	}
}

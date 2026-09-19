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
	if result.StateFingerprint != "" && (a == nil || result.StateFingerprint != codexTicketFixedProxyFingerprint(a) || result.StateRevision != codexAccountTicketConfigOf(a).Revision) {
		return false
	}
	if result.StateTicketID == "" {
		return true
	}
	ac := codexAccountTicketConfigOf(a)
	t := parseOpenAICodexTicketFromAny(a.ID, result.StateModel, a.Extra[openAICodexTicketExtraKey(result.StateModel)])
	return ac.manages(result.StateModel) && t.validFor(a, ac, now) && codexTicketIdentity(t) == result.StateTicketID
}
func CodexTicketProbeLeaseUntil(a *Account, now time.Time) *time.Time {
	for _, rt := range codexTicketRuntimes(a) {
		if rt.LeaseUntil != nil && rt.LeaseUntil.After(now) {
			return rt.LeaseUntil
		}
	}
	return nil
}
func (s *OpenAIGatewayService) codexTicketIQWait(ctx context.Context, a *Account) (string, time.Time) {
	now := time.Now()
	if !s.openAICodexTicketEnabledContext(ctx) {
		return "", now
	}
	model := s.openAICodexTicketOutboundModel(a, a.IQCheck.Profile().Model, false)
	ac := codexAccountTicketConfigOf(a)
	if !ac.manages(model) {
		return "", now
	}
	if until := CodexTicketProbeLeaseUntil(a, now); until != nil {
		return "waiting_state", now.Add(6 * time.Second)
	}
	if ac.MissingPolicy != "allow_unprotected" && !s.lookupOpenAICodexTicket(a, model).validFor(a, ac, now) {
		next := now.Add(6 * time.Second)
		rt := codexTicketRuntimes(a)[model]
		if rt.RetryAfter != nil && rt.RetryAfter.After(next) {
			next = *rt.RetryAfter
		}
		return "waiting_state", next
	}
	return "", now
}
func (s *OpenAIGatewayService) recoverCodexFromIQ(ctx context.Context, c IQCheckClaim, result iqcheck.Result) {
	if result.Status != "degraded" || result.Reason != "wrong_answer" {
		return
	}
	a, e := s.codexTicketAccountByID(ctx, c.AccountID)
	if e != nil || a.IQCheck.Revision != c.Revision || !CodexTicketIQResultCurrent(a, result, time.Now()) {
		return
	}
	if result.StateTicketID == "" {
		if result.Diagnostic == nil || result.Diagnostic.StateProtection != "unprotected" {
			return
		}
		_, e = s.mutateCodexTicket(ctx, a.ID, 0, func(live *Account, _ int, now time.Time) (bool, error) {
			if live.IQCheck.Revision != c.Revision || !CodexTicketIQResultCurrent(live, result, now) || !codexAccountTicketConfigOf(live).manages(result.StateModel) {
				return false, nil
			}
			rt := codexTicketRuntimes(live)[result.StateModel]
			if rt.IQRecoveryAt != nil && rt.IQRecoveryAt.After(now.Add(-15*time.Minute)) {
				return false, nil
			}
			rt.IQRecoveryAt = &now
			rt.Requested = true
			rt.event(now, "iq_unprotected_recovery")
			saveCodexTicketRuntime(live, result.StateModel, rt)
			return true, nil
		})
		if e == nil {
			s.startCodexAccountTicketJob(ctx, a.ID, false, result.StateModel)
		}
		return
	}
	t := s.lookupOpenAICodexTicket(a, result.StateModel)
	if codexTicketIdentity(t) != result.StateTicketID {
		return
	}
	s.invalidateCodexTicketFromResponse(receiptForCodexTicket(t), "iq_degraded")
}

func CodexTicketIQResultFinished(a *Account, result iqcheck.Result, now time.Time) {
	if result.StateModel == "" {
		return
	}
	rt := codexTicketRuntimes(a)[result.StateModel]
	if rt.IQRetest == "queued" {
		rt.IQRetest = result.Status
		rt.event(now, "iq_result_"+result.Status)
		saveCodexTicketRuntime(a, result.StateModel, rt)
	}
}

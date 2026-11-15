package service

import (
	"context"
	"errors"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// One observation per account prevents the same candidate being counted again
// by fresh/DB/last-resort checks. Each check records its first failing gate.
type selectionFailureEvidence struct {
	reasons map[int64]SelectionReason
	ready   map[int64]bool
	cause   error
}

type selectionFailureEvidenceKey struct{}
type openAISelectionRequestKey struct{}

func withSelectionFailureEvidence(ctx context.Context) context.Context {
	return context.WithValue(ctx, selectionFailureEvidenceKey{}, &selectionFailureEvidence{
		reasons: make(map[int64]SelectionReason), ready: make(map[int64]bool),
	})
}

func observeSelectionFailure(ctx context.Context, id int64, reason string, cause error) {
	evidence, _ := ctx.Value(selectionFailureEvidenceKey{}).(*selectionFailureEvidence)
	if evidence == nil || reason == "" {
		return
	}
	evidence.reasons[id] = normalizedSelectionReason(reason)
	delete(evidence.ready, id)
	if cause != nil {
		evidence.cause = cause
	}
}

func observeSelectionReady(ctx context.Context, id int64) {
	evidence, _ := ctx.Value(selectionFailureEvidenceKey{}).(*selectionFailureEvidence)
	if evidence != nil {
		evidence.ready[id] = true
		delete(evidence.reasons, id)
	}
}

func accountSchedulingFailureReason(account *Account) string {
	if account == nil {
		return string(SelectionReasonDependencyError)
	}
	if !account.IsActive() {
		if account.Status == StatusError {
			return string(SelectionReasonAuthError)
		}
		return string(SelectionReasonManualDisabled)
	}
	if !account.Schedulable {
		return string(SelectionReasonManualDisabled)
	}
	now := time.Now()
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return string(SelectionReasonExpired)
	}
	if account.OverloadUntil != nil && now.Before(*account.OverloadUntil) {
		return string(SelectionReasonRuntimeBlocked)
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return string(SelectionReasonRateLimited)
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		if resilience.IsBalancePause(account.TempUnschedulableReason) {
			return string(SelectionReasonBalanceExhausted)
		}
		return string(SelectionReasonRuntimeBlocked)
	}
	if account.IsAPIKeyOrBedrock() && account.IsQuotaExceeded() {
		return string(SelectionReasonBalanceExhausted)
	}
	return ""
}

// Persistent request expressibility deliberately ignores learned capability,
// cooldown, quota, profit, privacy and other transient/request-specific vetoes.
func (s *OpenAIGatewayService) openAIConfiguredRequestFailureReason(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) string {
	if reason := s.chatRequestCapabilityFailureReason(ctx, account); reason != "" {
		return reason
	}
	if !accountSupportsOpenAICapabilities(account, req.RequiredCapability, req.RequiredImageCapability) {
		return string(SelectionReasonCapabilityMismatch)
	}
	if req.RequireCompact && openAICompactSupportTier(account) == 0 {
		return string(SelectionReasonCompactUnsupported)
	}
	if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		return string(SelectionReasonTransportIncompatible)
	}
	return ""
}

func selectionExhaustionEvidence(ctx context.Context, diagnosis ModelAvailabilityDiagnosis, err error) *SelectionExhaustion {
	e := &SelectionExhaustion{
		Known: diagnosis.Known, Members: diagnosis.Members, Eligible: diagnosis.Members,
		ModelSupported: diagnosis.ModelSupported, RequestSupported: diagnosis.RequestSupported,
		Reasons: make(map[SelectionReason]int), OwnerID: resilience.StateFrom(ctx).StickyOwner(),
	}
	evidence, _ := ctx.Value(selectionFailureEvidenceKey{}).(*selectionFailureEvidence)
	if evidence != nil {
		observedMembers := len(evidence.ready)
		for id, reason := range evidence.reasons {
			e.Reasons[reason]++
			if id > 0 {
				observedMembers++
			}
		}
		e.Immediate = len(evidence.ready)
		if observedMembers > e.Members {
			e.Members = observedMembers
		}
		e.LastCause = evidence.cause
	}
	if diagnosis.Err != nil {
		e.Known = false
		e.Reasons[SelectionReasonDependencyError]++
		e.LastCause = diagnosis.Err
	}
	var upstream *UpstreamFailoverError
	if errors.As(err, &upstream) {
		e.LastCause = upstream
	}
	if e.LastCause == nil && !isNoAccountSelectionError(err) {
		e.LastCause = err
	}
	if e.Known && e.Members == 0 {
		e.Reasons[SelectionReasonNoMembers] = 1
	} else if e.Known && e.ModelSupported == 0 && len(e.Reasons) == 0 {
		e.Reasons[SelectionReasonModelNotSupported] = e.Eligible
	}
	// Only complete, request-independent hard evidence can feed group caches.
	// Soft penalties, capacity and request-local exclusions prove no such fact.
	e.GroupLevel = e.Known && len(e.Reasons) > 0 && e.Immediate == 0 && e.OwnerID == 0 && e.LastCause == nil
	if evidence != nil && len(evidence.reasons) < e.Eligible {
		e.GroupLevel = false
	}
	for reason := range e.Reasons {
		switch reason {
		case SelectionReasonNoMembers, SelectionReasonManualDisabled, SelectionReasonAuthError,
			SelectionReasonExpired, SelectionReasonBalanceExhausted:
		default:
			e.GroupLevel = false
		}
	}
	return e
}

func selectionErrorWithExhaustion(err error, exhaustion *SelectionExhaustion) error {
	if openAIErr, ok := err.(openAINoAvailableSelectionError); ok {
		// A later permitted pass can discover additional configured candidates.
		// The initial stream-only marker must not conceal their capacity failure.
		if exhaustion.Known {
			openAIErr.capabilityOnly = exhaustion.ModelSupported > 0 && len(exhaustion.Reasons) == 1 && exhaustion.Reasons[SelectionReasonCapabilityStreamOnly] == exhaustion.ModelSupported
		} else if len(exhaustion.Reasons) > 1 {
			openAIErr.capabilityOnly = false
		}
		openAIErr.exhaustion = exhaustion
		return openAIErr
	}
	// Keep dedicated compact/profit/owner errors reachable through the chain.
	return selectionExhaustionError{cause: err, exhaustion: exhaustion}
}

func (s *OpenAIGatewayService) withOpenAISelectionExhaustion(ctx context.Context, req OpenAIAccountScheduleRequest, err error) error {
	if err == nil || errors.Is(err, errNegativeCached) {
		return err
	}
	diagnosis, accounts := s.openAIModelAvailability(ctx, req.GroupID, req.RequestedModel, req.Platform)
	evidence, _ := ctx.Value(selectionFailureEvidenceKey{}).(*selectionFailureEvidence)
	// Repository scheduling lists omit some timed hard states. Diagnose those
	// persistent members too, without acquiring a slot or relaxing any gate.
	for i := range accounts {
		a := &accounts[i]
		if evidence == nil || evidence.reasons[a.ID] != "" || evidence.ready[a.ID] {
			continue
		}
		if _, excluded := req.ExcludedIDs[a.ID]; excluded {
			observeSelectionFailure(ctx, a.ID, "excluded", nil)
		} else if reason := openAICompatibleAccountEligibilityFailureReason(ctx, a, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability, s); reason != "" {
			if reason == "not_schedulable" {
				reason = accountSchedulingFailureReason(a)
			}
			observeSelectionFailure(ctx, a.ID, reason, nil)
		} else if ok, reason := s.isAccountRequestCompatibleReason(ctx, a, req); !ok {
			observeSelectionFailure(ctx, a.ID, reason, nil)
		} else if !s.isAccountTransportCompatible(a, req.RequiredTransport) {
			observeSelectionFailure(ctx, a.ID, "transport_incompatible", nil)
		} else {
			// No observed capacity attempt for this member: absence cannot prove quota.
			observeSelectionFailure(ctx, a.ID, "dependency_error", nil)
		}
	}
	exhaustion := selectionExhaustionEvidence(ctx, diagnosis, err)
	if !isNoAccountSelectionError(err) {
		exhaustion.Known, exhaustion.GroupLevel = false, false
		return selectionDependencyFailure{cause: err, exhaustion: exhaustion}
	}
	return selectionErrorWithExhaustion(err, exhaustion)
}

func gatewaySelectionDiagnosisReason(d selectionFailureDiagnosis) string {
	switch d.Category {
	case "unschedulable":
		return d.Detail
	case "platform_filtered":
		return string(SelectionReasonPlatformMismatch)
	case "model_unsupported":
		return string(SelectionReasonModelNotSupported)
	case "eligible":
		return string(SelectionReasonDependencyError)
	default:
		return d.Category
	}
}

func normalizedSelectionReason(reason string) SelectionReason {
	switch reason {
	case "quota_auto_pause_requests", "quota_auto_pause_tokens", "quota_auto_pause_retry_after":
		return SelectionReasonQuotaAutoPause
	case "channel_restricted", "channel_pricing_restricted":
		return SelectionReasonChannelUpstreamRestricted
	case "account_nil", "ineligible", "verify_failed":
		return SelectionReasonDependencyError
	default:
		return SelectionReason(reason)
	}
}

// A failed candidate query is still an internal dependency failure, not an
// observed empty pool. Expose its evidence without adding a no-account sentinel.
type selectionDependencyFailure struct {
	cause      error
	exhaustion *SelectionExhaustion
}

func (e selectionDependencyFailure) Error() string { return e.cause.Error() }
func (e selectionDependencyFailure) Unwrap() error { return e.cause }
func (e selectionDependencyFailure) As(target any) bool {
	if out, ok := target.(**SelectionExhaustion); ok {
		*out = e.exhaustion.Snapshot()
		return true
	}
	return false
}
func selectionDependencyFailed(ctx context.Context) bool {
	evidence, _ := ctx.Value(selectionFailureEvidenceKey{}).(*selectionFailureEvidence)
	return evidence != nil && evidence.cause != nil
}

type selectionExhaustionError struct {
	cause      error
	exhaustion *SelectionExhaustion
}

func (e selectionExhaustionError) Error() string   { return e.cause.Error() }
func (e selectionExhaustionError) Unwrap() []error { return []error{e.cause, e.exhaustion} }

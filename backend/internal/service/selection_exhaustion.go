package service

import "errors"

// SelectionReason names the first actual gate that rejected a candidate.
type SelectionReason string

// SelectionExhaustion distinguishes unknown selection from observed exhaustion.
type SelectionExhaustion struct {
	Known, GroupLevel                                              bool
	Members, Eligible, ModelSupported, RequestSupported, Immediate int
	Reasons                                                        map[SelectionReason]int
	OwnerID                                                        int64
	LastCause                                                      error
}

func (e *SelectionExhaustion) Error() string { return ErrNoAvailableAccounts.Error() }
func (e *SelectionExhaustion) Unwrap() []error {
	if e != nil && e.LastCause != nil {
		return []error{ErrNoAvailableAccounts, e.LastCause}
	}
	return []error{ErrNoAvailableAccounts}
}

// Snapshot makes the mutable reason counts independent of the scheduler.
func (e *SelectionExhaustion) Snapshot() *SelectionExhaustion {
	if e == nil {
		return nil
	}
	v := *e
	if e.Reasons != nil {
		v.Reasons = make(map[SelectionReason]int, len(e.Reasons))
		for reason, count := range e.Reasons {
			v.Reasons[reason] = count
		}
	}
	return &v
}

const (
	SelectionReasonNoMembers                  SelectionReason = "no_members"
	SelectionReasonPlatformMismatch           SelectionReason = "platform_mismatch"
	SelectionReasonModelNotSupported          SelectionReason = "model_not_supported"
	SelectionReasonAccountModelNotOwned       SelectionReason = "account_model_not_owned"
	SelectionReasonResponsesRequired          SelectionReason = "responses_required"
	SelectionReasonCapabilityMismatch         SelectionReason = "capability_mismatch"
	SelectionReasonTransportIncompatible      SelectionReason = "transport_incompatible"
	SelectionReasonCapabilityStreamOnly       SelectionReason = "capability_stream_only"
	SelectionReasonCompactUnsupported         SelectionReason = "compact_unsupported"
	SelectionReasonOwnerFixed                 SelectionReason = "owner_fixed"
	SelectionReasonPrivacyNotSet              SelectionReason = "privacy_not_set"
	SelectionReasonShadowParentUnhealthy      SelectionReason = "shadow_parent_unhealthy"
	SelectionReasonChannelUpstreamRestricted  SelectionReason = "channel_upstream_restricted"
	SelectionReasonProfitThreshold            SelectionReason = "profit_threshold"
	SelectionReasonProfitInvalidAccountRate   SelectionReason = "profit_invalid_account_rate"
	SelectionReasonRateLimited                SelectionReason = "rate_limited"
	SelectionReasonModelRateLimited           SelectionReason = "model_rate_limited"
	SelectionReasonConcurrencyFull            SelectionReason = "concurrency_full"
	SelectionReasonQueueFull                  SelectionReason = "queue_full"
	SelectionReasonQueueTimeout               SelectionReason = "queue_timeout"
	SelectionReasonManualDisabled             SelectionReason = "manual_disabled"
	SelectionReasonAuthError                  SelectionReason = "auth_error"
	SelectionReasonExpired                    SelectionReason = "expired"
	SelectionReasonBalanceExhausted           SelectionReason = "balance_exhausted"
	SelectionReasonQuotaAutoPause5h           SelectionReason = "quota_auto_pause_5h"
	SelectionReasonQuotaAutoPause7d           SelectionReason = "quota_auto_pause_7d"
	SelectionReasonQuotaAutoPause             SelectionReason = "quota_auto_pause"
	SelectionReasonAccountSchedulingThreshold SelectionReason = "account_scheduling_threshold"
	SelectionReasonRuntimeBlocked             SelectionReason = "runtime_blocked"
	SelectionReasonSameSiteHold               SelectionReason = "same_site_hold"
	SelectionReasonProxyStreamQuarantined     SelectionReason = "proxy_stream_quarantined"
	SelectionReasonDependencyError            SelectionReason = "dependency_error"
	SelectionReasonExcluded                   SelectionReason = "excluded"
	SelectionReasonFailedChangeTarget         SelectionReason = "failed_change_target"
)

// SelectionExhaustionFromError gives handlers an independent scheduling snapshot.
func SelectionExhaustionFromError(err error) (SelectionExhaustion, bool) {
	var exhaustion *SelectionExhaustion
	if !errors.As(err, &exhaustion) || exhaustion == nil {
		return SelectionExhaustion{}, false
	}
	return *exhaustion.Snapshot(), true
}

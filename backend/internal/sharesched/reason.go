// Package sharesched is the pure share-scheduling kernel: policy validation,
// weighted rendezvous ordering and the per-request placement plan. It holds no
// I/O and no service types so every rule can be tested exhaustively; the
// gateway adapters supply candidates and execute the plan.
package sharesched

// Reason is a stable code explaining a placement decision or an exclusion.
// Codes are kept in report counters and translated by the admin UI, so
// existing values must never change meaning. Retired codes (spill_keep,
// return_immediate, return_deadline, drain_cold, drain_deadline,
// admin_rebalance) are no longer produced and may still appear in counters
// written before an upgrade.
type Reason string

// Selection reasons.
const (
	ReasonOwnerContinuation       Reason = "owner_continuation"
	ReasonAffinityHit             Reason = "affinity_hit"
	ReasonAffinityWait            Reason = "affinity_wait"
	ReasonSpillRebind             Reason = "spill_rebind"
	ReasonSoftSpill               Reason = "soft_spill"
	ReasonNewSession              Reason = "new_session"
	ReasonSessionless             Reason = "sessionless"
	ReasonRealignCold             Reason = "realign_cold"
	ReasonReturnCold              Reason = "return_cold"
	ReasonRebindUnavailable       Reason = "rebind_unavailable"
	ReasonTierOverflowSaturated   Reason = "tier_overflow_saturated"
	ReasonTierOverflowUnavailable Reason = "tier_overflow_unavailable"
	ReasonFailoverNext            Reason = "failover_next"
	ReasonModelRouting            Reason = "model_routing"
	ReasonModelRoutingFallback    Reason = "model_routing_fallback"
	// Gateway resilience (design §4): the session left an account it kept
	// failing on, and a last-resort pick of a soft-penalised account when
	// nothing else was available.
	ReasonResilFailureStreak Reason = "resil_failure_streak"
	ReasonLastResortSoft     Reason = "last_resort_soft"
)

// Exclusion and skip reasons. Adapters map the original Sub2API gates onto
// these codes; the kernel itself only produces the share-specific ones.
const (
	ReasonDisabled            Reason = "disabled"
	ReasonInactive            Reason = "inactive"
	ReasonNotSchedulable      Reason = "not_schedulable"
	ReasonExpired             Reason = "expired"
	ReasonRateLimited         Reason = "rate_limited"
	ReasonOverloaded          Reason = "overloaded"
	ReasonTempUnschedulable   Reason = "temp_unschedulable"
	ReasonBalanceExhausted    Reason = "balance_exhausted"
	ReasonQuotaExceeded       Reason = "quota_exceeded"
	ReasonModelUnsupported    Reason = "model_unsupported"
	ReasonModelRateLimited    Reason = "model_rate_limited"
	ReasonWindowCost          Reason = "window_cost"
	ReasonRPMLimit            Reason = "rpm_limit"
	ReasonSessionLimit        Reason = "session_limit"
	ReasonIQDegraded          Reason = "iq_degraded"
	ReasonQuotaAutoPause      Reason = "quota_auto_pause"
	ReasonShadowParent        Reason = "shadow_parent_unhealthy"
	ReasonGrokQuota           Reason = "grok_quota"
	ReasonChannelRestricted   Reason = "channel_restricted"
	ReasonProfitGate          Reason = "profit_gate"
	ReasonPrivacyRequired     Reason = "privacy_required"
	ReasonPlatformMismatch    Reason = "platform_mismatch"
	ReasonTransportMismatch   Reason = "transport_mismatch"
	ReasonCapabilityMismatch  Reason = "capability_mismatch"
	ReasonRuntimeBlocked      Reason = "runtime_blocked"
	ReasonProxyQuarantined    Reason = "proxy_quarantined"
	ReasonSchedulingThreshold Reason = "scheduling_threshold"
	ReasonExcludedInRequest   Reason = "excluded_in_request"
	ReasonSaturated           Reason = "saturated"
	ReasonCodexPressure       Reason = "codex_pressure"
	ReasonIneligible          Reason = "ineligible"
)

// KeySource records where the affinity key came from.
type KeySource string

const (
	KeyContinuation KeySource = "key_continuation"
	KeySession      KeySource = "key_session"
	KeyAnchor       KeySource = "key_anchor"
	KeyNone         KeySource = "key_none"
)

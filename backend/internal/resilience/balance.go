package resilience

import "strings"

// Temp-unschedulable reason prefixes written by the resilience layer. The
// prefix decides the penalty tier and lets each component clear only what it
// wrote.
const (
	ReasonPrefixBalanceExhausted = "balance:exhausted"
	ReasonPrefixTransient        = "transient:"
)

// BalancePauseReason is the temp_unschedulable reason of a balance pause
// (design §3.2): the account's relay reported an exhausted balance.
func BalancePauseReason(evidence string) string {
	return ReasonPrefixBalanceExhausted + ": " + truncate(evidence, 200)
}

// IsBalancePause reports whether a temp_unschedulable reason is a balance
// pause written by the resilience layer.
func IsBalancePause(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), "balance:")
}

// PenaltyTier is the scheduling tier of an account restriction.
type PenaltyTier int

// Penalty tiers (design §3.1).
const (
	TierNone PenaltyTier = iota
	TierSoft
	TierHard
)

// TempUnschedTier classifies a temp_unschedulable reason. Reasons written by
// the resilience layer are explicit; the counted 403 cooldown and overload
// style pauses are soft; every other reason (balance pauses, credentials,
// balance of CN providers, administrator rules) stays hard.
func TempUnschedTier(reason string) PenaltyTier {
	r := strings.TrimSpace(reason)
	switch {
	case r == "":
		return TierHard
	case strings.HasPrefix(r, ReasonPrefixTransient):
		return TierSoft
	case strings.HasPrefix(r, Reason403Cooldown), strings.HasPrefix(r, legacyOpenAI403CooldownPrefix):
		return TierSoft
	}
	return TierHard
}

// Reason403Cooldown prefixes the counted non-balance 403 cooldown of
// non-OpenAI API-key platforms.
const Reason403Cooldown = "auth:403_cooldown"

// legacyOpenAI403CooldownPrefix is the reason the existing OpenAI 403
// counter writes; it is the same soft cooldown.
const legacyOpenAI403CooldownPrefix = "OpenAI 403 temporary cooldown"

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

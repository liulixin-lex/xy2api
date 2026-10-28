package resilience

import (
	"strconv"
	"strings"
	"time"
)

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
	return BalancePauseReasonAtStep(1, evidence)
}

// BalancePauseReasonAtStep formats the persisted balance failure level. Old
// balance:exhausted: reasons remain readable as step one.
func BalancePauseReasonAtStep(step int, evidence string) string {
	if step < 1 || step > 3 {
		step = 1
	}
	return ReasonPrefixBalanceExhausted + "[step=" + strconv.Itoa(step) + "]: " + truncate(evidence, 200)
}

func BalancePauseStep(reason string) int {
	reason = strings.TrimSpace(reason)
	prefix := ReasonPrefixBalanceExhausted + "[step="
	if !strings.HasPrefix(reason, prefix) {
		return 1
	}
	end := strings.IndexByte(reason[len(prefix):], ']')
	if end < 0 {
		return 1
	}
	step, err := strconv.Atoi(reason[len(prefix) : len(prefix)+end])
	if err != nil || step < 1 || step > 3 {
		return 1
	}
	return step
}

// BalancePauseStepForFailure only advances after a failure attempt began
// after the previous balance pause expired. A late response from an older
// in-flight attempt therefore cannot advance the ladder.
func BalancePauseStepForFailure(reason string, previousUntil, attemptStartedAt, now time.Time) int {
	if !IsBalancePause(reason) {
		return 1
	}
	step := BalancePauseStep(reason)
	if previousUntil.IsZero() || attemptStartedAt.IsZero() || now.Before(previousUntil) || !attemptStartedAt.After(previousUntil) {
		return step
	}
	if step < 3 {
		step++
	}
	return step
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
	if r == "" {
		return TierHard
	}
	for _, prefix := range softReasonPrefixes {
		if strings.HasPrefix(r, prefix) {
			return TierSoft
		}
	}
	return TierHard
}

// softReasonPrefixes are the temp_unschedulable reasons of soft penalties.
var softReasonPrefixes = []string{ReasonPrefixTransient, Reason403Cooldown, legacyOpenAI403CooldownPrefix}

// SoftReasonPrefixes returns the reason prefixes TempUnschedTier treats as
// soft, for storage that has to tell the tiers apart (review R01).
func SoftReasonPrefixes() []string {
	return append([]string(nil), softReasonPrefixes...)
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

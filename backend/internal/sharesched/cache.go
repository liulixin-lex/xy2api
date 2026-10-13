package sharesched

import (
	"math"
	"strings"
	"time"
)

// CacheFamily groups platforms by prompt-cache semantics.
type CacheFamily string

const (
	CacheFamilyAnthropic CacheFamily = "anthropic"
	CacheFamilyOpenAI    CacheFamily = "openai"
	CacheFamilyGemini    CacheFamily = "gemini"
	CacheFamilyOther     CacheFamily = "other"
)

// CacheTTL returns the conservative cache lifetime used to decide whether a
// session's upstream cache is cold. Upper bounds are preferred: a late
// migration costs nothing, an early one rebuilds a warm cache.
//
//   - Anthropic family: 1h when the outgoing request asks for it, else 5m.
//   - OpenAI family: 30m (fixed for GPT-5.6+, "about 30 minutes" earlier).
//   - Gemini implicit cache and unknown platforms: 5m.
func CacheTTL(family CacheFamily, requestsOneHour bool, overrideSeconds int) time.Duration {
	if overrideSeconds > 0 {
		return time.Duration(overrideSeconds) * time.Second
	}
	switch family {
	case CacheFamilyAnthropic:
		if requestsOneHour {
			return time.Hour
		}
		return 5 * time.Minute
	case CacheFamilyOpenAI:
		return 30 * time.Minute
	default:
		return 5 * time.Minute
	}
}

// MinCacheableTokens is the smallest prompt that an upstream will cache.
// Below it a session has no cache to protect. Unknown models use the lowest
// common threshold: overstating cacheability only makes migration more
// conservative.
func MinCacheableTokens(family CacheFamily, model string) int {
	if family != CacheFamilyAnthropic {
		return 1024
	}
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "3-haiku") || strings.Contains(model, "3-5-haiku") || strings.Contains(model, "haiku-3"):
		return 2048
	case strings.Contains(model, "opus-4-5") || strings.Contains(model, "haiku-4-5"):
		return 4096
	default:
		return 1024
	}
}

// CacheProfile describes how valuable the session's upstream cache is.
type CacheProfile struct {
	// TTL is the cache lifetime from CacheTTL.
	TTL time.Duration
	// EstimatedTokens approximates the prompt length.
	EstimatedTokens int
	// Cacheable reports whether the prompt reaches MinCacheableTokens.
	Cacheable bool
}

// NewCacheProfile builds a profile from the family, model and estimate.
func NewCacheProfile(family CacheFamily, model string, requestsOneHour bool, overrideSeconds, estimatedTokens int) CacheProfile {
	return CacheProfile{
		TTL:             CacheTTL(family, requestsOneHour, overrideSeconds),
		EstimatedTokens: estimatedTokens,
		Cacheable:       estimatedTokens >= MinCacheableTokens(family, model),
	}
}

// Warm reports whether a session idle for idle still has a usable cache.
func (c CacheProfile) Warm(idle time.Duration) bool {
	return c.Cacheable && c.TTL > 0 && idle < c.TTL
}

// WaitBudget is how long a request may wait for its saturated bound account.
// Waiting pays off only while the cache is warm and roughly in proportion to
// the prefix that would otherwise be recomputed.
func WaitBudget(w Wait, profile CacheProfile, idle time.Duration) time.Duration {
	if !profile.Warm(idle) {
		return 0
	}
	maxWait := time.Duration(w.MaxMs) * time.Millisecond
	switch w.Mode {
	case WaitFixed:
		return maxWait
	case WaitAuto:
		budget := time.Duration(float64(profile.EstimatedTokens) / 1000 * float64(w.MsPer1kTokens) * float64(time.Millisecond))
		return min(budget, maxWait)
	default:
		return 0
	}
}

// CorrectionFactor scales one account's new-session weight toward its target
// share, bounded by the configured strength. It returns 1 when correction is
// off or the inputs are unusable.
func CorrectionFactor(strength string, target, actual float64) float64 {
	var gamma, lo, hi float64
	switch strength {
	case CorrectionGentle:
		gamma, lo, hi = 0.5, 0.5, 2
	case CorrectionStrong:
		gamma, lo, hi = 1, 0.25, 4
	default:
		return 1
	}
	if target <= 0 || math.IsNaN(target) || math.IsNaN(actual) {
		return 1
	}
	const epsilon = 1e-3
	factor := math.Pow(target/math.Max(actual, epsilon), gamma)
	return math.Min(hi, math.Max(lo, factor))
}

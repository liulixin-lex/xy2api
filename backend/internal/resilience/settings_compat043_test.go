// Frozen from 3092629d7 (0.4.3) settings.go. Only top-level names are
// prefixed to avoid collisions; the decoding and validation are unchanged.
package resilience

import (
	"encoding/json"
	"fmt"
	"strings"
)

// legacy043SettingKey is the settings-table key of the stored JSON.
const legacy043SettingKey = "gateway_resilience_settings"

// legacy043Settings is the full gateway_resilience_settings document. Every capability
// has its own switch; turning one off restores the 0.3.0 behaviour of that
// capability only.
type legacy043Settings struct {
	Enabled       bool                         `json:"enabled"`
	Failure       legacy043FailureSettings     `json:"failure"`
	Session       legacy043SessionSettings     `json:"session"`
	LastResort    legacy043SwitchSettings      `json:"last_resort"`
	Balance       legacy043BalanceSettings     `json:"balance"`
	Budget        legacy043BudgetSettings      `json:"budget"`
	Keepalive     legacy043KeepaliveSettings   `json:"keepalive"`
	Stall         legacy043StallSettings       `json:"stall"`
	Disconnect    legacy043DisconnectSettings  `json:"disconnect"`
	Errors        legacy043ErrorSettings       `json:"errors"`
	FallbackGroup legacy043FallbackSettings    `json:"fallback_group"`
	Reservation   legacy043ReservationSettings `json:"reservation"`
}

// legacy043SwitchSettings is a capability with only an on/off switch.
type legacy043SwitchSettings struct {
	Enabled bool `json:"enabled"`
}

// legacy043FailureSettings tunes classification and soft cooldowns.
type legacy043FailureSettings struct {
	TransientCooldownSeconds int      `json:"transient_cooldown_seconds"`
	RetryAfterCapSeconds     int      `json:"retry_after_cap_seconds"`
	BalanceKeywordsExtra     []string `json:"balance_keywords_extra"`
}

// legacy043SessionSettings tunes session failure handling.
type legacy043SessionSettings struct {
	Enabled             bool `json:"enabled"`
	StreakThreshold     int  `json:"streak_threshold"`
	StreakWindowSeconds int  `json:"streak_window_seconds"`
	AvoidSeconds        int  `json:"avoid_seconds"`
}

// legacy043BalanceSettings tunes the balance pause: an account whose relay reports an
// exhausted balance is paused for PauseSeconds, then real traffic retries it.
// Documents stored by 0.4.0-rc.1 carry more fields; legacy043Parse ignores them.
type legacy043BalanceSettings struct {
	Enabled      bool `json:"enabled"`
	PauseSeconds int  `json:"pause_seconds"`
}

// legacy043FirstOutputTimeouts are per reasoning-effort first-output timeouts.
type legacy043FirstOutputTimeouts struct {
	Default int `json:"default"`
	High    int `json:"high"`
	XHigh   int `json:"xhigh"`
	Max     int `json:"max"`
}

// legacy043BudgetSettings tunes the per-request attempt and time budget.
type legacy043BudgetSettings struct {
	Enabled                           bool                         `json:"enabled"`
	MaxAttempts                       int                          `json:"max_attempts"`
	PreOutputBudgetSeconds            int                          `json:"pre_output_budget_seconds"`
	FirstOutputTimeoutSeconds         legacy043FirstOutputTimeouts `json:"first_output_timeout_seconds"`
	AnthropicFirstEventTimeoutSeconds int                          `json:"anthropic_first_event_timeout_seconds"`
	GeminiFirstEventTimeoutSeconds    int                          `json:"gemini_first_event_timeout_seconds"`
	ModelOverrides                    map[string]int               `json:"model_overrides"`
	SameAccountRetryMax               int                          `json:"same_account_retry_max"`
	GeminiSameAccountRetries          int                          `json:"gemini_same_account_retries"`
	UpstreamRetryAfterCapSeconds      int                          `json:"upstream_retry_after_cap_seconds"`
}

// legacy043KeepaliveSettings tunes the pre-output keepalive.
type legacy043KeepaliveSettings struct {
	Enabled           bool     `json:"enabled"`
	DelaySeconds      int      `json:"delay_seconds"`
	IntervalSeconds   int      `json:"interval_seconds"`
	Edges             []string `json:"edges"`
	SkipOfficialOAuth bool     `json:"skip_official_oauth"`
}

// legacy043StallSettings tunes the text stall detector.
type legacy043StallSettings struct {
	Enabled          bool `json:"enabled"`
	TextStallSeconds int  `json:"text_stall_seconds"`
}

// legacy043AbuseGuardSettings bills repeated undelivered requests.
type legacy043AbuseGuardSettings struct {
	Enabled          bool   `json:"enabled"`
	ThresholdPerHour int    `json:"threshold_per_hour"`
	Mode             string `json:"mode"`
}

// legacy043DisconnectSettings tunes client disconnect handling.
type legacy043DisconnectSettings struct {
	Enabled                bool                        `json:"enabled"`
	CancelUndelivered      bool                        `json:"cancel_undelivered"`
	PartialDrainMaxSeconds int                         `json:"partial_drain_max_seconds"`
	AbuseGuard             legacy043AbuseGuardSettings `json:"abuse_guard"`
}

// legacy043RangeSettings is an inclusive range in seconds.
type legacy043RangeSettings struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// legacy043FastFailSettings tunes the per-key fast fail.
type legacy043FastFailSettings struct {
	Enabled       bool `json:"enabled"`
	WindowSeconds int  `json:"window_seconds"`
	Threshold     int  `json:"threshold"`
}

// legacy043TurnCapSettings tunes the failed-turn cap (design §9.7).
type legacy043TurnCapSettings struct {
	Enabled       bool `json:"enabled"`
	Failures      int  `json:"failures"`
	WindowSeconds int  `json:"window_seconds"`
}

// legacy043ErrorSettings tunes error semantics and storm protection.
type legacy043ErrorSettings struct {
	Enabled                      bool                      `json:"enabled"`
	NoAccountRetryAfter          legacy043RangeSettings    `json:"no_account_retry_after"`
	ConfigRetryAfterSeconds      int                       `json:"config_retry_after_seconds"`
	ReservationRetryAfterSeconds int                       `json:"reservation_retry_after_seconds"`
	NegativeCacheSeconds         int                       `json:"negative_cache_seconds"`
	FastFail                     legacy043FastFailSettings `json:"fast_fail"`
	TurnCap                      legacy043TurnCapSettings  `json:"turn_cap"`
}

// legacy043FallbackSettings tunes the availability fallback group.
type legacy043FallbackSettings struct {
	Enabled             bool `json:"enabled"`
	ReturnStableSeconds int  `json:"return_stable_seconds"`
	BindingTTLSeconds   int  `json:"binding_ttl_seconds"`
}

// legacy043ReservationSettings tunes the in-flight balance reservation estimate.
type legacy043ReservationSettings struct {
	CacheAwareEstimate bool `json:"cache_aware_estimate"`
	OutputP99Cap       bool `json:"output_p99_cap"`
}

// legacy043Defaults returns the documented default settings (design §10).
func legacy043Defaults() legacy043Settings {
	return legacy043Settings{
		Enabled:    true,
		Failure:    legacy043FailureSettings{TransientCooldownSeconds: 30, RetryAfterCapSeconds: 120, BalanceKeywordsExtra: []string{}},
		Session:    legacy043SessionSettings{Enabled: true, StreakThreshold: 2, StreakWindowSeconds: 600, AvoidSeconds: 600},
		LastResort: legacy043SwitchSettings{Enabled: true},
		Balance:    legacy043BalanceSettings{Enabled: true, PauseSeconds: 300},
		// First-output tiers are calibrated on 7 days of production first-token
		// latency (tasks 0.4): about 0.1-0.2% of successful streams exceed them.
		Budget: legacy043BudgetSettings{
			Enabled:                           true,
			MaxAttempts:                       4,
			PreOutputBudgetSeconds:            240,
			FirstOutputTimeoutSeconds:         legacy043FirstOutputTimeouts{Default: 180, High: 180, XHigh: 200, Max: 220},
			AnthropicFirstEventTimeoutSeconds: 90,
			GeminiFirstEventTimeoutSeconds:    90,
			ModelOverrides:                    map[string]int{},
			SameAccountRetryMax:               1,
			GeminiSameAccountRetries:          1,
			UpstreamRetryAfterCapSeconds:      60,
		},
		Keepalive: legacy043KeepaliveSettings{Enabled: true, DelaySeconds: 15, IntervalSeconds: 10, Edges: []string{"cf", "esa"}, SkipOfficialOAuth: true},
		Stall:     legacy043StallSettings{Enabled: true, TextStallSeconds: 120},
		Disconnect: legacy043DisconnectSettings{
			Enabled:                true,
			CancelUndelivered:      true,
			PartialDrainMaxSeconds: 180,
			AbuseGuard:             legacy043AbuseGuardSettings{Enabled: true, ThresholdPerHour: 30, Mode: legacy043AbuseModeBillUpstreamCost},
		},
		Errors: legacy043ErrorSettings{
			Enabled:                      true,
			NoAccountRetryAfter:          legacy043RangeSettings{Min: 2, Max: 30},
			ConfigRetryAfterSeconds:      60,
			ReservationRetryAfterSeconds: 5,
			NegativeCacheSeconds:         2,
			FastFail:                     legacy043FastFailSettings{Enabled: true, WindowSeconds: 30, Threshold: 10},
			TurnCap:                      legacy043TurnCapSettings{Enabled: true, Failures: 5, WindowSeconds: 120},
		},
		FallbackGroup: legacy043FallbackSettings{Enabled: true, ReturnStableSeconds: 120, BindingTTLSeconds: 600},
		Reservation:   legacy043ReservationSettings{CacheAwareEstimate: true, OutputP99Cap: true},
	}
}

// legacy043AbuseModeBillUpstreamCost bills abusive undelivered requests at upstream cost.
const legacy043AbuseModeBillUpstreamCost = "bill_upstream_cost"

// legacy043ValidationError lists every invalid field of a settings document.
type legacy043ValidationError struct {
	Problems []string
}

func (e *legacy043ValidationError) Error() string {
	return "invalid gateway resilience settings: " + strings.Join(e.Problems, "; ")
}

// Validate checks every range of design §10. It reports all problems at once.
func (s legacy043Settings) Validate() error {
	var p []string
	check := func(ok bool, format string, args ...any) {
		if !ok {
			p = append(p, fmt.Sprintf(format, args...))
		}
	}
	in := func(v, lo, hi int) bool { return v >= lo && v <= hi }

	check(in(s.Failure.TransientCooldownSeconds, 1, 3600), "failure.transient_cooldown_seconds must be 1-3600")
	check(in(s.Failure.RetryAfterCapSeconds, 1, 3600), "failure.retry_after_cap_seconds must be 1-3600")
	check(len(s.Failure.BalanceKeywordsExtra) <= 64, "failure.balance_keywords_extra allows at most 64 entries")
	for _, kw := range s.Failure.BalanceKeywordsExtra {
		check(len(strings.TrimSpace(kw)) >= 2 && len(kw) <= 128, "failure.balance_keywords_extra entries must be 2-128 characters")
	}

	check(in(s.Session.StreakThreshold, 1, 20), "session.streak_threshold must be 1-20")
	check(in(s.Session.StreakWindowSeconds, 1, 86400), "session.streak_window_seconds must be 1-86400")
	check(in(s.Session.AvoidSeconds, 1, 86400), "session.avoid_seconds must be 1-86400")

	check(in(s.Balance.PauseSeconds, 30, 3600), "balance.pause_seconds must be 30-3600")

	b := s.Budget
	check(in(b.MaxAttempts, 1, 10), "budget.max_attempts must be 1-10")
	check(in(b.PreOutputBudgetSeconds, 60, 290), "budget.pre_output_budget_seconds must be 60-290")
	for name, v := range map[string]int{
		"default": b.FirstOutputTimeoutSeconds.Default, "high": b.FirstOutputTimeoutSeconds.High,
		"xhigh": b.FirstOutputTimeoutSeconds.XHigh, "max": b.FirstOutputTimeoutSeconds.Max,
		"anthropic_first_event": b.AnthropicFirstEventTimeoutSeconds, "gemini_first_event": b.GeminiFirstEventTimeoutSeconds,
	} {
		check(in(v, 5, b.PreOutputBudgetSeconds), "budget timeout %s must be 5-%d (the pre-output budget)", name, b.PreOutputBudgetSeconds)
	}
	for model, v := range b.ModelOverrides {
		check(strings.TrimSpace(model) != "", "budget.model_overrides keys must not be empty")
		check(in(v, 5, b.PreOutputBudgetSeconds), "budget.model_overrides[%s] must be 5-%d", model, b.PreOutputBudgetSeconds)
	}
	check(in(b.SameAccountRetryMax, 0, 5), "budget.same_account_retry_max must be 0-5")
	check(in(b.GeminiSameAccountRetries, 0, 5), "budget.gemini_same_account_retries must be 0-5")
	check(in(b.UpstreamRetryAfterCapSeconds, 1, 600), "budget.upstream_retry_after_cap_seconds must be 1-600")

	k := s.Keepalive
	check(in(k.DelaySeconds, 5, 60), "keepalive.delay_seconds must be 5-60")
	check(in(k.IntervalSeconds, 5, 30), "keepalive.interval_seconds must be 5-30")
	for _, e := range k.Edges {
		check(legacy043validEdgeName(e), "keepalive.edges entries must be lowercase letters, digits or '-' (got %q)", e)
	}

	check(in(s.Stall.TextStallSeconds, 30, 280), "stall.text_stall_seconds must be 30-280")

	d := s.Disconnect
	check(in(d.PartialDrainMaxSeconds, 1, 3600), "disconnect.partial_drain_max_seconds must be 1-3600")
	check(in(d.AbuseGuard.ThresholdPerHour, 1, 100000), "disconnect.abuse_guard.threshold_per_hour must be 1-100000")
	check(d.AbuseGuard.Mode == legacy043AbuseModeBillUpstreamCost, "disconnect.abuse_guard.mode must be %q", legacy043AbuseModeBillUpstreamCost)

	e := s.Errors
	check(in(e.NoAccountRetryAfter.Min, 1, 300) && in(e.NoAccountRetryAfter.Max, e.NoAccountRetryAfter.Min, 300), "errors.no_account_retry_after needs 1 <= min <= max <= 300")
	check(in(e.ConfigRetryAfterSeconds, 1, 3600), "errors.config_retry_after_seconds must be 1-3600")
	check(in(e.ReservationRetryAfterSeconds, 1, 300), "errors.reservation_retry_after_seconds must be 1-300")
	check(in(e.NegativeCacheSeconds, 1, 60), "errors.negative_cache_seconds must be 1-60")
	check(in(e.FastFail.WindowSeconds, 1, 3600), "errors.fast_fail.window_seconds must be 1-3600")
	check(in(e.FastFail.Threshold, 2, 10000), "errors.fast_fail.threshold must be 2-10000")
	check(in(e.TurnCap.Failures, 2, 30), "errors.turn_cap.failures must be 2-30")
	check(in(e.TurnCap.WindowSeconds, 10, 3600), "errors.turn_cap.window_seconds must be 10-3600")

	f := s.FallbackGroup
	check(in(f.ReturnStableSeconds, 1, 86400), "fallback_group.return_stable_seconds must be 1-86400")
	check(in(f.BindingTTLSeconds, 1, 86400), "fallback_group.binding_ttl_seconds must be 1-86400")

	if len(p) > 0 {
		return &legacy043ValidationError{Problems: p}
	}
	return nil
}

func legacy043validEdgeName(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// legacy043Parse decodes a stored document over the defaults, so fields added in later
// versions keep their default value. Invalid stored documents are rejected so
// the caller can keep the last known good value.
func legacy043Parse(raw string) (legacy043Settings, error) {
	s := legacy043Defaults()
	if strings.TrimSpace(raw) == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return legacy043Defaults(), fmt.Errorf("decode gateway resilience settings: %w", err)
	}
	s.normalize()
	if err := s.Validate(); err != nil {
		return legacy043Defaults(), err
	}
	return s, nil
}

// normalize fills nil collections so callers never branch on nil.
func (s *legacy043Settings) normalize() {
	if s.Failure.BalanceKeywordsExtra == nil {
		s.Failure.BalanceKeywordsExtra = []string{}
	}
	if s.Budget.ModelOverrides == nil {
		s.Budget.ModelOverrides = map[string]int{}
	}
	if s.Keepalive.Edges == nil {
		s.Keepalive.Edges = []string{}
	}
}

// Normalized returns a copy with nil collections filled; handlers use it
// before validating an administrator update.
func (s legacy043Settings) Normalized() legacy043Settings {
	s.normalize()
	return s
}

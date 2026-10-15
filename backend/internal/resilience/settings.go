// Package resilience holds the gateway resilience rules: upstream failure
// classification, request budgets, penalty and balance-state transitions,
// pre-output keepalive frames and storm protection. It has no I/O and no
// service types so every rule can be unit tested; the service and handler
// layers call it through small hooks.
package resilience

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

// SettingKey is the settings-table key of the stored JSON.
const SettingKey = "gateway_resilience_settings"

// Settings is the full gateway_resilience_settings document. Every capability
// has its own switch; turning one off restores the 0.3.0 behaviour of that
// capability only.
type Settings struct {
	Enabled       bool                `json:"enabled"`
	Failure       FailureSettings     `json:"failure"`
	Session       SessionSettings     `json:"session"`
	LastResort    SwitchSettings      `json:"last_resort"`
	Balance       BalanceSettings     `json:"balance"`
	Budget        BudgetSettings      `json:"budget"`
	Keepalive     KeepaliveSettings   `json:"keepalive"`
	Stall         StallSettings       `json:"stall"`
	Disconnect    DisconnectSettings  `json:"disconnect"`
	Errors        ErrorSettings       `json:"errors"`
	FallbackGroup FallbackSettings    `json:"fallback_group"`
	Reservation   ReservationSettings `json:"reservation"`
}

// SwitchSettings is a capability with only an on/off switch.
type SwitchSettings struct {
	Enabled bool `json:"enabled"`
}

// FailureSettings tunes classification and soft cooldowns.
type FailureSettings struct {
	TransientCooldownSeconds int      `json:"transient_cooldown_seconds"`
	RetryAfterCapSeconds     int      `json:"retry_after_cap_seconds"`
	BalanceKeywordsExtra     []string `json:"balance_keywords_extra"`
}

// SessionSettings tunes session failure handling.
type SessionSettings struct {
	Enabled             bool `json:"enabled"`
	StreakThreshold     int  `json:"streak_threshold"`
	StreakWindowSeconds int  `json:"streak_window_seconds"`
	AvoidSeconds        int  `json:"avoid_seconds"`
}

// BalanceSettings tunes the recoverable balance state machine.
type BalanceSettings struct {
	Enabled                    bool    `json:"enabled"`
	RecheckScheduleSeconds     []int   `json:"recheck_schedule_seconds"`
	JitterRatio                float64 `json:"jitter_ratio"`
	SuspectTTLSeconds          int     `json:"suspect_ttl_seconds"`
	RecoveringConcurrency      int     `json:"recovering_concurrency"`
	RecoveringWindowSeconds    int     `json:"recovering_window_seconds"`
	RecoveringSuccessTarget    int     `json:"recovering_success_target"`
	GenerationProbeDailyBudget int     `json:"generation_probe_daily_budget"`
	NotifyAfterHours           int     `json:"notify_after_hours"`
}

// FirstOutputTimeouts are per reasoning-effort first-output timeouts.
type FirstOutputTimeouts struct {
	Default int `json:"default"`
	High    int `json:"high"`
	XHigh   int `json:"xhigh"`
	Max     int `json:"max"`
}

// BudgetSettings tunes the per-request attempt and time budget.
type BudgetSettings struct {
	Enabled                           bool                `json:"enabled"`
	MaxAttempts                       int                 `json:"max_attempts"`
	PreOutputBudgetSeconds            int                 `json:"pre_output_budget_seconds"`
	FirstOutputTimeoutSeconds         FirstOutputTimeouts `json:"first_output_timeout_seconds"`
	AnthropicFirstEventTimeoutSeconds int                 `json:"anthropic_first_event_timeout_seconds"`
	GeminiFirstEventTimeoutSeconds    int                 `json:"gemini_first_event_timeout_seconds"`
	ModelOverrides                    map[string]int      `json:"model_overrides"`
	SameAccountRetryMax               int                 `json:"same_account_retry_max"`
	GeminiSameAccountRetries          int                 `json:"gemini_same_account_retries"`
	UpstreamRetryAfterCapSeconds      int                 `json:"upstream_retry_after_cap_seconds"`
}

// KeepaliveSettings tunes the pre-output keepalive.
type KeepaliveSettings struct {
	Enabled           bool     `json:"enabled"`
	DelaySeconds      int      `json:"delay_seconds"`
	IntervalSeconds   int      `json:"interval_seconds"`
	Edges             []string `json:"edges"`
	SkipOfficialOAuth bool     `json:"skip_official_oauth"`
}

// StallSettings tunes the text stall detector.
type StallSettings struct {
	Enabled          bool `json:"enabled"`
	TextStallSeconds int  `json:"text_stall_seconds"`
}

// AbuseGuardSettings bills repeated undelivered requests.
type AbuseGuardSettings struct {
	Enabled          bool   `json:"enabled"`
	ThresholdPerHour int    `json:"threshold_per_hour"`
	Mode             string `json:"mode"`
}

// DisconnectSettings tunes client disconnect handling.
type DisconnectSettings struct {
	Enabled                bool               `json:"enabled"`
	CancelUndelivered      bool               `json:"cancel_undelivered"`
	PartialDrainMaxSeconds int                `json:"partial_drain_max_seconds"`
	AbuseGuard             AbuseGuardSettings `json:"abuse_guard"`
}

// RangeSettings is an inclusive range in seconds.
type RangeSettings struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// FastFailSettings tunes the per-key fast fail.
type FastFailSettings struct {
	Enabled       bool `json:"enabled"`
	WindowSeconds int  `json:"window_seconds"`
	Threshold     int  `json:"threshold"`
}

// ErrorSettings tunes error semantics and storm protection.
type ErrorSettings struct {
	Enabled                      bool             `json:"enabled"`
	NoAccountRetryAfter          RangeSettings    `json:"no_account_retry_after"`
	ConfigRetryAfterSeconds      int              `json:"config_retry_after_seconds"`
	ReservationRetryAfterSeconds int              `json:"reservation_retry_after_seconds"`
	NegativeCacheSeconds         int              `json:"negative_cache_seconds"`
	FastFail                     FastFailSettings `json:"fast_fail"`
}

// FallbackSettings tunes the availability fallback group.
type FallbackSettings struct {
	Enabled             bool `json:"enabled"`
	ReturnStableSeconds int  `json:"return_stable_seconds"`
	BindingTTLSeconds   int  `json:"binding_ttl_seconds"`
}

// ReservationSettings tunes the in-flight balance reservation estimate.
type ReservationSettings struct {
	CacheAwareEstimate bool `json:"cache_aware_estimate"`
	OutputP99Cap       bool `json:"output_p99_cap"`
}

// Defaults returns the documented default settings (design §10).
func Defaults() Settings {
	return Settings{
		Enabled:    true,
		Failure:    FailureSettings{TransientCooldownSeconds: 30, RetryAfterCapSeconds: 120, BalanceKeywordsExtra: []string{}},
		Session:    SessionSettings{Enabled: true, StreakThreshold: 2, StreakWindowSeconds: 600, AvoidSeconds: 600},
		LastResort: SwitchSettings{Enabled: true},
		Balance: BalanceSettings{
			Enabled:                    true,
			RecheckScheduleSeconds:     []int{60, 120, 300, 600, 900},
			JitterRatio:                0.15,
			SuspectTTLSeconds:          90,
			RecoveringConcurrency:      1,
			RecoveringWindowSeconds:    120,
			RecoveringSuccessTarget:    3,
			GenerationProbeDailyBudget: 48,
			NotifyAfterHours:           72,
		},
		Budget: BudgetSettings{
			Enabled:                           true,
			MaxAttempts:                       4,
			PreOutputBudgetSeconds:            240,
			FirstOutputTimeoutSeconds:         FirstOutputTimeouts{Default: 60, High: 120, XHigh: 120, Max: 180},
			AnthropicFirstEventTimeoutSeconds: 90,
			GeminiFirstEventTimeoutSeconds:    90,
			ModelOverrides:                    map[string]int{},
			SameAccountRetryMax:               1,
			GeminiSameAccountRetries:          1,
			UpstreamRetryAfterCapSeconds:      60,
		},
		Keepalive: KeepaliveSettings{Enabled: true, DelaySeconds: 15, IntervalSeconds: 10, Edges: []string{"cf", "esa"}, SkipOfficialOAuth: true},
		Stall:     StallSettings{Enabled: true, TextStallSeconds: 120},
		Disconnect: DisconnectSettings{
			Enabled:                true,
			CancelUndelivered:      true,
			PartialDrainMaxSeconds: 180,
			AbuseGuard:             AbuseGuardSettings{Enabled: true, ThresholdPerHour: 30, Mode: AbuseModeBillUpstreamCost},
		},
		Errors: ErrorSettings{
			Enabled:                      true,
			NoAccountRetryAfter:          RangeSettings{Min: 2, Max: 30},
			ConfigRetryAfterSeconds:      60,
			ReservationRetryAfterSeconds: 5,
			NegativeCacheSeconds:         2,
			FastFail:                     FastFailSettings{Enabled: true, WindowSeconds: 30, Threshold: 10},
		},
		FallbackGroup: FallbackSettings{Enabled: true, ReturnStableSeconds: 120, BindingTTLSeconds: 600},
		Reservation:   ReservationSettings{CacheAwareEstimate: true, OutputP99Cap: true},
	}
}

// AbuseModeBillUpstreamCost bills abusive undelivered requests at upstream cost.
const AbuseModeBillUpstreamCost = "bill_upstream_cost"

// ValidationError lists every invalid field of a settings document.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid gateway resilience settings: " + strings.Join(e.Problems, "; ")
}

// Validate checks every range of design §10. It reports all problems at once.
func (s Settings) Validate() error {
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

	check(len(s.Balance.RecheckScheduleSeconds) >= 1 && len(s.Balance.RecheckScheduleSeconds) <= 16, "balance.recheck_schedule_seconds needs 1-16 entries")
	for _, v := range s.Balance.RecheckScheduleSeconds {
		check(in(v, 10, 86400), "balance.recheck_schedule_seconds entries must be 10-86400")
	}
	check(s.Balance.JitterRatio >= 0 && s.Balance.JitterRatio <= 0.5, "balance.jitter_ratio must be 0-0.5")
	check(in(s.Balance.SuspectTTLSeconds, 1, 3600), "balance.suspect_ttl_seconds must be 1-3600")
	check(in(s.Balance.RecoveringConcurrency, 1, 1000), "balance.recovering_concurrency must be 1-1000")
	check(in(s.Balance.RecoveringWindowSeconds, 1, 86400), "balance.recovering_window_seconds must be 1-86400")
	check(in(s.Balance.RecoveringSuccessTarget, 1, 1000), "balance.recovering_success_target must be 1-1000")
	check(in(s.Balance.GenerationProbeDailyBudget, 0, 1440), "balance.generation_probe_daily_budget must be 0-1440")
	check(in(s.Balance.NotifyAfterHours, 1, 24*90), "balance.notify_after_hours must be 1-2160")

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
		check(validEdgeName(e), "keepalive.edges entries must be lowercase letters, digits or '-' (got %q)", e)
	}

	check(in(s.Stall.TextStallSeconds, 30, 280), "stall.text_stall_seconds must be 30-280")

	d := s.Disconnect
	check(in(d.PartialDrainMaxSeconds, 1, 3600), "disconnect.partial_drain_max_seconds must be 1-3600")
	check(in(d.AbuseGuard.ThresholdPerHour, 1, 100000), "disconnect.abuse_guard.threshold_per_hour must be 1-100000")
	check(d.AbuseGuard.Mode == AbuseModeBillUpstreamCost, "disconnect.abuse_guard.mode must be %q", AbuseModeBillUpstreamCost)

	e := s.Errors
	check(in(e.NoAccountRetryAfter.Min, 1, 300) && in(e.NoAccountRetryAfter.Max, e.NoAccountRetryAfter.Min, 300), "errors.no_account_retry_after needs 1 <= min <= max <= 300")
	check(in(e.ConfigRetryAfterSeconds, 1, 3600), "errors.config_retry_after_seconds must be 1-3600")
	check(in(e.ReservationRetryAfterSeconds, 1, 300), "errors.reservation_retry_after_seconds must be 1-300")
	check(in(e.NegativeCacheSeconds, 1, 60), "errors.negative_cache_seconds must be 1-60")
	check(in(e.FastFail.WindowSeconds, 1, 3600), "errors.fast_fail.window_seconds must be 1-3600")
	check(in(e.FastFail.Threshold, 2, 10000), "errors.fast_fail.threshold must be 2-10000")

	f := s.FallbackGroup
	check(in(f.ReturnStableSeconds, 1, 86400), "fallback_group.return_stable_seconds must be 1-86400")
	check(in(f.BindingTTLSeconds, 1, 86400), "fallback_group.binding_ttl_seconds must be 1-86400")

	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}

func validEdgeName(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Parse decodes a stored document over the defaults, so fields added in later
// versions keep their default value. Invalid stored documents are rejected so
// the caller can keep the last known good value.
func Parse(raw string) (Settings, error) {
	s := Defaults()
	if strings.TrimSpace(raw) == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Defaults(), fmt.Errorf("decode gateway resilience settings: %w", err)
	}
	s.normalize()
	if err := s.Validate(); err != nil {
		return Defaults(), err
	}
	return s, nil
}

// normalize fills nil collections so callers never branch on nil.
func (s *Settings) normalize() {
	if s.Failure.BalanceKeywordsExtra == nil {
		s.Failure.BalanceKeywordsExtra = []string{}
	}
	if s.Budget.ModelOverrides == nil {
		s.Budget.ModelOverrides = map[string]int{}
	}
	if s.Keepalive.Edges == nil {
		s.Keepalive.Edges = []string{}
	}
	if len(s.Balance.RecheckScheduleSeconds) == 0 {
		s.Balance.RecheckScheduleSeconds = Defaults().Balance.RecheckScheduleSeconds
	}
}

// Normalized returns a copy with nil collections filled; handlers use it
// before validating an administrator update.
func (s Settings) Normalized() Settings {
	s.normalize()
	return s
}

// Capability switches. Each is false when the master switch is off.

func (s *Settings) master() bool { return s != nil && s.Enabled }

// SessionOn reports whether session failure handling is active.
func (s *Settings) SessionOn() bool { return s.master() && s.Session.Enabled }

// LastResortOn reports whether last-resort soft selection is active.
func (s *Settings) LastResortOn() bool { return s.master() && s.LastResort.Enabled }

// BalanceOn reports whether the recoverable balance state machine is active.
func (s *Settings) BalanceOn() bool { return s.master() && s.Balance.Enabled }

// BudgetOn reports whether request budgets are active.
func (s *Settings) BudgetOn() bool { return s.master() && s.Budget.Enabled }

// KeepaliveOn reports whether the pre-output keepalive is active.
func (s *Settings) KeepaliveOn() bool { return s.master() && s.Keepalive.Enabled }

// StallOn reports whether the text stall detector is active.
func (s *Settings) StallOn() bool { return s.master() && s.Stall.Enabled }

// DisconnectOn reports whether disconnect handling is active.
func (s *Settings) DisconnectOn() bool { return s.master() && s.Disconnect.Enabled }

// ErrorsOn reports whether error semantics and storm protection are active.
func (s *Settings) ErrorsOn() bool { return s.master() && s.Errors.Enabled }

// FallbackGroupOn reports whether availability fallback groups are active.
func (s *Settings) FallbackGroupOn() bool { return s.master() && s.FallbackGroup.Enabled }

// FailureOn reports whether classification-driven penalties are active. It
// follows the master switch only; individual penalties have their own gates.
func (s *Settings) FailureOn() bool { return s.master() }

var current atomic.Pointer[Settings]

func init() {
	d := Defaults()
	current.Store(&d)
}

// Current returns the published settings snapshot. It never returns nil and
// the returned value must be treated as read-only.
func Current() *Settings { return current.Load() }

// Publish replaces the process-wide snapshot.
func Publish(s Settings) {
	s.normalize()
	current.Store(&s)
}

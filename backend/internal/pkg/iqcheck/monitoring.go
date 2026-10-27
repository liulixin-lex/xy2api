package iqcheck

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const MaxAttemptsPerRound = 5

func NextEffort(effort string) string {
	switch effort {
	case "minimal":
		return "low"
	case "low":
		return "medium"
	case "medium":
		return "high"
	case "high":
		return "xhigh"
	case "xhigh":
		return "max"
	}
	return effort
}

type Schedule struct {
	NextRunAt                     *time.Time
	NotBefore                     *time.Time
	PausedReason                  string
	DegradedStreak, FailureStreak int
}

// NextSchedule takes the previous streaks and an injected jitter, so scheduling
// can be tested without a clock or random source. NotBefore is a separate floor.
func NextSchedule(now time.Time, interval time.Duration, degradedStreak, failureStreak int, r Result, jitter time.Duration) Schedule {
	s := Schedule{DegradedStreak: degradedStreak, FailureStreak: failureStreak}
	delay := interval
	switch r.Status {
	case "smart":
		s.DegradedStreak, s.FailureStreak = 0, 0
		delay += min(max(0, jitter), min(30*time.Second, interval/20))
	case "degraded":
		s.DegradedStreak, s.FailureStreak = min(4, degradedStreak+1), 0
		delay = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}[max(0, s.DegradedStreak-1)]
	case "unknown":
		pause, _, _ := FailurePolicy(r)
		if pause {
			s.PausedReason = r.Reason
		}
		if r.Reason == "quota_exhausted" || r.Reason == "http_402" {
			delay = max(interval, 15*time.Minute)
		} else {
			s.FailureStreak = min(3, failureStreak+1)
			delay = min(interval*time.Duration(1<<max(0, s.FailureStreak-1)), max(interval, time.Hour))
		}
	}
	if r.Diagnostic != nil {
		s.NotBefore = r.Diagnostic.RetryAfter
		if r.Diagnostic.RetryAfterUnbounded {
			s.PausedReason = "retry_after_unbounded"
		}
	}
	if s.PausedReason == "" {
		next := now.Add(delay)
		s.NextRunAt = &next
	}
	return s
}

// Only documented machine codes enter diagnostics. Raw messages never do.
func ReadUpstreamError(raw []byte, d *Diagnostic) string {
	return classifyUpstreamError(raw, d, 0)
}

// RetryAfter returns an absolute lower bound. Overflow pauses instead of retrying early.
func RetryAfter(value string, now time.Time) (*time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	digits := true
	for _, c := range value {
		if c < '0' || c > '9' {
			digits = false
			break
		}
	}
	if digits {
		n, e := strconv.ParseUint(value, 10, 64)
		if e != nil || n > uint64(math.MaxInt64/int64(time.Second)) {
			return nil, true
		}
		t := now.Add(time.Duration(n) * time.Second)
		return &t, false
	}
	if t, e := http.ParseTime(value); e == nil {
		if t.Before(now) {
			t = now
		}
		return &t, false
	}
	return nil, false
}

func ReadUsage(raw []byte, d *Diagnostic) {
	var x struct {
		Usage struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			Prompt        int64 `json:"prompt_tokens"`
			Completion    int64 `json:"completion_tokens"`
			OutputDetails struct {
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
			CompletionDetails struct {
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &x) != nil {
		return
	}
	u := x.Usage
	d.InputTokens = max(0, max(u.Input, u.Prompt))
	d.OutputTokens = max(0, max(u.Output, u.Completion))
	d.ReasoningTokens = max(0, max(u.OutputDetails.Reasoning, u.CompletionDetails.Reasoning))
}

func FailurePolicy(r Result) (pause, transient, protocol bool) {
	switch r.Reason {
	case "zero_byte_response", "missing_final_message", "empty_final_text", "empty_response", "incomplete_response":
		return false, true, true
	case "authentication_unavailable", "permission_denied", "policy_denied", "unsupported_model", "unsupported_parameter", "unsupported_account_type", "invalid_endpoint", "http_401", "http_400", "http_404":
		return true, false, false
	case "http_403", "http_408", "timeout", "request_failed", "request_cancelled", "response_read_failed", "rate_limited", "upstream_unavailable", "upstream_error", "interrupted":
		return false, true, false
	}
	if strings.HasPrefix(r.Reason, "http_5") || r.Reason == "http_429" {
		return false, true, false
	}
	if r.Diagnostic != nil && (r.Diagnostic.Stage == "decode" || r.Diagnostic.Stage == "terminal" || r.Diagnostic.Stage == "parse" || r.Diagnostic.Stage == "format" || r.Diagnostic.Stage == "read") && r.Reason != "refusal" && r.Reason != "incomplete_response" {
		return false, true, true
	}
	return false, false, false
}

// Preparation failures are known not to have reached the probe transport.
// A timeout or a network error cannot prove non-delivery and stays charged.
func NotSent(r Result) bool {
	if r.Diagnostic == nil || r.Diagnostic.Stage != "request" {
		return false
	}
	switch r.Reason {
	case "waiting_state", "transport_unavailable", "account_unavailable", "unsupported_model", "authentication_unavailable", "invalid_endpoint", "unsupported_account_type", "cancelled_by_account_change":
		return true
	}
	return false
}

// Retryable is deliberately narrower than the periodic failure policy.
func Retryable(r Result) bool {
	if r.Reason == "quota_exhausted" || r.Status != "unknown" || r.Diagnostic != nil && (r.Diagnostic.Transport == "plugin" || r.Diagnostic.RetryAfterUnbounded) {
		return false
	}
	if pause, transient, protocol := FailurePolicy(r); pause {
		return false
	} else if transient || protocol {
		return true
	}
	if r.Diagnostic != nil && r.Diagnostic.Stage == "http" {
		switch r.Diagnostic.HTTPStatus {
		case 429, 502, 503, 504:
			return true
		}
	}
	switch r.Reason {
	case "request_failed", "timeout", "response_read_failed", "rate_limited", "http_408", "http_429", "http_502", "http_503", "http_504", "upstream_error", "incomplete_response", "unparseable_answer", "invalid_answer_structure", "ambiguous_answer", "model_conflict":
		return true
	}
	return false
}

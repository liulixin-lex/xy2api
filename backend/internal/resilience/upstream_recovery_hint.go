package resilience

import (
	"github.com/tidwall/gjson"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// UpstreamRecoveryHint preserves the uncapped upstream retry boundary.
type UpstreamRecoveryHint struct {
	Headers               map[string]string
	Source                string
	ObservedAt, NotBefore time.Time
	Delay                 time.Duration
}

func cloneUpstreamRecoveryHint(h *UpstreamRecoveryHint) *UpstreamRecoveryHint {
	if h == nil {
		return nil
	}
	v := *h
	if h.Headers != nil {
		v.Headers = make(map[string]string, len(h.Headers))
		for k, value := range h.Headers {
			v.Headers[k] = value
		}
	}
	return &v
}
func (s *RequestState) SetUpstreamRecoveryHint(h *UpstreamRecoveryHint) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreamRecoveryHint = cloneUpstreamRecoveryHint(h)
}
func (s *RequestState) UpstreamRecoveryHint() *UpstreamRecoveryHint {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneUpstreamRecoveryHint(s.upstreamRecoveryHint)
}
func (s *RequestState) NoteAccountRetryNotBefore(accountID int64, h UpstreamRecoveryHint) {
	if s == nil || accountID <= 0 || h.NotBefore.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.accountRetryNotBefore[accountID]; ok && !h.NotBefore.After(old.NotBefore) {
		return
	}
	if s.accountRetryNotBefore == nil {
		s.accountRetryNotBefore = make(map[int64]UpstreamRecoveryHint)
	}
	s.accountRetryNotBefore[accountID] = *cloneUpstreamRecoveryHint(&h)
}
func (s *RequestState) AccountRetryNotBefore(accountID int64) (UpstreamRecoveryHint, bool) {
	if s == nil || accountID <= 0 {
		return UpstreamRecoveryHint{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.accountRetryNotBefore[accountID]
	if !ok {
		return UpstreamRecoveryHint{}, false
	}
	return *cloneUpstreamRecoveryHint(&h), true
}

// recoveryDelay rounds upward at nanosecond precision, never shortening a hint.
func recoveryDelay(value string, unit time.Duration) (time.Duration, bool) {
	if strings.ContainsAny(value, "\r\n") {
		return 0, false
	}
	if !recoveryDecimalNumber.MatchString(strings.TrimSpace(value)) {
		return 0, false
	}
	numeric, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsInf(numeric, 0) || math.IsNaN(numeric) || numeric < 0 {
		return 0, false
	}
	if numeric == 0 {
		// Decimal underflow can still carry a positive sub-nanosecond hint.
		if strings.ContainsAny(strings.SplitN(strings.ToLower(value), "e", 2)[0], "123456789") && !strings.HasPrefix(strings.TrimSpace(value), "-") {
			return time.Nanosecond, true
		}
		return 0, false
	}
	number, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok || number.Sign() <= 0 {
		return 0, false
	}
	number.Mul(number, new(big.Rat).SetInt64(int64(unit)))
	whole, rem := new(big.Int), new(big.Int)
	whole.QuoRem(number.Num(), number.Denom(), rem)
	if rem.Sign() > 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() {
		return 0, false
	}
	return time.Duration(whole.Int64()), true
}

func recoveryUnit(unit string) time.Duration {
	unit = strings.ToLower(unit)
	switch {
	case strings.HasPrefix(unit, "ms"), strings.HasPrefix(unit, "milli"):
		return time.Millisecond
	case strings.HasPrefix(unit, "h"):
		return time.Hour
	case strings.HasPrefix(unit, "m"):
		return time.Minute
	default:
		return time.Second
	}
}

func ParseUpstreamRecoveryHint(h http.Header, payload []byte, observedAt time.Time) (UpstreamRecoveryHint, bool) {
	var winner UpstreamRecoveryHint
	choose := func(at time.Time, source string, headers map[string]string) {
		if !at.After(observedAt) || !at.After(winner.NotBefore) {
			return
		}
		winner = UpstreamRecoveryHint{Headers: headers, Source: source, ObservedAt: observedAt, NotBefore: at, Delay: at.Sub(observedAt)}
	}
	parseHeader := func(value, key string) (time.Time, bool) {
		if strings.ContainsAny(value, "\r\n") {
			return time.Time{}, false
		}
		unit := time.Second
		if key == "retry-after-ms" {
			unit = time.Millisecond
		}
		if d, ok := recoveryDelay(value, unit); ok {
			return observedAt.Add(d), true
		}
		if key == "retry-after" {
			at, err := http.ParseTime(strings.TrimSpace(value))
			if err == nil && at.After(observedAt) && at.Sub(observedAt) < time.Duration(math.MaxInt64) {
				return at, true
			}
		}
		return time.Time{}, false
	}
	headers := func(values http.Header, source string) {
		safe := map[string]string{}
		var latest time.Time
		// Use canonical keys when possible, but accept ordinary lower-case maps too.
		for _, key := range []string{"retry-after", "retry-after-ms"} {
			var best time.Time
			for name, vs := range values {
				if !strings.EqualFold(name, key) {
					continue
				}
				for _, value := range vs {
					if at, ok := parseHeader(value, key); ok && at.After(best) {
						best = at
						safe[key] = strings.TrimSpace(value)
					}
				}
			}
			if best.After(latest) {
				latest = best
			}
		}
		if !latest.IsZero() {
			choose(latest, source, safe)
		}
	}
	headers(h, "http.headers")
	if !utf8.Valid(payload) || !gjson.ValidBytes(payload) {
		return winner, !winner.NotBefore.IsZero()
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return winner, !winner.NotBefore.IsZero()
	}
	for _, path := range []string{"response.error.headers", "error.headers"} {
		object := root.Get(path)
		if !object.IsObject() {
			continue
		}
		values := http.Header{}
		object.ForEach(func(key, value gjson.Result) bool {
			if value.Type == gjson.String || value.Type == gjson.Number {
				values.Add(key.Str, value.String())
			}
			return true
		})
		headers(values, path)
	}
	bodyPaths := []string{"response.error", "error"}
	// Only an error envelope authorizes top-level compatibility retry fields.
	// Ordinary response bodies are not retry-hint sources.
	errorEnvelope := false
	switch root.Get("type").String() {
	case "error", "proxy.upstream_error", "response.failed":
		errorEnvelope = true
	case "":
		if object := root.Get("error"); object.IsObject() {
			for _, field := range []string{"message", "type", "code"} {
				v := object.Get(field)
				if v.Type == gjson.String && strings.TrimSpace(v.Str) != "" || field == "code" && v.Type == gjson.Number {
					errorEnvelope = true
				}
			}
		}
	}
	if errorEnvelope {
		bodyPaths = append(bodyPaths, "")
	}
	for _, path := range bodyPaths {
		object := root
		if path != "" {
			object = root.Get(path)
		}
		if !object.IsObject() {
			continue
		}
		for _, field := range []string{"retry_after", "retry_after_ms", "resets_in_seconds"} {
			value := object.Get(field)
			if value.Type != gjson.String && value.Type != gjson.Number {
				continue
			}
			// retry_after has the same seconds/HTTP-date grammar as Retry-After.
			if at, ok := parseHeader(value.String(), strings.ReplaceAll(field, "_", "-")); ok {
				choose(at, strings.TrimPrefix(path+"."+field, "."), nil)
			}
		}
		// A flat type:error payload is itself the trusted error object.
		if path == "" && (root.Get("type").String() != "error" || root.Get("error").IsObject() || root.Get("response.error").IsObject()) {
			continue
		}
		message := object.Get("message")
		if message.Type != gjson.String || strings.ContainsAny(message.Str, "\r\n") {
			continue
		}
		for _, match := range reTryAgainIn.FindAllStringSubmatch(message.Str, -1) {
			if d, ok := recoveryDelay(match[1], recoveryUnit(match[2])); ok {
				choose(observedAt.Add(d), strings.TrimPrefix(path+".message", "."), nil)
			}
		}
		for _, match := range reTryAgainInCompound.FindAllStringSubmatch(message.Str, -1) {
			// Parse each decimal component exactly before summing to avoid early retry.
			total := time.Duration(0)
			valid := true
			for _, part := range recoveryCompoundParts.FindAllStringSubmatch(strings.ToLower(match[1]), -1) {
				// The component regex guarantees decimal syntax. All-zero
				// decimal spellings contribute zero without invalidating the sum.
				if strings.Trim(part[1], "0.") == "" {
					continue
				}
				d, ok := recoveryDelay(part[1], recoveryUnit(part[2]))
				if !ok || d > time.Duration(math.MaxInt64)-total {
					valid = false
					break
				}
				total += d
			}
			if valid && total > 0 {
				choose(observedAt.Add(total), strings.TrimPrefix(path+".message", "."), nil)
			}
		}
	}
	return winner, !winner.NotBefore.IsZero()
}

var recoveryCompoundParts = regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|h|m|s)`)

var recoveryDecimalNumber = regexp.MustCompile(`^[+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

package resilience

import (
	"github.com/tidwall/gjson"
	"math"
	"math/big"
	"net/http"
	"regexp"
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
	for _, path := range []string{"response.error", "error"} {
		object := root.Get(path)
		if !object.IsObject() {
			continue
		}
		for _, field := range []string{"retry_after", "retry_after_ms", "resets_in_seconds"} {
			value := object.Get(field)
			if value.Type != gjson.String && value.Type != gjson.Number {
				continue
			}
			unit := time.Second
			if field == "retry_after_ms" {
				unit = time.Millisecond
			}
			if d, ok := recoveryDelay(value.String(), unit); ok {
				choose(observedAt.Add(d), path+"."+field, nil)
			}
		}
		message := object.Get("message")
		if message.Type != gjson.String || strings.ContainsAny(message.Str, "\r\n") {
			continue
		}
		for _, match := range reTryAgainIn.FindAllStringSubmatch(message.Str, -1) {
			if d, ok := recoveryDelay(match[1], recoveryUnit(match[2])); ok {
				choose(observedAt.Add(d), path+".message", nil)
			}
		}
		for _, match := range reTryAgainInCompound.FindAllStringSubmatch(message.Str, -1) {
			// Parse each decimal component exactly before summing to avoid early retry.
			total := time.Duration(0)
			valid := true
			for _, part := range recoveryCompoundParts.FindAllStringSubmatch(strings.ToLower(match[1]), -1) {
				d, ok := recoveryDelay(part[1], recoveryUnit(part[2]))
				if part[1] == "0" {
					continue
				}
				if !ok || d > time.Duration(math.MaxInt64)-total {
					valid = false
					break
				}
				total += d
			}
			if valid && total > 0 {
				choose(observedAt.Add(total), path+".message", nil)
			}
		}
	}
	return winner, !winner.NotBefore.IsZero()
}

var recoveryCompoundParts = regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|h|m|s)`)

package service

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"time"
)

const codexTicketParserVersion = 2

// Envelope inspection cannot authenticate or decrypt the opaque upstream value.
func codexTicketIssuedAt(state string) (time.Time, bool) {
	if len(state) < 100 || len(state) > 8192 || strings.ContainsAny(state, "\r\n\t ,") {
		return time.Time{}, false
	}
	raw, err := base64.URLEncoding.Strict().DecodeString(state)
	if err != nil || len(raw) < 73 || raw[0] != 0x80 || (len(raw)-57)%16 != 0 {
		return time.Time{}, false
	}
	seconds := binary.BigEndian.Uint64(raw[1:9])
	if seconds < 1577836800 || seconds > 4102444800 {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0).UTC(), true
}

func validCodexTicketState(state string) bool {
	_, ok := codexTicketIssuedAt(state)
	return ok
}

func (t *openAICodexTicket) boundedTimes(ttl time.Duration) (time.Time, time.Time, time.Time, bool) {
	issued, ok := codexTicketIssuedAt(t.State)
	if !ok || t.CapturedAt.IsZero() {
		return time.Time{}, time.Time{}, time.Time{}, false
	}
	first := t.FirstObservedAt
	if first.IsZero() || t.CapturedAt.Before(first) {
		first = t.CapturedAt
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	margin := min(30*time.Second, ttl/10)
	expires := codexTicketEarlier(first.Add(ttl), issued.Add(ttl-margin))
	if !t.ExpiresAt.IsZero() {
		expires = codexTicketEarlier(expires, t.ExpiresAt)
	}
	return issued, first, expires, true
}

func codexTicketEarlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (t *openAICodexTicket) timeUsable(now time.Time) bool {
	ttl := time.Duration(t.TTLSeconds) * time.Second
	if t.TTLSeconds <= 0 {
		ttl = t.ExpiresAt.Sub(t.CapturedAt)
	}
	issued, first, expires, ok := t.boundedTimes(ttl)
	return ok && !issued.After(now.Add(30*time.Second)) && !first.After(now.Add(30*time.Second)) && now.Before(expires)
}

// Normalization is idempotent and can only shorten the stored expiry.
func normalizeCodexTicketTimes(t *openAICodexTicket, ttlSeconds int) bool {
	if t == nil {
		return false
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	issued, first, expires, ok := t.boundedTimes(time.Duration(ttlSeconds) * time.Second)
	if !ok {
		t.Verified = false
		t.ParserVersion = codexTicketParserVersion
		return false
	}
	t.IssuedAt, t.FirstObservedAt, t.ExpiresAt = issued, first, expires
	t.TTLSeconds, t.ParserVersion = ttlSeconds, codexTicketParserVersion
	return true
}

// Ordinary opaque session continuity is deliberately not subject to this rule.
func uniqueCodexTicketHeader(h http.Header) (string, error) {
	var values []string
	for k, v := range h {
		if strings.EqualFold(k, openAICodexTurnStateHeader) {
			values = append(values, v...)
		}
	}
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || !validCodexTicketState(values[0]) {
		return "", errors.New("invalid_state_header")
	}
	return values[0], nil
}

func clearCodexTicketHeader(h http.Header) {
	for k := range h {
		if strings.EqualFold(k, openAICodexTurnStateHeader) {
			delete(h, k)
		}
	}
}

// Called under the account row lock during the versioned startup upgrade.
func NormalizeCodexTicketAccountTimes(a *Account, ttlSeconds int) bool {
	changed := false
	for key, raw := range a.Extra {
		if !strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix) {
			continue
		}
		t := parseOpenAICodexTicketFromAny(a.ID, strings.TrimPrefix(key, openAICodexTicketExtraKeyPrefix), raw)
		if t == nil || t.ParserVersion == codexTicketParserVersion {
			continue
		}
		normalizeCodexTicketTimes(t, ttlSeconds)
		if !t.timeUsable(time.Now()) {
			t.Verified = false
		}
		a.Extra[key] = t
		changed = true
	}
	return changed
}

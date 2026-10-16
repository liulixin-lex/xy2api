package resilience

import (
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// CachedResponse is a gateway error response that may be replayed verbatim.
type CachedResponse struct {
	Status int
	Body   []byte
	Header http.Header
	// RetryAt is when the client may retry; the replay recomputes
	// Retry-After from it.
	RetryAt time.Time
	// Model is the requested model the answer belongs to ("" when unknown);
	// a fast-fail replay only answers requests for the same model.
	Model string
}

// RetryAfterSeconds returns the remaining Retry-After of a replay (at least 1).
func (r CachedResponse) RetryAfterSeconds(now time.Time) int {
	if r.RetryAt.IsZero() {
		return 0
	}
	secs := int((r.RetryAt.Sub(now) + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}

var (
	processNegativeCache = NewNegativeCache()
	processFastFail      = NewFastFail()
)

// ProcessNegativeCache returns the process-wide no-account cache.
func ProcessNegativeCache() *NegativeCache { return processNegativeCache }

// ProcessFastFail returns the process-wide per-key fast-fail tracker.
func ProcessFastFail() *FastFail { return processFastFail }

// NegativeCache remembers "no available account" per (group, platform,
// model) for a short time (design §9.2). It is in-process and best effort.
type NegativeCache struct {
	mu      sync.Mutex
	entries map[string]negEntry
}

type negEntry struct {
	resp    CachedResponse
	expires time.Time
	groupID int64
}

// NewNegativeCache creates an empty cache.
func NewNegativeCache() *NegativeCache {
	return &NegativeCache{entries: map[string]negEntry{}}
}

// NegativeKey builds the cache key.
func NegativeKey(groupID int64, platform, model string) string {
	return platform + "\x00" + model + "\x00" + itoa64(groupID)
}

// Get returns the cached response for key when still valid.
func (c *NegativeCache) Get(key string, now time.Time) (CachedResponse, bool) {
	if c == nil {
		return CachedResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return CachedResponse{}, false
	}
	if !now.Before(e.expires) {
		delete(c.entries, key)
		return CachedResponse{}, false
	}
	return e.resp, true
}

// Put stores a no-account result for ttl.
func (c *NegativeCache) Put(key string, groupID int64, resp CachedResponse, ttl time.Duration, now time.Time) {
	if c == nil || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 4096 {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) > 4096 {
			c.entries = map[string]negEntry{}
		}
	}
	c.entries[key] = negEntry{resp: resp, expires: now.Add(ttl), groupID: groupID}
}

// InvalidateGroup drops every entry of a group (account recovered, admin
// change). groupID <= 0 drops everything.
func (c *NegativeCache) InvalidateGroup(groupID int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if groupID <= 0 {
		c.entries = map[string]negEntry{}
		return
	}
	for k, e := range c.entries {
		if e.groupID == groupID {
			delete(c.entries, k)
		}
	}
}

// FastFail tracks identical 503 responses per API key (design §9.3).
type FastFail struct {
	mu   sync.Mutex
	keys map[int64]*fastFailKey
}

type fastFailKey struct {
	signature string
	times     []time.Time
	resp      CachedResponse
	tripUntil time.Time
	lastSeen  time.Time
}

// NewFastFail creates an empty tracker.
func NewFastFail() *FastFail {
	return &FastFail{keys: map[int64]*fastFailKey{}}
}

// Signature identifies "the same 503": status, error type and message.
func Signature(status int, errType, message string) string {
	return itoa64(int64(status)) + "\x00" + errType + "\x00" + message
}

// Observe records a 503 for apiKeyID. It returns true when the key is now
// tripped (threshold identical responses within the window).
func (f *FastFail) Observe(apiKeyID int64, signature string, resp CachedResponse, window time.Duration, threshold int, now time.Time) bool {
	if f == nil || apiKeyID <= 0 || threshold < 1 {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gcLocked(now, window)
	k := f.keys[apiKeyID]
	if k == nil || k.signature != signature {
		k = &fastFailKey{signature: signature}
		f.keys[apiKeyID] = k
	}
	k.lastSeen = now
	cut := now.Add(-window)
	kept := k.times[:0]
	for _, t := range k.times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	k.times = append(kept, now)
	k.resp = resp
	if len(k.times) >= threshold {
		k.tripUntil = now.Add(window)
		return true
	}
	return false
}

// Check returns the response to replay when apiKeyID is tripped.
func (f *FastFail) Check(apiKeyID int64, now time.Time) (CachedResponse, bool) {
	if f == nil || apiKeyID <= 0 {
		return CachedResponse{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.keys[apiKeyID]
	if k == nil || k.tripUntil.IsZero() || !now.Before(k.tripUntil) {
		return CachedResponse{}, false
	}
	if !k.resp.RetryAt.IsZero() && !now.Before(k.resp.RetryAt) {
		// The original condition has had time to clear: let one request
		// through to observe the real state again.
		k.tripUntil = time.Time{}
		k.times = k.times[:0]
		return CachedResponse{}, false
	}
	return k.resp, true
}

// Reset clears a key after a successful request.
func (f *FastFail) Reset(apiKeyID int64) {
	if f == nil {
		return
	}
	f.mu.Lock()
	delete(f.keys, apiKeyID)
	f.mu.Unlock()
}

func (f *FastFail) gcLocked(now time.Time, window time.Duration) {
	if len(f.keys) < 2048 {
		return
	}
	for id, k := range f.keys {
		if now.Sub(k.lastSeen) > 2*window && (k.tripUntil.IsZero() || !now.Before(k.tripUntil)) {
			delete(f.keys, id)
		}
	}
}

var processTurnCap = NewTurnCap()

// ProcessTurnCap returns the process-wide failed-turn tracker.
func ProcessTurnCap() *TurnCap { return processTurnCap }

// TurnCap counts consecutive exhausted failures of one conversation (design
// §9.7). Codex honours Retry-After without limit and retries a failed turn
// up to about 30 times; after the cap the gateway answers with a response
// that ends the turn. It is in-process and best effort.
type TurnCap struct {
	mu      sync.Mutex
	entries map[string]turnCapEntry
}

type turnCapEntry struct {
	count int
	last  time.Time
}

// NewTurnCap creates an empty tracker.
func NewTurnCap() *TurnCap {
	return &TurnCap{entries: map[string]turnCapEntry{}}
}

// TurnKey identifies a conversation: API key, group, model and the sticky
// session hash ("" when the client sends none).
func TurnKey(apiKeyID, groupID int64, model, sessionHash string) string {
	return itoa64(apiKeyID) + "\x00" + itoa64(groupID) + "\x00" + model + "\x00" + sessionHash
}

// Fail records one exhausted failure of key and returns the number of
// consecutive failures; a gap longer than window starts a new streak.
func (t *TurnCap) Fail(key string, window time.Duration, now time.Time) int {
	if t == nil || key == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) > 4096 {
		for k, e := range t.entries {
			if now.Sub(e.last) > window {
				delete(t.entries, k)
			}
		}
		if len(t.entries) > 4096 {
			t.entries = map[string]turnCapEntry{}
		}
	}
	e := t.entries[key]
	if now.Sub(e.last) > window {
		e.count = 0
	}
	e.count++
	e.last = now
	t.entries[key] = e
	return e.count
}

// Reset clears a conversation after a successful request.
func (t *TurnCap) Reset(key string) {
	if t == nil || key == "" {
		return
	}
	t.mu.Lock()
	delete(t.entries, key)
	t.mu.Unlock()
}

// ClampRetryAfter bounds a retry delay to [lo, hi] seconds.
func ClampRetryAfter(d time.Duration, lo, hi int) int {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < lo {
		secs = lo
	}
	if secs > hi {
		secs = hi
	}
	return secs
}

// JitterSeconds spreads a Retry-After of secs by up to ±spread seconds so
// that clients rejected together do not retry together; the result is at
// least 1.
func JitterSeconds(secs, spread int) int {
	if spread > 0 {
		secs += rand.IntN(2*spread+1) - spread
	}
	if secs < 1 {
		secs = 1
	}
	return secs
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// GinKeyErrorResponse is the gin context key under which gateway error
// writers stash the JSON error response they wrote, for per-key fast fail.
const GinKeyErrorResponse = "xy_resilience_error_response"

// GinKeyTurnCapped marks a failure that ended the client's turn (design
// §9.7). It belongs to one conversation and is never replayed by the
// per-key fast fail.
const GinKeyTurnCapped = "xy_resilience_turn_capped"

// ErrorResponse is a stashed gateway JSON error response.
type ErrorResponse struct {
	Status  int
	ErrType string
	Message string
	Body    []byte
}

// UserInsufficientBalanceMessage is the client message for the user's own
// empty balance (design §9.5). It keeps "insufficient balance" so that
// downstream sub2api-style sites still recognise it.
const UserInsufficientBalanceMessage = "Insufficient balance on your xy2api account, please recharge"

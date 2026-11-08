package service

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const openAITerminalRejectionCapacity = 16384

var (
	openAITerminalRequestIDPattern = regexp.MustCompile(`(?i)\b(?:request|req)[ _-]?id\b\s*[:=]\s*["']?[^"'\s,;]+`)
	openAITerminalVariablePattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b[0-9a-f]{16,}\b|[0-9]+`)
)

// One key per account streak and one per rejection's host evidence. Both share
// a bounded LRU; only digests of messages are retained. State is instance-local.
type openAITerminalRejectionKey struct {
	group   int64
	session string
	account int64
	digest  [sha256.Size]byte
}

type openAITerminalRejectionEntry struct {
	key       openAITerminalRejectionKey
	digest    [sha256.Size]byte
	host      string
	streak    int
	crossHost bool
	until     time.Time
}

type openAITerminalRejectionTracker struct {
	mu      sync.Mutex
	entries map[openAITerminalRejectionKey]*list.Element
	lru     list.List
}

// entry is called under mu. Expired or evicted evidence is learned again
// from subsequent terminal outcomes; no raw messages are retained.
func (t *openAITerminalRejectionTracker) entry(key openAITerminalRejectionKey, now time.Time) *openAITerminalRejectionEntry {
	if t.entries == nil {
		t.entries = make(map[openAITerminalRejectionKey]*list.Element)
	}
	element := t.entries[key]
	if element == nil {
		element = t.lru.PushFront(&openAITerminalRejectionEntry{key: key})
		t.entries[key] = element
	} else {
		t.lru.MoveToFront(element)
	}
	entry, _ := element.Value.(*openAITerminalRejectionEntry)
	if !now.Before(entry.until) {
		*entry = openAITerminalRejectionEntry{key: key}
	}
	if t.lru.Len() > openAITerminalRejectionCapacity {
		oldest := t.lru.Back()
		old, _ := oldest.Value.(*openAITerminalRejectionEntry)
		delete(t.entries, old.key)
		t.lru.Remove(oldest)
	}
	return entry
}

func (t *openAITerminalRejectionTracker) record(group int64, session string, account int64, host string, digest [sha256.Size]byte, now time.Time, cfg resilience.SessionSettings) (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	window := time.Duration(cfg.StreakWindowSeconds) * time.Second
	guardWindow := time.Duration(cfg.AvoidSeconds) * time.Second
	if window > guardWindow {
		guardWindow = window
	}
	guard := t.entry(openAITerminalRejectionKey{group: group, session: session, digest: digest}, now)
	if guard.host != "" && guard.host != host {
		guard.crossHost = true
	}
	guard.host, guard.until = host, now.Add(guardWindow)
	streak := t.entry(openAITerminalRejectionKey{group: group, session: session, account: account}, now)
	if streak.digest != digest || streak.host != host {
		streak.streak = 0
	}
	streak.digest, streak.host, streak.until = digest, host, now.Add(window)
	streak.streak++
	n := streak.streak
	avoid := !guard.crossHost && n >= cfg.StreakThreshold
	if n >= cfg.StreakThreshold {
		streak.streak = 0
	}
	return n, avoid
}

func (t *openAITerminalRejectionTracker) reset(group int64, session string, account int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := openAITerminalRejectionKey{group: group, session: session, account: account}
	if element := t.entries[key]; element != nil {
		delete(t.entries, key)
		t.lru.Remove(element)
	}
}

// Called only after the existing HTTP error handler decides to terminate.
// It never changes that response or retries the current request.
func (s *OpenAIGatewayService) noteOpenAITerminalRejection(ctx context.Context, account *Account, status int, headers http.Header, body []byte, forwardErr error) {
	group, session := resilience.StateFrom(ctx).SessionIdentity()
	s.noteOpenAITerminalRejectionForSession(ctx, account, group, session, status, headers, body, forwardErr)
}

func (s *OpenAIGatewayService) noteOpenAITerminalRejectionForSession(ctx context.Context, account *Account, group int64, session string, status int, headers http.Header, body []byte, forwardErr error) {
	cfg := resilience.Current()
	if s == nil || s.resilience == nil || s.resilience.store == nil || account == nil ||
		!account.IsOpenAICompatible() || !cfg.SessionOn() {
		return
	}
	if session == "" || account.ID <= 0 {
		return
	}
	// A guardian request can be scheduled with its parent's binding as a hint.
	// If the request identity aliases that parent key, do not let a child-side
	// rejection delete or avoid the parent's healthy sticky owner.
	if preserveOpenAIGuardianParentBinding(ctx, session) {
		return
	}
	tracker := &s.resilience.terminalRejections
	var failover *UpstreamFailoverError
	if forwardErr == nil || errors.As(forwardErr, &failover) {
		return
	}
	digest, eligible := openAITerminalRejectionDigest(status, headers, body)
	host := accountUpstreamHost(account)
	if !eligible || host == "" {
		tracker.reset(group, session, account.ID)
		return
	}
	n, avoid := tracker.record(group, session, account.ID, host, digest, time.Now(), cfg.Session)
	if !avoid {
		return
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	s.AvoidOpenAIStateAccount(stateCtx, group, session, account.ID)
	const reason = "terminal_rejection_streak"
	resilience.Count(resilience.MetricSessionRebind, group, account.ID, reason)
	slog.Warn("resilience.session_rebind", append(resilience.EventFields(ctx),
		"group_id", group, "session", shortSessionHash(session), "account_id", account.ID,
		"failures", n, "category", string(resilience.CategoryClientError),
		"avoid", (time.Duration(cfg.Session.AvoidSeconds)*time.Second).String(), "reason", reason,
	)...)
}

func openAITerminalRejectionDigest(status int, headers http.Header, body []byte) ([sha256.Size]byte, bool) {
	var empty [sha256.Size]byte
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return empty, false
	}
	message := extractUpstreamErrorMessage(body)
	if message == "" {
		message = gjson.GetBytes(body, "response.error.message").String()
	}
	if message == "" && !gjson.ValidBytes(body) {
		message = string(body)
	}
	if resilience.IsContextWindowError(message, body) {
		return empty, false
	}
	result := classifyUpstreamHTTP(status, headers, body, message)
	if result.Category != resilience.CategoryClientError || result.ContentPolicy() ||
		classifyUpstreamHTTP(http.StatusForbidden, headers, body, message).ContentPolicy() {
		return empty, false
	}
	if hit, _, _ := detectOpenAICyberPolicy(body); hit {
		return empty, false
	}
	code := extractUpstreamErrorCode(body)
	for _, path := range []string{"error.code", "response.error.code", "code"} {
		candidate := gjson.GetBytes(body, path).String()
		if resilience.IsKnownClientErrorCode(candidate) {
			return empty, false
		}
		if code == "" {
			code = candidate
		}
	}
	if resilience.IsKnownClientErrorCode(code) {
		return empty, false
	}
	message = normalizeOpenAITerminalRejectionMessage(message)
	code = strings.ToLower(strings.TrimSpace(code))
	return sha256.Sum256([]byte(strconv.Itoa(status) + "\x00" + code + "\x00" + message)), true
}

func normalizeOpenAITerminalRejectionMessage(message string) string {
	message = strings.Join(strings.Fields(strings.ToLower(SanitizeUpstreamErrorMessage(message))), " ")
	message = openAITerminalRequestIDPattern.ReplaceAllString(message, "request_id=[id]")
	// Normalize all numbers so changing limits or counts do not prevent a
	// session streak from forming. A false merge only avoids one account for
	// AvoidSeconds without penalty, then cross-host evidence stops avoidance;
	// a missed merge can leave a session retrying a numbered refusal indefinitely.
	return openAITerminalVariablePattern.ReplaceAllString(message, "[number]")
}

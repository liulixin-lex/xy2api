package service

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const (
	openAISameSiteHoldWindow   = 60 * time.Second
	openAISameSiteHoldDuration = 300 * time.Second
	openAISameSiteHoldMerge    = 3 * time.Second
	openAISameSiteHoldMinPeers = 2
)

type openAISameSiteFailure struct {
	host  string
	last  time.Time
	until time.Time
}

type openAISameSiteHoldEvent struct {
	accountID int64
	peerID    int64
	until     time.Time
}

// openAISameSiteHoldTracker is independent of proxy quarantine. Like the
// existing post-output tracker it bounds the number of tracked accounts.
type openAISameSiteHoldTracker struct {
	mu         sync.RWMutex
	generation uint64
	latest     time.Time
	accounts   map[int64]openAISameSiteFailure
}

func openAISameSiteHost(account *Account) string {
	if account == nil || !account.IsOpenAI() {
		return ""
	}
	u, err := url.Parse(account.GetOpenAIBaseURL())
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

func (t *openAISameSiteHoldTracker) record(account *Account, now time.Time) []openAISameSiteHoldEvent {
	host := openAISameSiteHost(account)
	if host == "" || account.ID <= 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	generation := resilience.SameSiteHoldGeneration()
	if !resilience.Current().SameSiteHoldOn() {
		return nil
	}
	if t.accounts == nil || t.generation != generation {
		t.accounts = make(map[int64]openAISameSiteFailure)
		t.generation = generation
		t.latest = time.Time{}
	}
	// Concurrent failures can acquire the lock out of timestamp order.
	// Evaluate at the newest observed time without moving an older sample.
	if now.After(t.latest) {
		t.latest = now
	}
	evaluatedAt := t.latest
	entry, exists := t.accounts[account.ID]
	if exists && entry.host == host && now.Sub(entry.last) < openAISameSiteHoldMerge {
		return nil
	}
	if !exists && len(t.accounts) >= postOutputTrackedAccounts {
		for id, recent := range t.accounts {
			if now.Sub(recent.last) > openAISameSiteHoldWindow && !now.Before(recent.until) {
				delete(t.accounts, id)
			}
		}
		if len(t.accounts) >= postOutputTrackedAccounts {
			return nil
		}
	}
	entry.host, entry.last = host, now
	t.accounts[account.ID] = entry
	if evaluatedAt.Sub(now) > openAISameSiteHoldWindow {
		return nil
	}
	var peers []int64
	for id, recent := range t.accounts {
		if recent.host == host && evaluatedAt.Sub(recent.last) <= openAISameSiteHoldWindow {
			peers = append(peers, id)
		}
	}
	if len(peers) < openAISameSiteHoldMinPeers {
		return nil
	}
	var events []openAISameSiteHoldEvent
	for _, id := range peers {
		recent := t.accounts[id]
		if evaluatedAt.Before(recent.until) {
			continue
		}
		peerID := account.ID
		if id == peerID {
			for _, candidate := range peers {
				if candidate != id && (peerID == id || candidate < peerID) {
					peerID = candidate
				}
			}
		}
		recent.until = evaluatedAt.Add(openAISameSiteHoldDuration)
		t.accounts[id] = recent
		events = append(events, openAISameSiteHoldEvent{id, peerID, recent.until})
	}
	if resilience.SameSiteHoldGeneration() != generation || !resilience.Current().SameSiteHoldOn() {
		return nil
	}
	return events
}

func (t *openAISameSiteHoldTracker) held(account *Account, now time.Time) bool {
	return now.Before(t.until(account, now))
}

func (t *openAISameSiteHoldTracker) until(account *Account, now time.Time) time.Time {
	if account == nil || !account.IsOpenAI() || !resilience.Current().SameSiteHoldOn() {
		return time.Time{}
	}
	generation := resilience.SameSiteHoldGeneration()
	t.mu.RLock()
	entry := t.accounts[account.ID]
	held := t.generation == generation && now.Before(entry.until)
	t.mu.RUnlock()
	if held && resilience.Current().SameSiteHoldOn() && resilience.SameSiteHoldGeneration() == generation {
		return entry.until
	}
	return time.Time{}
}

func (r *ResilienceService) noteOpenAISameSiteStreamFailure(account *Account, now time.Time) {
	if r == nil {
		return
	}
	events := r.sameSiteHold.record(account, now)
	if len(events) == 0 {
		return
	}
	sum := sha256.Sum256([]byte(openAISameSiteHost(account)))
	for _, event := range events {
		slog.Warn("resilience.same_site_hold",
			"host_hash", hex.EncodeToString(sum[:8]),
			"account_id", event.accountID,
			"peer_account_id", event.peerID,
			"until", event.until,
		)
	}
}

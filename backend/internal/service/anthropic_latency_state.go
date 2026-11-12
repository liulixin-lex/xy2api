package service

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	anthropicLatencyHoldDecayTau    = 5 * time.Minute
	anthropicLatencyHoldDuration    = 3 * time.Minute
	anthropicLatencySlowThreshold   = 10 * time.Second
	anthropicLatencyMinSamples      = 6
	anthropicLatencySlowFraction    = 0.35
	anthropicLatencyStateDefaultMax = 4096
	anthropicLatencyMaxModelBytes   = 512
)

type anthropicLatencyStateKey struct {
	AccountID int64
	Model     string
}

func anthropicLatencyStateModel(model string) string {
	model = strings.TrimSpace(model)
	if len(model) > anthropicLatencyMaxModelBytes {
		return ""
	}
	return strings.ToLower(model)
}

func anthropicLatencyKey(accountID int64, model string) (anthropicLatencyStateKey, bool) {
	model = anthropicLatencyStateModel(model)
	if accountID <= 0 || model == "" {
		return anthropicLatencyStateKey{}, false
	}
	return anthropicLatencyStateKey{AccountID: accountID, Model: model}, true
}

type anthropicLatencyHoldEntry struct {
	weightedSamples float64
	weightedSlow    float64
	lastSample      time.Time
	holdUntil       time.Time
	lastTouched     time.Time
}

type anthropicLatencyHoldState struct {
	mu                      sync.Mutex
	entries                 map[anthropicLatencyStateKey]anthropicLatencyHoldEntry
	maxEntries              int
	latestHoldUntilUnixNano atomic.Int64
}

func newAnthropicLatencyHoldState(maxEntries int) *anthropicLatencyHoldState {
	if maxEntries <= 0 {
		maxEntries = anthropicLatencyStateDefaultMax
	}
	return &anthropicLatencyHoldState{
		entries:    make(map[anthropicLatencyStateKey]anthropicLatencyHoldEntry),
		maxEntries: maxEntries,
	}
}

// anthropicLatencyHoldSnapshot is the scheduler-facing immutable view for one account.
type anthropicLatencyHoldSnapshot struct {
	Held      bool
	HoldUntil time.Time
}

func (s *anthropicLatencyHoldState) record(accountID int64, model string, sample time.Duration, now time.Time) (anthropicLatencyHoldEntry, bool, float64, float64) {
	key, ok := anthropicLatencyKey(accountID, model)
	if s == nil || !ok || sample < 0 {
		return anthropicLatencyHoldEntry{}, false, 0, 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[anthropicLatencyStateKey]anthropicLatencyHoldEntry)
	}
	entry, exists := s.entries[key]
	if !exists {
		s.evictOldestLocked()
	}
	if !entry.lastSample.IsZero() {
		age := now.Sub(entry.lastSample)
		if age > 0 {
			factor := math.Exp(-float64(age) / float64(anthropicLatencyHoldDecayTau))
			entry.weightedSamples *= factor
			entry.weightedSlow *= factor
		}
	}
	entry.weightedSamples++
	if sample >= anthropicLatencySlowThreshold {
		entry.weightedSlow++
	}
	entry.lastSample = now
	entry.lastTouched = now
	started := false
	triggerSamples := entry.weightedSamples
	triggerSlowFraction := float64(0)
	if entry.weightedSamples > 0 {
		triggerSlowFraction = entry.weightedSlow / entry.weightedSamples
	}
	if entry.holdUntil.IsZero() || !now.Before(entry.holdUntil) {
		if entry.weightedSamples >= float64(anthropicLatencyMinSamples) && triggerSlowFraction >= anthropicLatencySlowFraction {
			entry.holdUntil = now.Add(anthropicLatencyHoldDuration)
			for {
				old := s.latestHoldUntilUnixNano.Load()
				if old >= entry.holdUntil.UnixNano() || s.latestHoldUntilUnixNano.CompareAndSwap(old, entry.holdUntil.UnixNano()) {
					break
				}
			}
			entry.weightedSamples = 0
			entry.weightedSlow = 0
			entry.lastSample = time.Time{}
			started = true
		}
	}
	s.entries[key] = entry
	return entry, started, triggerSamples, triggerSlowFraction
}

func (s *anthropicLatencyHoldState) snapshot(accountID int64, model string, now time.Time) anthropicLatencyHoldSnapshot {
	key, ok := anthropicLatencyKey(accountID, model)
	if s == nil || !ok {
		return anthropicLatencyHoldSnapshot{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return anthropicLatencyHoldSnapshot{}
	}
	if !entry.holdUntil.IsZero() && now.Before(entry.holdUntil) {
		entry.lastTouched = now
		s.entries[key] = entry
		return anthropicLatencyHoldSnapshot{Held: true, HoldUntil: entry.holdUntil}
	}
	if !entry.holdUntil.IsZero() {
		entry.holdUntil = time.Time{}
		entry.lastTouched = now
		s.entries[key] = entry
	}
	return anthropicLatencyHoldSnapshot{}
}

func (s *anthropicLatencyHoldState) evictOldestLocked() {
	if len(s.entries) < s.maxEntries {
		return
	}
	var oldestKey anthropicLatencyStateKey
	var oldest time.Time
	found := false
	for key, entry := range s.entries {
		if !found || entry.lastTouched.Before(oldest) {
			oldestKey, oldest, found = key, entry.lastTouched, true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

func partitionAnthropicLatencyHeldSegments[T any](items []T, accountOf func(T) *Account, held map[int64]anthropicLatencyHoldSnapshot, segmentKey func(*Account) int) []T {
	if len(items) < 2 || len(held) == 0 {
		return items
	}
	partition := func(segment []T) []T {
		healthy := make([]T, 0, len(segment))
		heldItems := make([]T, 0, len(segment))
		for _, item := range segment {
			account := accountOf(item)
			if account != nil && held[account.ID].Held {
				heldItems = append(heldItems, item)
			} else {
				healthy = append(healthy, item)
			}
		}
		return append(healthy, heldItems...)
	}
	if segmentKey == nil {
		return partition(items)
	}
	result := make([]T, 0, len(items))
	start := 0
	for start < len(items) {
		tier := segmentKey(accountOf(items[start]))
		end := start + 1
		for end < len(items) && segmentKey(accountOf(items[end])) == tier {
			end++
		}
		result = append(result, partition(items[start:end])...)
		start = end
	}
	return result
}

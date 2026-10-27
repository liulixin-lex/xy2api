package service

import (
	"strings"
	"sync"
	"time"
)

const (
	// openAIModelTransientStreakTTL bounds how long a failure streak survives
	// without a new failure. It exists only so the map does not keep state for
	// account+model pairs that stopped being used; a streak is otherwise reset
	// by recordSuccess alone.
	//
	// It must stay well above the cooldowns. Resetting the streak on a short
	// wall-clock window makes the breaker's sensitivity depend on request rate:
	// a gateway called less often than the window never reaches streak 2, so a
	// broken upstream is never cooled down and every request pays a failed
	// attempt plus a failover before reaching a healthy account. Low-traffic
	// deployments were hit hardest, which is the opposite of what a breaker
	// should do.
	openAIModelTransientStreakTTL     = 30 * time.Minute
	openAIModelTransientShortCooldown = 10 * time.Second
	openAIModelTransientLongCooldown  = 45 * time.Second
	openAIModelTransientDefaultMax    = 4096
	openAIModelTransientMaxModelBytes = 512
)

type openAIAccountModelKey struct {
	AccountID int64
	Model     string
}

type openAIAccountModelTransientEntry struct {
	failureStreak int
	lastFailure   time.Time
	blockUntil    time.Time
	lastTouched   time.Time
	slowStreak    int
	lastSlow      time.Time
}

type openAIAccountModelTransientDecision struct {
	FailureStreak int
	SlowStreak    int
	Cooldown      time.Duration
	BlockUntil    time.Time
}

type openAIAccountModelTransientState struct {
	mu         sync.Mutex
	entries    map[openAIAccountModelKey]openAIAccountModelTransientEntry
	maxEntries int
}

func newOpenAIAccountModelTransientState(maxEntries int) *openAIAccountModelTransientState {
	if maxEntries <= 0 {
		maxEntries = openAIModelTransientDefaultMax
	}
	return &openAIAccountModelTransientState{
		entries:    make(map[openAIAccountModelKey]openAIAccountModelTransientEntry),
		maxEntries: maxEntries,
	}
}

func normalizeOpenAIAccountModelTransientModel(model string) string {
	model = strings.TrimSpace(model)
	if len(model) > openAIModelTransientMaxModelBytes {
		return ""
	}
	return strings.ToLower(model)
}

func openAIAccountModelTransientKey(accountID int64, model string) (openAIAccountModelKey, bool) {
	model = normalizeOpenAIAccountModelTransientModel(model)
	if accountID <= 0 || model == "" {
		return openAIAccountModelKey{}, false
	}
	return openAIAccountModelKey{AccountID: accountID, Model: model}, true
}

func (s *openAIAccountModelTransientState) recordFailure(accountID int64, model string, now time.Time) openAIAccountModelTransientDecision {
	return s.recordFailureKind(accountID, model, now, false)
}

func (s *openAIAccountModelTransientState) recordSlowFailure(accountID int64, model string, now time.Time) openAIAccountModelTransientDecision {
	return s.recordFailureKind(accountID, model, now, true)
}

func (s *openAIAccountModelTransientState) recordFailureKind(accountID int64, model string, now time.Time, slow bool) openAIAccountModelTransientDecision {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return openAIAccountModelTransientDecision{}
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openAIAccountModelKey]openAIAccountModelTransientEntry)
	}
	if s.maxEntries <= 0 {
		s.maxEntries = openAIModelTransientDefaultMax
	}

	entry, exists := s.entries[key]
	if !exists {
		s.evictOldestLocked()
	}
	// The streak is cleared by recordSuccess. Only drop it here when the entry
	// is stale beyond the TTL, or when the clock moved backwards.
	if !exists || entry.lastFailure.IsZero() || now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL || now.Before(entry.lastFailure) {
		entry.failureStreak = 0
	}
	// Concurrent attempts already in flight belong to the current open round.
	// They refresh the success fence without extending or escalating the block.
	if !slow && now.Before(entry.blockUntil) {
		entry.lastFailure, entry.lastTouched = now, now
		s.entries[key] = entry
		return openAIAccountModelTransientDecision{
			FailureStreak: entry.failureStreak, SlowStreak: entry.slowStreak, BlockUntil: entry.blockUntil,
		}
	}
	if slow {
		if entry.lastSlow.IsZero() || now.Before(entry.lastSlow) || now.Sub(entry.lastSlow) > openAIModelTransientStreakTTL {
			entry.slowStreak = 0
		}
		entry.slowStreak++
		entry.lastSlow = now
	} else {
		// Preserve lastSlow so an older in-flight success cannot clear its block.
		entry.slowStreak = 0
	}
	entry.failureStreak++
	entry.lastFailure = now
	entry.lastTouched = now

	cooldown := time.Duration(0)
	switch {
	case !slow && entry.failureStreak >= 6:
		cooldown = 300 * time.Second
	case !slow && entry.failureStreak == 5:
		cooldown = 180 * time.Second
	case !slow && entry.failureStreak == 4:
		cooldown = 90 * time.Second
	case entry.failureStreak >= 3:
		cooldown = openAIModelTransientLongCooldown
	case entry.failureStreak == 2:
		cooldown = openAIModelTransientShortCooldown
	}
	if entry.slowStreak >= 2 {
		n := entry.slowStreak - 2
		slowCooldown := 10 * time.Minute
		if n < 4 {
			slowCooldown = time.Minute << n
		}
		if slowCooldown > cooldown {
			cooldown = slowCooldown
		}
	}
	if cooldown > 0 {
		until := now.Add(cooldown)
		if until.After(entry.blockUntil) {
			entry.blockUntil = until
		}
	}
	s.entries[key] = entry
	return openAIAccountModelTransientDecision{
		FailureStreak: entry.failureStreak,
		SlowStreak:    entry.slowStreak,
		Cooldown:      cooldown,
		BlockUntil:    entry.blockUntil,
	}
}

func (s *openAIAccountModelTransientState) recordSuccess(accountID int64, model string) {
	s.recordSuccessAt(accountID, model, time.Time{})
}

func (s *openAIAccountModelTransientState) resetSlowStreak(accountID int64, model string) {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return
	}
	s.mu.Lock()
	if entry, exists := s.entries[key]; exists {
		entry.slowStreak = 0
		s.entries[key] = entry
	}
	s.mu.Unlock()
}

func (s *openAIAccountModelTransientState) recordSuccessAt(accountID int64, model string, attemptStart time.Time) {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return
	}
	s.mu.Lock()
	entry, ok := s.entries[key]
	if !ok || (!entry.lastSlow.IsZero() && (attemptStart.IsZero() || !attemptStart.After(entry.lastSlow))) ||
		(!attemptStart.IsZero() && !attemptStart.After(entry.lastFailure)) {
		s.mu.Unlock()
		return
	}
	delete(s.entries, key)
	s.mu.Unlock()
}

func (s *openAIAccountModelTransientState) isBlocked(accountID int64, model string, now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	entry, _ := s.readEntry(accountID, model, now)
	return !entry.blockUntil.IsZero() && now.Before(entry.blockUntil)
}

// readEntry shares the streak TTL with scheduling and retry decisions. Expired
// failures must disappear before either reader can act on them.
func (s *openAIAccountModelTransientState) readEntry(accountID int64, model string, now time.Time) (openAIAccountModelTransientEntry, bool) {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return openAIAccountModelTransientEntry{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return openAIAccountModelTransientEntry{}, false
	}
	if !entry.lastFailure.IsZero() && now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL {
		delete(s.entries, key)
		return openAIAccountModelTransientEntry{}, false
	}
	entry.lastTouched = now
	s.entries[key] = entry
	return entry, true
}

func (s *openAIAccountModelTransientState) size() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *openAIAccountModelTransientState) evictOldestLocked() {
	if len(s.entries) < s.maxEntries {
		return
	}
	var oldestKey openAIAccountModelKey
	var oldestTime time.Time
	found := false
	for key, entry := range s.entries {
		if !found || entry.lastTouched.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.lastTouched
			found = true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

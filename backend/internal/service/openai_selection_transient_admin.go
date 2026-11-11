package service

import (
	"sort"
	"time"
)

var _ SchedModelTransientAdmin = (*openAIAccountModelTransientState)(nil)

func (s *openAIAccountModelTransientState) List(accountID int64) []SchedLocalPenalty {
	return s.listAt(accountID, time.Now())
}

func (s *openAIAccountModelTransientState) listAt(accountID int64, now time.Time) []SchedLocalPenalty {
	if s == nil || accountID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]SchedLocalPenalty, 0)
	for key, entry := range s.entries {
		if key.AccountID != accountID || (!entry.lastFailure.IsZero() && now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL) {
			continue
		}
		item := SchedLocalPenalty{Key: "model_transient:" + key.Model, Kind: "model_transient", Scope: "account", Model: key.Model}
		if now.Before(entry.blockUntil) {
			until := entry.blockUntil
			item.Until = &until
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Model < items[j].Model })
	return items
}

func (s *openAIAccountModelTransientState) Clear(accountID int64, model string) bool {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists {
		return false
	}
	delete(s.entries, key)
	return true
}

func (s *openAIAccountModelTransientState) ClearAll(accountID int64) int {
	if s == nil || accountID <= 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for key := range s.entries {
		if key.AccountID == accountID {
			delete(s.entries, key)
			count++
		}
	}
	return count
}

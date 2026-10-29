package service

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const (
	openAIRecentSessionCapacity = 16384
	openAIRecentSessionAccounts = 3
	openAIRecentSessionTTL      = time.Hour
)

type openAIRecentSessionKey struct {
	group   int64
	session string
	model   string // Actual outbound model; never apply account mapping again.
}

type openAIRecentAccountSuccess struct {
	account int64
	at      time.Time
}

type openAIRecentSessionEntry struct {
	key      openAIRecentSessionKey
	accounts []openAIRecentAccountSuccess
}

type openAIRecentSessionSuccess struct {
	mu      sync.Mutex
	entries map[openAIRecentSessionKey]*list.Element
	lru     list.List
}

func (s *OpenAIGatewayService) recentSessionSuccess() *openAIRecentSessionSuccess {
	s.openaiRecentSessionOnce.Do(func() {
		s.openaiRecentSession = &openAIRecentSessionSuccess{entries: make(map[openAIRecentSessionKey]*list.Element)}
	})
	return s.openaiRecentSession
}

// RecordOpenAIRecentSessionSuccess is called only after HTTP forwarding completes.
// outboundModel is the handler's resolved success-report model, already canonical.
// Usage submission also accepts partial failures, so it must not populate this history.
func (s *OpenAIGatewayService) RecordOpenAIRecentSessionSuccess(ctx context.Context, groupID *int64, session string, account *Account, outboundModel string, result *OpenAIForwardResult) {
	if s == nil || account == nil || account.Platform != PlatformOpenAI || result == nil ||
		result.PartialFailure || result.ClientDisconnect || result.OpenAIWSMode || ctx.Err() != nil ||
		session == "" || strings.TrimSpace(outboundModel) == "" {
		return
	}
	st := resilience.StateFrom(ctx)
	if st.ClientGone() || st.EndToEndFirstOutputMs() == nil {
		return
	}
	if outcome := st.RecoveryOutcome(); outcome == nil || *outcome != "completed" {
		return
	}
	key := openAIRecentSessionKey{derefGroupID(groupID), session, strings.TrimSpace(outboundModel)}
	s.recentSessionSuccess().record(key, account.ID, time.Now())
}

func (s *openAIRecentSessionSuccess) record(key openAIRecentSessionKey, accountID int64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element := s.entries[key]
	if element == nil {
		element = s.lru.PushFront(&openAIRecentSessionEntry{key: key})
		s.entries[key] = element
	}
	entry, _ := element.Value.(*openAIRecentSessionEntry)
	accounts := entry.accounts[:0]
	for _, success := range entry.accounts {
		if success.account != accountID && now.Sub(success.at) < openAIRecentSessionTTL {
			accounts = append(accounts, success)
		}
	}
	entry.accounts = append(accounts, openAIRecentAccountSuccess{accountID, now})
	if len(entry.accounts) > openAIRecentSessionAccounts {
		oldest := 0
		for i := range entry.accounts {
			if entry.accounts[i].at.Before(entry.accounts[oldest].at) {
				oldest = i
			}
		}
		entry.accounts = append(entry.accounts[:oldest], entry.accounts[oldest+1:]...)
	}
	s.lru.MoveToFront(element)
	if s.lru.Len() > openAIRecentSessionCapacity {
		oldest := s.lru.Back()
		oldestEntry, _ := oldest.Value.(*openAIRecentSessionEntry)
		delete(s.entries, oldestEntry.key)
		s.lru.Remove(oldest)
	}
}

func (s *openAIRecentSessionSuccess) lastSuccess(key openAIRecentSessionKey, accountID int64, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	element := s.entries[key]
	if element == nil {
		return time.Time{}
	}
	entry, _ := element.Value.(*openAIRecentSessionEntry)
	var at time.Time
	accounts := entry.accounts[:0]
	for _, success := range entry.accounts {
		if now.Sub(success.at) >= openAIRecentSessionTTL {
			continue
		}
		accounts = append(accounts, success)
		if success.account == accountID {
			at = success.at
		}
	}
	entry.accounts = accounts
	if len(accounts) == 0 {
		delete(s.entries, key)
		s.lru.Remove(element)
	} else {
		s.lru.MoveToFront(element)
	}
	return at
}

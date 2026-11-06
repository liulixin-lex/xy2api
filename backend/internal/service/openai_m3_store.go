package service

import (
	"strings"
	"time"
)

const (
	openAIM3AgentMessage   = "agent_message"
	openAIM3FunctionOutput = "function_call_output"
	openAIM3MaxRefBytes    = 256
)

// Bad is deliberately separate from rs/cmp lineage: both the item reference
// and the account are required to reuse evidence on a later turn.
type openAIBadEncryptedSlot struct {
	kind, ref, digest string
}

type openAIBadEncryptedScope struct {
	groupID   int64
	session   string
	accountID int64
}

type openAIBadEncryptedBinding struct {
	slots     map[openAIBadEncryptedSlot]struct{}
	expiresAt time.Time
}

func (s *defaultOpenAIWSStateStore) MarkSessionBadEncryptedSlots(groupID int64, sessionHash string, accountID int64, slots []openAIBadEncryptedSlot, ttl time.Duration) int {
	if sessionHash == "" || accountID <= 0 || len(slots) == 0 {
		return 0
	}
	s.maybeCleanup()
	key := openAIBadEncryptedScope{groupID, sessionHash, accountID}
	now := time.Now()
	s.sessionBadEncryptedMu.Lock()
	defer s.sessionBadEncryptedMu.Unlock()
	if s.sessionBadEncrypted == nil {
		s.sessionBadEncrypted = make(map[openAIBadEncryptedScope]openAIBadEncryptedBinding)
	}
	ensureBindingCapacity(s.sessionBadEncrypted, key, openAIWSStateStoreMaxEntriesPerMap)
	binding := s.sessionBadEncrypted[key]
	if !now.Before(binding.expiresAt) || binding.slots == nil {
		binding.slots = make(map[openAIBadEncryptedSlot]struct{})
	}
	added := 0
	for _, slot := range slots {
		if slot.ref == "" || len(slot.ref) > openAIM3MaxRefBytes || slot.digest == "" {
			continue
		}
		if _, exists := binding.slots[slot]; exists || len(binding.slots) >= openAIWSInvalidEncryptedDigestsPerSession {
			continue
		}
		switch slot.kind {
		case openAIM3AgentMessage:
			slot.kind = openAIM3AgentMessage
		case openAIM3FunctionOutput:
			slot.kind = openAIM3FunctionOutput
		default:
			continue
		}
		slot.ref = strings.Clone(slot.ref)
		binding.slots[slot] = struct{}{}
		added++
	}
	binding.expiresAt = now.Add(normalizeOpenAIWSTTL(ttl))
	s.sessionBadEncrypted[key] = binding
	return added
}

func (s *defaultOpenAIWSStateStore) GetSessionBadEncryptedSlots(groupID int64, sessionHash string, accountID int64) map[openAIBadEncryptedSlot]struct{} {
	if sessionHash == "" || accountID <= 0 {
		return nil
	}
	s.maybeCleanup()
	s.sessionBadEncryptedMu.RLock()
	defer s.sessionBadEncryptedMu.RUnlock()
	binding := s.sessionBadEncrypted[openAIBadEncryptedScope{groupID, sessionHash, accountID}]
	if !time.Now().Before(binding.expiresAt) {
		return nil
	}
	result := make(map[openAIBadEncryptedSlot]struct{}, len(binding.slots))
	for slot := range binding.slots {
		result[slot] = struct{}{}
	}
	return result
}

func (s *defaultOpenAIWSStateStore) cleanupBadEncrypted(now time.Time) {
	s.sessionBadEncryptedMu.Lock()
	defer s.sessionBadEncryptedMu.Unlock()
	scanned := 0
	for key, binding := range s.sessionBadEncrypted {
		if !now.Before(binding.expiresAt) {
			delete(s.sessionBadEncrypted, key)
		}
		scanned++
		if scanned >= openAIWSStateStoreCleanupMaxPerMap {
			break
		}
	}
}

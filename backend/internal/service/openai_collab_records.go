package service

import (
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"strconv"
	"strings"
)

// CollabRecordsCache extends GatewayCache without changing existing stubs.
type CollabRecordsCache interface {
	CollabRecords() resilience.CollabSharedRecordsStore
}

// Explicit access is for an enabled records/ladder path; constructor and off
// paths never call it. The mutex also prevents initialization after shutdown.
func (s *OpenAIGatewayService) CollabRecords() (store *resilience.SafeCollabRecordsStore) {
	if s == nil {
		return nil
	}
	s.collabRecordsMu.Lock()
	defer s.collabRecordsMu.Unlock()
	if s.collabRecordsClosed {
		return nil
	}
	defer func() {
		if recover() != nil {
			s.collabRecordsClosed = true
			store = nil
			resilience.Count("collab_construction_failed", 0, 0, "records")
		}
	}()
	if s.collabRecords == nil {
		var shared resilience.CollabSharedRecordsStore
		if cache, ok := s.cache.(CollabRecordsCache); ok {
			shared = cache.CollabRecords()
			if collabCapabilityNil(shared) {
				s.collabRecordsClosed = true
				resilience.Count("collab_construction_failed", 0, 0, "records")
				return nil
			}
		}
		s.collabRecords = resilience.NewSafeCollabRecordsStore(resilience.NewMemoryCollabRecordsStore(), shared, 1024)
	}
	return s.collabRecords
}
func (s *OpenAIGatewayService) CloseCollabRecords() {
	if s == nil {
		return
	}
	s.collabRecordsMu.Lock()
	s.collabRecordsClosed = true
	store := s.collabRecords
	s.collabRecordsMu.Unlock()
	if store != nil {
		store.Close()
	}
}

// CollabRecordAccount identifies the stable account and selected target.
// Pass the actual resolved base URL; never a credential version or bearer token.
func (s *OpenAIGatewayService) CollabRecordAccount(accountID int64, target string) (account, site resilience.CollabDigest) {
	if accountID <= 0 || strings.TrimSpace(target) == "" {
		return "", ""
	}
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	return s.CollabDigest(resilience.CollabDigestDomainAccount, strconv.FormatInt(accountID, 10), target), s.CollabDigest(resilience.CollabDigestDomainSite, target)
}

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

func (s *OpenAIGatewayService) initCollabRecords() {
	var shared resilience.CollabSharedRecordsStore
	if cache, ok := s.cache.(CollabRecordsCache); ok {
		shared = cache.CollabRecords()
	}
	s.collabRecords = resilience.NewSafeCollabRecordsStore(resilience.NewMemoryCollabRecordsStore(), shared, 1024)
}
func (s *OpenAIGatewayService) CollabRecords() *resilience.SafeCollabRecordsStore {
	if s == nil {
		return nil
	}
	return s.collabRecords
}
func (s *OpenAIGatewayService) CloseCollabRecords() {
	if s != nil && s.collabRecords != nil {
		s.collabRecords.Close()
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

package service

import (
	"context"
	"strings"
	"time"
)

// OpenAIStickyAtomicCache provides first-writer binding and owner-checked cleanup.
// Optional so existing platform caches and test doubles keep their contracts.
type OpenAIStickyAtomicCache interface {
	ClaimSessionAccountID(context.Context, int64, string, int64, time.Duration, bool) (int64, error)
	DeleteSessionAccountIDIfOwner(context.Context, int64, string, int64) error
}

type openAIStickyPromotionKey struct{}
type openAIStickyReconciledKey struct{}

func openAIAtomicStickySession(sessionHash string) bool {
	return strings.HasPrefix(sessionHash, "v2:")
}

func markOpenAIStickySpillover(selection *AccountSelectionResult, spillover bool) *AccountSelectionResult {
	if selection != nil {
		selection.StickyCapacitySpillover = spillover
	}
	return selection
}

func (s *OpenAIGatewayService) deleteStickySessionAccountIDIfOwner(ctx context.Context, groupID *int64, sessionHash string, owner int64) error {
	if s.qualityOwnsSticky(ctx, groupID, sessionHash) || s.cache == nil || owner <= 0 {
		return nil
	}
	if cache, ok := s.cache.(OpenAIStickyAtomicCache); ok && openAIAtomicStickySession(sessionHash) {
		return cache.DeleteSessionAccountIDIfOwner(ctx, derefGroupID(groupID), s.openAISessionCacheKey(sessionHash), owner)
	}
	return s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
}

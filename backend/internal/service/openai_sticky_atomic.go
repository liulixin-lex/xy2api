package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
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
		if reporter, ok := s.cache.(interface {
			DeleteSessionAccountIDIfOwnerAndReport(context.Context, int64, string, int64) (bool, error)
		}); ok {
			deleted, err := reporter.DeleteSessionAccountIDIfOwnerAndReport(ctx, derefGroupID(groupID), s.openAISessionCacheKey(sessionHash), owner)
			if err == nil && deleted {
				resilience.StateFrom(ctx).NoteStickyBypassBindingChange()
			}
			return err
		}
		return cache.DeleteSessionAccountIDIfOwner(ctx, derefGroupID(groupID), s.openAISessionCacheKey(sessionHash), owner)
	}
	return s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
}

// deleteOpenAIStickyBinding preserves the cache's deletion semantics. Only a
// reported deletion counts as a binding change; error-only caches cannot prove
// that a key was present and stay conservative for diagnostics.
func (s *OpenAIGatewayService) deleteOpenAIStickyBinding(ctx context.Context, groupID int64, key string) (bool, error) {
	if reporter, ok := s.cache.(interface {
		DeleteSessionAccountIDAndReport(context.Context, int64, string) (bool, error)
	}); ok {
		return reporter.DeleteSessionAccountIDAndReport(ctx, groupID, key)
	}
	return false, s.cache.DeleteSessionAccountID(ctx, groupID, key)
}

// openAIStickyBindingTTL is the configured binding TTL (or the share
// scheduling TTL), shared by the first binding and its renewals.
func (s *OpenAIGatewayService) openAIStickyBindingTTL(groupID *int64) time.Duration {
	ttl := s.openAIWSSessionStickyTTL()
	if shareTTL, ok := s.shareScheduling.shareBindingTTL(derefGroupID(groupID)); ok {
		ttl = shareTTL
	}
	return ttl
}

// recheckOpenAIStickyOwnerFromDB is recheckSelectedOpenAIAccountFromDB for a
// sticky owner. transient reports a failed read of an existing account; the
// owner is then skipped once and its binding kept.
func (s *OpenAIGatewayService) recheckOpenAIStickyOwnerFromDB(ctx context.Context, account *Account, groupID *int64, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) (latest *Account, transient bool) {
	if account == nil || s.schedulerSnapshot == nil || s.accountRepo == nil {
		return s.recheckSelectedOpenAIAccountFromDB(ctx, account, groupID, platform, requestedModel, requireCompact, requiredCapability), false
	}
	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || latest == nil {
		return nil, err != nil && !errors.Is(err, ErrAccountNotFound)
	}
	latest = s.recheckLatestOpenAIAccount(ctx, latest, groupID, NormalizeOpenAICompatiblePlatform(platform), requestedModel, requireCompact, requiredCapability)
	if latest == nil {
		return nil, false
	}
	if vetoed, _ := openAIProfitControlVetoReason(ctx, latest); vetoed {
		return nil, false
	}
	return latest, false
}

type openAILegacyStickyCapacityKey struct{}

// openAILegacyStickyCapacity carries what the legacy compatibility loop checks
// after selection, so that a sticky borrow already applies it.
type openAILegacyStickyCapacity struct {
	transport OpenAIUpstreamTransport
	image     OpenAIImagesCapability
	retried   bool
}

func withOpenAILegacyStickyCapacity(ctx context.Context, transport OpenAIUpstreamTransport, image OpenAIImagesCapability) context.Context {
	return context.WithValue(ctx, openAILegacyStickyCapacityKey{}, &openAILegacyStickyCapacity{transport: transport, image: image})
}

// openAILegacyStickyCapacityRequest adds the loop's transport and image
// requirements and allows one short sticky retry per selection.
func openAILegacyStickyCapacityRequest(ctx context.Context, req OpenAIAccountScheduleRequest) OpenAIAccountScheduleRequest {
	if state, _ := ctx.Value(openAILegacyStickyCapacityKey{}).(*openAILegacyStickyCapacity); state != nil {
		req.RequiredTransport, req.RequiredImageCapability = state.transport, state.image
		req.SkipStickyCapacityRetry = req.SkipStickyCapacityRetry || state.retried
		state.retried = true
	}
	return req
}

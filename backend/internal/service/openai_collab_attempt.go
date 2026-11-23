package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// CommitCollabResponseIdentity runs only after semantic delivery, through the
// parent's settlement owner. Prelude continuation names the response the client
// saw, so ownership and billing must use that same identity. The returned
// callback starts bounded shared persistence and must run after settlement.
func (s *OpenAIGatewayService) CommitCollabResponseIdentity(c *gin.Context, account *Account, result *OpenAIForwardResult, deliveredID string) (persist func()) {
	// Alias storage cannot prevent the request owner's settlement after output.
	defer func() {
		if recover() != nil {
			s.collabAliasFailures.Add(1)
		}
	}()
	if result == nil || c == nil || c.Request == nil || resilience.StateFrom(c.Request.Context()).DeliveredBytes() == 0 {
		return
	}
	var writes []func(context.Context) error
	if deliveredID != "" && deliveredID != result.ResponseID {
		result.UpstreamResponseID = result.ResponseID
		store := s.collabAliasStore()
		// Readiness was checked before takeover. Never call the adapter again
		// on the delivered request's settlement path.
		if store == nil {
			return
		}
		alias := CollabContinuationAlias{Account: account.ID, Upstream: result.ResponseID, Expires: time.Now().Add(s.openAIWSResponseStickyTTL())}
		scope := collabAliasScope(c)
		if s.collabLocalAliases.Put(context.Background(), scope, deliveredID, alias) != nil {
			s.collabAliasFailures.Add(1)
		}
		result.ResponseID = deliveredID
		writes = append(writes, func(ctx context.Context) error { return store.Put(ctx, scope, deliveredID, alias) })
	}
	if result.CompletedForDelivery() && account != nil && account.ID > 0 {
		store := s.getOpenAIWSStateStore()
		group, id, accountID, ttl := getOpenAIGroupIDFromContext(c), normalizeOpenAIWSResponseID(result.ResponseID), account.ID, normalizeOpenAIWSTTL(s.openAIWSResponseStickyTTL())
		if publisher, ok := store.(CollabResponseIdentityStore); ok && id != "" {
			writes = append(writes, publisher.PrepareCollabResponseAccount(group, id, accountID, ttl))
			if raw, ok := c.Get(openAIHTTPResponseOwnerContextKey); ok {
				if owner, ok := raw.(openAIHTTPResponseOwner); ok && owner.userID > 0 && owner.apiKeyID > 0 {
					writes = append(writes, publisher.PrepareCollabHTTPResponseOwner(group, id, owner.userID, owner.apiKeyID, ttl))
				}
			}
		} else {
			// An unconditional deferred Bind could overwrite a later deletion.
			s.collabAliasFailures.Add(1)
		}
	}
	if len(writes) == 0 {
		return
	}
	var once sync.Once
	persist = func() {
		once.Do(func() {
			if s.collabAliasWrites.Add(1) > 4 {
				s.collabAliasWrites.Add(-1)
				s.collabAliasFailures.Add(1)
				return
			}
			go func() {
				defer s.collabAliasWrites.Add(-1)
				defer func() {
					if recover() != nil {
						s.collabAliasFailures.Add(1)
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), resilience.CollabRecordTimeout)
				defer cancel()
				for _, write := range writes {
					if ctx.Err() != nil {
						break
					}
					if write(ctx) != nil {
						s.collabAliasFailures.Add(1)
					}
				}
			}()
		})
	}
	return persist
}

// Selection may update the rejection scope's binding flag. Keep that mutable
// cell out of the legacy context even though the thread identity is shared.
func WithCollabAttemptIsolation(ctx context.Context) context.Context {
	if scope, ok := ctx.Value(openAITerminalRejectionScopeKey{}).(openAITerminalRejectionScope); ok {
		scope.selectionPreservesBinding = &atomic.Bool{}
		ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, scope)
	}
	return ctx
}

// IsolateCollabAttempt removes mutable forwarding state from the shallow gin
// snapshot. Ingress authentication/routing values remain read-only; attempts
// create fresh repair maps, restoration state, quotas and response observers.
func IsolateCollabAttempt(c *gin.Context) {
	for _, key := range []string{openAIM3ContextKey, openAIRequestRepairContextKey,
		openAICollabResponseKey, codexCollabAttemptContextKey, upstreamResponseModelObserverContextKey,
		openAIFCOReplayKey, openAIFCOAttemptKey, openAIResponsesRejectedFieldRetryBudgetContextKey,
		openAIWebSearchReplayContextKey, openAIStateRejectionAttemptKey, openAIStateRejectionBodyKey} {
		c.Delete(key)
	}
	previous := currentOpsUpstreamEvents(c)
	events := make([]*OpsUpstreamErrorEvent, len(previous))
	for i, event := range previous {
		if event != nil {
			copy := *event
			events[i] = &copy
		}
	}
	if previous != nil {
		c.Set(OpsUpstreamErrorsKey, events)
	}
}

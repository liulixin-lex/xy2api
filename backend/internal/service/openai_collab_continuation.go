package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CollabContinuationScope keeps an alias within its downstream owner and group.
type CollabContinuationScope struct{ Group, User, Key int64 }
type CollabContinuationAlias struct {
	Account  int64
	Upstream string
	Expires  time.Time
}

// CollabContinuationStore must retain aliases for the response affinity TTL.
// Shared deployments must supply a shared store before prelude takeovers run.
type CollabContinuationStore interface {
	Ready() bool
	Put(context.Context, CollabContinuationScope, string, CollabContinuationAlias) error
	Get(context.Context, CollabContinuationScope, string) (CollabContinuationAlias, bool, error)
}

type collabAliasKey struct {
	scope    CollabContinuationScope
	response string
}
type collabMemoryAliases struct {
	mu      sync.Mutex
	entries map[collabAliasKey]CollabContinuationAlias
}

func (*collabMemoryAliases) Ready() bool { return true }
func (s *collabMemoryAliases) Put(_ context.Context, scope CollabContinuationScope, id string, alias CollabContinuationAlias) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[collabAliasKey]CollabContinuationAlias{}
	}
	for key, old := range s.entries {
		if time.Now().After(old.Expires) {
			delete(s.entries, key)
		}
	}
	if len(s.entries) >= openAIWSStateStoreMaxEntriesPerMap {
		return errors.New("response alias capacity")
	}
	s.entries[collabAliasKey{scope, id}] = alias
	return nil
}
func (s *collabMemoryAliases) Get(_ context.Context, scope CollabContinuationScope, id string) (CollabContinuationAlias, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := collabAliasKey{scope, id}
	alias, ok := s.entries[key]
	if ok && time.Now().After(alias.Expires) {
		delete(s.entries, key)
		ok = false
	}
	return alias, ok, nil
}

// SetCollabContinuationStore is called during assembly, before serving requests.
func (s *OpenAIGatewayService) SetCollabContinuationStore(store CollabContinuationStore) {
	s.collabContinuations = store
}
func (s *OpenAIGatewayService) collabAliasStore() CollabContinuationStore {
	if s == nil {
		return nil
	}
	if s.collabContinuations != nil {
		return s.collabContinuations
	}
	if store, ok := s.openaiWSStateStore.(*defaultOpenAIWSStateStore); ok && store.cache == nil {
		return &store.collabAliases
	}
	return nil
}
func (s *OpenAIGatewayService) CanContinueCollabResponse(ctx context.Context) bool {
	store := s.collabAliasStore()
	if store == nil {
		return false
	}
	// The concrete process-local store has no external readiness dependency.
	if _, local := store.(*collabMemoryAliases); local {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	if s.collabAliasReads.Add(1) > 4 {
		s.collabAliasReads.Add(-1)
		s.collabAliasFailures.Add(1)
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, resilience.CollabRecordTimeout)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		defer s.collabAliasReads.Add(-1)
		ready := false
		defer func() {
			if recover() != nil {
				s.collabAliasFailures.Add(1)
			}
			done <- ready
		}()
		ready = store.Ready()
	}()
	select {
	case ready := <-done:
		return ready
	case <-ctx.Done():
		s.collabAliasFailures.Add(1)
		return false
	}
}
func collabAliasScope(c *gin.Context) CollabContinuationScope {
	scope := CollabContinuationScope{Group: getOpenAIGroupIDFromContext(c), Key: getAPIKeyIDFromContext(c)}
	if raw, ok := c.Get(openAIHTTPResponseOwnerContextKey); ok {
		if owner, ok := raw.(openAIHTTPResponseOwner); ok {
			scope.User, scope.Key = owner.userID, owner.apiKeyID
		}
	}
	if scope.User > 0 {
		scope.Key = 0
	} // Keys of the same owner remain interoperable.
	return scope
}
func (s *OpenAIGatewayService) restoreCollabContinuation(ctx context.Context, c *gin.Context, account *Account, body []byte) (restored []byte, restoreErr error) {
	if !resilience.StateFrom(ctx).CollabSettingsSnapshot().CollabLadderOn() {
		return body, nil
	}
	defer func() {
		if recover() != nil {
			restored, restoreErr = body, nil
		}
	}()
	store := s.collabAliasStore()
	if store == nil {
		return body, nil
	}
	id := gjson.GetBytes(body, "previous_response_id").String()
	if id == "" {
		return body, nil
	}
	scope := collabAliasScope(c)
	alias, ok, err := s.collabLocalAliases.Get(ctx, scope, id)
	if !ok {
		alias, ok, err = s.readCollabAlias(ctx, store, scope, id)
	}
	if err != nil {
		return body, nil
	}
	if !ok {
		return body, nil
	}
	if account == nil || alias.Account != account.ID {
		return nil, errors.New("response alias account mismatch")
	}
	return sjson.SetBytes(body, "previous_response_id", alias.Upstream)
}

// At most four shared reads may be outstanding even if an adapter ignores
// cancellation. A failed or timed out optional read keeps the original pointer.
func (s *OpenAIGatewayService) readCollabAlias(ctx context.Context, store CollabContinuationStore, scope CollabContinuationScope, id string) (CollabContinuationAlias, bool, error) {
	if s.collabAliasReads.Add(1) > 4 {
		s.collabAliasReads.Add(-1)
		s.collabAliasFailures.Add(1)
		return CollabContinuationAlias{}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, resilience.CollabRecordTimeout)
	defer cancel()
	type result struct {
		alias CollabContinuationAlias
		ok    bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer s.collabAliasReads.Add(-1)
		var value result
		defer func() {
			if recover() != nil {
				value = result{err: errors.New("response alias read failed")}
			}
			done <- value
		}()
		value.alias, value.ok, value.err = store.Get(ctx, scope, id)
	}()
	select {
	case value := <-done:
		if value.err != nil {
			s.collabAliasFailures.Add(1)
		}
		return value.alias, value.ok, value.err
	case <-ctx.Done():
		s.collabAliasFailures.Add(1)
		return CollabContinuationAlias{}, false, ctx.Err()
	}
}

// Continuation failures are counters only; they never change delivered output.
func (s *OpenAIGatewayService) CollabContinuationFailures() uint64 {
	return s.collabAliasFailures.Load()
}

package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
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
func (s *OpenAIGatewayService) CanContinueCollabResponse() bool {
	store := s.collabAliasStore()
	return store != nil && store.Ready()
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
func (s *OpenAIGatewayService) restoreCollabContinuation(ctx context.Context, c *gin.Context, account *Account, body []byte) ([]byte, error) {
	store := s.collabAliasStore()
	if store == nil {
		return body, nil
	}
	id := gjson.GetBytes(body, "previous_response_id").String()
	if id == "" {
		return body, nil
	}
	alias, ok, err := store.Get(ctx, collabAliasScope(c), id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return body, nil
	}
	if account == nil || alias.Account != account.ID {
		return nil, errors.New("response alias account mismatch")
	}
	return sjson.SetBytes(body, "previous_response_id", alias.Upstream)
}

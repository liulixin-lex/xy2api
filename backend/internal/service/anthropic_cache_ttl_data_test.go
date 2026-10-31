//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAnthropicCacheTTLKeyCreateUpdateValidation(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"inherit", "off", "5m", "1h"} {
		repo, cache := newCreateLimitStubs()
		svc := newCreateLimitService(repo, cache, 0, 0)
		created, err := svc.Create(ctx, 7, CreateAPIKeyRequest{Name: "TTL", AnthropicCacheTTLMode: &mode})
		require.NoError(t, err)
		require.Equal(t, mode, created.AnthropicCacheTTLMode)
	}
	repo, cache := newCreateLimitStubs()
	created, err := newCreateLimitService(repo, cache, 0, 0).Create(ctx, 7, CreateAPIKeyRequest{Name: "TTL"})
	require.NoError(t, err)
	require.Equal(t, "inherit", created.AnthropicCacheTTLMode)
	for _, mode := range []string{"", "2h", "1H", " 5m"} {
		_, err = newCreateLimitService(repo, cache, 0, 0).Create(ctx, 7, CreateAPIKeyRequest{Name: "TTL", AnthropicCacheTTLMode: &mode})
		require.ErrorIs(t, err, ErrInvalidAnthropicCacheTTLMode)
		svc, _ := newUpdateFieldsAPIKeyService(&APIKey{ID: 1, UserID: 7})
		_, err = svc.Update(ctx, 1, 7, UpdateAPIKeyRequest{AnthropicCacheTTLMode: &mode})
		require.ErrorIs(t, err, ErrInvalidAnthropicCacheTTLMode)
	}
	svc, _ := newUpdateFieldsAPIKeyService(&APIKey{ID: 1, UserID: 7, AnthropicCacheTTLMode: "off"})
	unchanged, err := svc.Update(ctx, 1, 7, UpdateAPIKeyRequest{})
	require.NoError(t, err)
	require.Equal(t, "off", unchanged.AnthropicCacheTTLMode)
	mode := "1h"
	_, err = svc.Update(ctx, 1, 8, UpdateAPIKeyRequest{AnthropicCacheTTLMode: &mode})
	require.ErrorIs(t, err, ErrInsufficientPerms)
}

type ttlAuthRepo struct {
	updateFieldsAPIKeyRepoStub
	lookups int
}

func (r *ttlAuthRepo) GetByKeyForAuth(context.Context, string) (*APIKey, error) {
	r.lookups++
	key := *r.key
	return &key, nil
}
func (r *ttlAuthRepo) Update(_ context.Context, key *APIKey, fields APIKeyUpdateFields) error {
	if fields.AnthropicCacheTTLMode {
		r.key.AnthropicCacheTTLMode = key.AnthropicCacheTTLMode
	}
	return nil
}

type ttlAuthCache struct {
	authCacheStub
	entries map[string][]byte
}

func (c *ttlAuthCache) GetAuthCache(_ context.Context, key string) (*APIKeyAuthCacheEntry, error) {
	raw, ok := c.entries[key]
	if !ok {
		return nil, redis.Nil
	}
	var entry APIKeyAuthCacheEntry
	err := json.Unmarshal(raw, &entry)
	return &entry, err
}
func (c *ttlAuthCache) SetAuthCache(_ context.Context, key string, entry *APIKeyAuthCacheEntry, _ time.Duration) error {
	raw, err := json.Marshal(entry)
	c.entries[key] = raw
	return err
}
func (c *ttlAuthCache) DeleteAuthCache(_ context.Context, key string) error {
	delete(c.entries, key)
	return nil
}

func TestAnthropicCacheTTLAuthColdL1L2AndUpdateInvalidation(t *testing.T) {
	ctx := context.Background()
	repo := &ttlAuthRepo{updateFieldsAPIKeyRepoStub: updateFieldsAPIKeyRepoStub{key: &APIKey{ID: 1, UserID: 7, Key: "sk-ttl", Status: StatusActive, AnthropicCacheTTLMode: "off", User: &User{ID: 7, Status: StatusActive}}}}
	cache := &ttlAuthCache{entries: map[string][]byte{}}
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L1Size: 100, L1TTLSeconds: 60, L2TTLSeconds: 60}}
	svc := NewAPIKeyService(repo, nil, nil, nil, nil, cache, cfg)
	t.Cleanup(func() { svc.authCacheL1.Close() })
	key, err := svc.GetByKey(ctx, "sk-ttl")
	require.NoError(t, err)
	require.Equal(t, "off", key.AnthropicCacheTTLMode)
	svc.authCacheL1.Wait()
	key, err = svc.GetByKey(ctx, "sk-ttl")
	require.NoError(t, err)
	require.Equal(t, "off", key.AnthropicCacheTTLMode)
	require.Equal(t, 1, repo.lookups)
	svc.authCacheL1.Clear()
	key, err = svc.GetByKey(ctx, "sk-ttl")
	require.NoError(t, err)
	require.Equal(t, "off", key.AnthropicCacheTTLMode)
	require.Equal(t, 1, repo.lookups)
	mode := "1h"
	_, err = svc.Update(ctx, 1, 7, UpdateAPIKeyRequest{AnthropicCacheTTLMode: &mode})
	require.NoError(t, err)
	// Ristretto applies Del asynchronously; wait for the local invalidation
	// before asserting the next authentication read observes the new snapshot.
	svc.authCacheL1.Wait()
	key, err = svc.GetByKey(ctx, "sk-ttl")
	require.NoError(t, err)
	require.Equal(t, "1h", key.AnthropicCacheTTLMode)
	require.Equal(t, 2, repo.lookups)
	_, used, err := svc.applyAuthCacheEntry("sk-ttl", &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: 26}})
	require.NoError(t, err)
	require.False(t, used)
}

func TestAnthropicCacheTTLCacheCreationCostConservation(t *testing.T) {
	svc := &BillingService{}
	pricing := &ModelPricing{SupportsCacheBreakdown: true, CacheCreation5mPrice: 2.5, CacheCreation1hPrice: 4}
	for _, tc := range []struct {
		name   string
		tokens UsageTokens
		want   float64
	}{
		{"full", UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 40, CacheCreation1hTokens: 60}, 340},
		{"unknown", UsageTokens{CacheCreationTokens: 100}, 250},
		{"partial", UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 20, CacheCreation1hTokens: 30}, 295},
		{"over", UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 90, CacheCreation1hTokens: 60}, 310},
		{"explicit zero", UsageTokens{CacheCreationTokensPresent: true, CacheCreation5mTokens: 40, CacheCreation1hTokens: 60}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.tokens
			require.Equal(t, tc.want, svc.computeCacheCreationCost(pricing, tc.tokens, 2.5, 1))
			require.Equal(t, original, tc.tokens)
		})
	}
	require.Equal(t, 100*2.5, svc.computeCacheCreationCost(&ModelPricing{}, UsageTokens{CacheCreationTokens: 100}, 2.5, 1))
	billing := newTestBillingService()
	require.Equal(t, 0.1e-6, billing.fallbackPrices["claude-sonnet-5-5"].CacheReadPricePerToken)
	require.Equal(t, 0.1e-6, claudeSonnet55FallbackPricing.CacheReadInputTokenCost)
}

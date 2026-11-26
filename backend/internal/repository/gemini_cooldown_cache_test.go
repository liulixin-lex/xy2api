//go:build unit

package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGeminiCooldownSnapshotRetainsSourceAndExpiresInPlace(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := service.SchedulerBucket{GroupID: 903, Platform: service.PlatformGemini, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	proxyID := int64(8)
	a := service.Account{ID: 901, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, ProxyID: &proxyID, Credentials: map[string]any{"base_url": "https://relay.example.com", "api_key": "fixture-key", "tier_id": "aistudio_free"}, Extra: map[string]any{"model_rate_limits": map[string]any{"gemini-test": map[string]any{"rate_limit_reset_at": time.Now().Add(400 * time.Millisecond).Format(time.RFC3339Nano)}}}}
	generation := "fixture-generation"
	target := fmt.Sprintf("%x", sha256.Sum256([]byte("https://relay.example.com\x00fixture-key\x008")))
	limits := a.Extra["model_rate_limits"].(map[string]any)
	limits["gemini-test"].(map[string]any)["reason"] = "gemini_429:" + generation
	a.Extra[service.GeminiCooldownExtraKey] = map[string]any{"gemini-test": map[string]any{"generation": generation, "target": target}}
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{a}))
	accounts, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, accounts, 1)
	require.True(t, accounts[0].IsGeminiThirdPartyAPIKey())
	require.Equal(t, &proxyID, accounts[0].ProxyID)
	require.False(t, accounts[0].IsSchedulableForModel("gemini-test"))
	require.True(t, accounts[0].IsSchedulableForModel("another-model"))
	accounts[0].Credentials["api_key"] = "new-key"
	require.True(t, accounts[0].IsSchedulableForModel("gemini-test"), "old target cannot block replacement credentials")
	require.Eventually(t, func() bool {
		cached, _, e := cache.GetSnapshot(ctx, bucket)
		return e == nil && len(cached) == 1 && cached[0].IsSchedulableForModel("gemini-test")
	}, 2*time.Second, 10*time.Millisecond)
}

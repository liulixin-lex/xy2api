package service

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCacheProviderSessionTenantAndServingScope(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	const raw = "same-synthetic-provider-session"
	for _, platform := range []string{PlatformAnthropic, PlatformGemini} {
		first := s.cacheProviderSessionScope(platform, 11, 101, raw)
		require.Regexp(t, "^[0-9a-f]{16}$", first.digest)
		require.Equal(t, first, s.cacheProviderSessionScope(platform, 11, 101, raw))
		secondKey := s.cacheProviderSessionScope(platform, 12, 101, raw)
		secondGroup := s.cacheProviderSessionScope(platform, 11, 102, raw)
		require.NotEqual(t, first.digest, secondKey.digest, "same content and group cannot share tenant history")
		require.NotEqual(t, first.digest, secondGroup.digest, "serving-group fallback cannot reuse another group's chain")
		require.NotEqual(t, first, secondKey)
		require.NotEqual(t, first, secondGroup)
		require.NotEqual(t, first, s.cacheProviderSessionScope(platform, 11, 101, raw+"-changed"))
	}
	anthropic := s.cacheProviderSessionScope(PlatformAnthropic, 11, 101, raw)
	gemini := s.cacheProviderSessionScope(PlatformGemini, 11, 101, raw)
	require.NotEqual(t, anthropic.digest, gemini.digest, "platform-session HMAC domains isolate the emitted digests too")
	require.NotEqual(t, anthropic, gemini, "history/LRU keys additionally partition the actual platform")
}

func TestCacheProviderSessionScopeMissingIdentity(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	for _, tc := range []struct {
		platform   string
		key, group int64
		hash       string
	}{
		{PlatformAnthropic, 0, 101, "synthetic"},
		{PlatformAnthropic, -1, 101, "synthetic"},
		{PlatformGemini, 11, -1, "synthetic"},
		{PlatformGemini, 11, 101, ""},
		{PlatformOpenAI, 11, 101, "synthetic"},
		{"", 11, 101, "synthetic"},
	} {
		require.Zero(t, s.cacheProviderSessionScope(tc.platform, tc.key, tc.group, tc.hash))
	}
	for _, missingSecret := range []*OpenAIGatewayService{nil, {}, {cfg: &config.Config{}}} {
		require.Zero(t, missingSecret.cacheProviderSessionScope(PlatformAnthropic, 11, 101, "private-session"),
			"missing HMAC secret must never fall back to a raw identifier")
	}
}

// Authoritative independent vectors bind the algorithm to the contract bytes.
// The negative vector deliberately omits the tenant and serving-group scope.
func TestCacheProviderSessionContractVectors(t *testing.T) {
	type vector struct {
		Platform         string `json:"platform"`
		APIKeyID         int64  `json:"api_key_id"`
		ServingGroupID   int64  `json:"serving_group_id"`
		LocalSessionHash string `json:"local_session_hash"`
		Expected         string `json:"expected"`
		Claimed          string `json:"claimed"`
	}
	var fixture struct {
		Secret  string   `json:"secret"`
		Valid   []vector `json:"valid"`
		Invalid []vector `json:"invalid"`
	}
	raw, err := os.ReadFile("testdata/cache_trace_m2b/session_digest_vectors.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Len(t, fixture.Valid, 4)
	require.Len(t, fixture.Invalid, 1)
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: fixture.Secret}}}
	var history cacheProviderTraceHistory
	caps := CachePolicyCaps{MaxSessions: 4, ChainsPerSession: 3, MaxCheckpoints: 512}
	for _, v := range fixture.Valid {
		t.Run(fmt.Sprintf("%s/%d/%d", v.Platform, v.APIKeyID, v.ServingGroupID), func(t *testing.T) {
			scope := s.cacheProviderSessionScope(v.Platform, v.APIKeyID, v.ServingGroupID, v.LocalSessionHash)
			require.Equal(t, v.Expected, scope.digest)
			token := history.send(scope, uint64(v.APIKeyID), time.Now(), caps)
			require.NotNil(t, token.owner)
			require.Equal(t, v.Expected, history.entries[scope].key.digest,
				"history/LRU must use exactly the emitted digest")
		})
	}
	require.Len(t, history.entries, 4, "all scoped vectors retain independent history")
	for _, v := range fixture.Invalid {
		scope := s.cacheProviderSessionScope(v.Platform, v.APIKeyID, v.ServingGroupID, v.LocalSessionHash)
		require.NotEqual(t, v.Claimed, scope.digest, "unscoped HMAC vector must be rejected")
		require.Contains(t, history.entries, scope, "the scoped key matches its positive vector")
	}
}

package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCacheProviderSessionTenantAndServingScope(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-provider-secret"}}}
	const raw = "same-synthetic-provider-session"
	mac := hmac.New(sha256.New, []byte("synthetic-provider-secret"))
	_, err := mac.Write([]byte("cache-trace-anth-v1\x00" + "11|101|" + raw))
	require.NoError(t, err)
	want := hex.EncodeToString(mac.Sum(nil))[:16]
	for _, platform := range []string{PlatformAnthropic, PlatformGemini} {
		first := s.cacheProviderSessionScope(platform, 11, 101, raw)
		require.Equal(t, want, first.digest, "emitted digest follows the specified domain and scope")
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
	require.Equal(t, anthropic.digest, gemini.digest, "both use the contract's exact HMAC domain")
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

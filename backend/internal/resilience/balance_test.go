package resilience

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTempUnschedTier(t *testing.T) {
	require.Equal(t, TierSoft, TempUnschedTier("transient:TRANSIENT 502"))
	require.Equal(t, TierSoft, TempUnschedTier("auth:403_cooldown (1/3): forbidden"))
	require.Equal(t, TierSoft, TempUnschedTier("OpenAI 403 temporary cooldown (1/3): x"))
	require.Equal(t, TierHard, TempUnschedTier("balance:exhausted: x"))
	require.Equal(t, TierHard, TempUnschedTier("OAuth 401: expired"))
	require.Equal(t, TierHard, TempUnschedTier("cn_balance_low: x"))
	require.Equal(t, TierHard, TempUnschedTier(""))
}

func TestTruncateRuneSafe(t *testing.T) {
	s := strings.Repeat("余", 10)
	got := truncate(s, 7)
	require.True(t, len(got) <= 7)
	require.Equal(t, "余余", got)
}

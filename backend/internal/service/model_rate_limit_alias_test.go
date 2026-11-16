//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistoricalAliasModelCooldown(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	until := now.Add(time.Minute).Truncate(time.Second)
	for _, tc := range []struct {
		name, requested, other, limited string
		want                            bool
	}{
		{"sibling", "ModelX", "ModelX", "other", true},
		{"different_target", "ModelY", "ModelX", "other", false},
		{"case_sensitive", "ModelX", "modelx", "other", false},
		{"wildcard", "ModelX", "ModelX", "other*", false},
		{"requested_alias", "ModelX", "ModelY", "public", true},
		{"canonical", "ModelX", "ModelY", "ModelX", true},
		{"image_isolated", "ModelX", "ModelX", openAIImageGenerationRateLimitKey, false},
		{"iq_isolated", "ModelX", "ModelX", "iq:ModelY", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"model_mapping": map[string]any{
					"public": tc.requested, "other": tc.other, "other*": tc.other,
				}},
			}
			setAccountModelRateLimitSnapshot(account, tc.limited, until, "test", now)
			keys := account.modelRateLimitKeysForRequest(ctx, "public")
			t.Logf("keys=%v limited=%v", keys, account.isModelRateLimitedWithContext(ctx, "public"))
			require.Equal(t, tc.want, account.isModelRateLimitedWithContext(ctx, "public"))
			if tc.want {
				require.InDelta(t, time.Until(until).Seconds(), account.GetModelRateLimitRemainingTimeWithContext(ctx, "public").Seconds(), 1)
			}
		})
	}
	// A4: historical aliases follow their current exact mapping until the
	// original reset, without copying records or remembering the previous target.
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"public": "ModelY", "other": "ModelX"}},
	}
	setAccountModelRateLimitSnapshot(account, "other", until, "test", now)
	require.False(t, account.isModelRateLimitedWithContext(ctx, "public"))
	account.Credentials["model_mapping"].(map[string]any)["other"] = "ModelY"
	require.True(t, account.isModelRateLimitedWithContext(ctx, "public"))
	require.True(t, until.Equal(*account.modelRateLimitResetAt("other")))
	setAccountModelRateLimitSnapshot(account, "other", now.Add(-time.Second), "test", now)
	require.False(t, account.isModelRateLimitedWithContext(ctx, "public"))
	require.Len(t, account.Extra[modelRateLimitsKey], 1)
}

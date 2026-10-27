package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIQGateCanonicalKeysAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, requested, mapped string
		compact                 bool
		want                    []string
	}{
		{"mapping", "public", "gpt-6-astra", false, []string{"iq:gpt-6-astra"}},
		{"passthrough", "gpt-6-astra", "gpt-6-astra", false, []string{"iq:gpt-6-astra"}},
		{"raw-chat", "gpt-6-astra", "gpt-6-astra", false, []string{"iq:gpt-6-astra"}},
		{"compact", "public", "gpt-6-astra", true, []string{"iq:gpt-6-astra", "iq:gpt-5.6-luna"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"model_rate_limits": map[string]any{"iq:gpt-6-astra": map[string]any{}}}}
			if tc.name == "mapping" {
				a.Credentials = map[string]any{"model_mapping": map[string]any{tc.requested: tc.mapped}}
			}
			if tc.compact {
				a.Credentials = map[string]any{"model_mapping": map[string]any{tc.requested: tc.mapped}, "compact_model_mapping": map[string]any{tc.requested: "gpt-5.6-luna"}}
			}
			if got := iqGateKeysForRequest(a, tc.requested, tc.compact); len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"model_rate_limits": map[string]any{"gpt-6-astra": map[string]any{}}}}
	require.Empty(t, iqGateKeysForRequest(a, "gpt-6-astra", false))
}

func TestIQGateRemainingFloorAndLastResort(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"model_rate_limits": map[string]any{"iq:gpt-6-astra": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Second).Format(time.RFC3339)}}}}
	require.GreaterOrEqual(t, a.GetModelRateLimitRemainingTimeWithContext(context.Background(), "gpt-6-astra"), 30*time.Second)
	require.False(t, lastResortModelLimitSoft(withResilienceLastResortGate(context.Background()), a, "gpt-6-astra"))
}

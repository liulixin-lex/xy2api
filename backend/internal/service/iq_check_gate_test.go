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
	ctx := withResilienceLastResortGate(context.Background())
	now := time.Now()
	tests := []struct {
		name           string
		key            string
		resetAt        time.Time
		requested      string
		compactMapped  string
		requireCompact bool
		wantSoft       bool
	}{
		{name: "ordinary short limit", key: "gpt-6-astra", resetAt: now.Add(60 * time.Second), requested: "gpt-6-astra", wantSoft: true},
		{name: "IQ future", key: "iq:gpt-6-astra", resetAt: now.Add(60 * time.Second), requested: "gpt-6-astra"},
		{name: "IQ expired", key: "iq:gpt-6-astra", resetAt: now.Add(-60 * time.Second), requested: "gpt-6-astra"},
		{name: "IQ without reset", key: "iq:gpt-6-astra", requested: "gpt-6-astra"},
		{name: "compact-only IQ", key: "iq:gpt-5.6-luna", resetAt: now.Add(-60 * time.Second), requested: "public", compactMapped: "gpt-5.6-luna", requireCompact: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := map[string]any{}
			if !tc.resetAt.IsZero() {
				entry["rate_limit_reset_at"] = tc.resetAt.Format(time.RFC3339)
			}
			a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"model_rate_limits": map[string]any{tc.key: entry}}}
			if tc.compactMapped != "" {
				a.Credentials = map[string]any{"compact_model_mapping": map[string]any{tc.requested: tc.compactMapped}}
			}
			require.Equal(t, !tc.wantSoft, iqGateBlocksRequest(a, tc.requested, tc.requireCompact))
			require.Equal(t, tc.wantSoft, lastResortModelLimitSoft(ctx, a, tc.requested, tc.requireCompact))
		})
	}

	ordinary := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"model_rate_limits": map[string]any{"gpt-6-astra": map[string]any{"rate_limit_reset_at": now.Add(60 * time.Second).Format(time.RFC3339)}}}}
	require.GreaterOrEqual(t, ordinary.GetModelRateLimitRemainingTimeWithContext(context.Background(), "gpt-6-astra"), 30*time.Second)
}

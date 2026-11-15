//go:build unit

package service

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestReviewAnthropicRelay429HonorsConfiguredRule(t *testing.T) {
	for _, mode := range []string{"relay_match", "relay_no_match", "relay_long_window", "official_window"} {
		t.Run(mode, func(t *testing.T) {
			withResilienceSettings(t, nil)
			repo := &rateLimitAccountRepoStub{}
			block := &runtimeBlockRecorder{}
			svc := &RateLimitService{accountRepo: repo}
			svc.SetAccountRuntimeBlocker(block)
			base := "https://relay.invalid"
			if mode == "official_window" {
				base = "https://api.anthropic.com"
			}
			keyword := "rate limit"
			if mode == "relay_no_match" {
				keyword = "other"
			}
			a := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": base, "temp_unschedulable_enabled": true, "temp_unschedulable_rules": []any{map[string]any{"error_code": 429, "keywords": []any{keyword}, "duration_minutes": 30}},
			}}
			reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
			headers := http.Header{}
			if mode == "relay_long_window" || mode == "official_window" {
				headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
				headers.Set("anthropic-ratelimit-unified-5h-utilization", "1")
				headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprint(reset.Unix()))
			}
			start := time.Now()
			svc.HandleUpstreamError(context.Background(), a, 429, headers, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded, slow down"}}`))
			if mode == "relay_match" || mode == "relay_long_window" {
				require.Equal(t, 1, repo.tempCalls)
				require.Zero(t, repo.rateLimitedCalls)
				require.Len(t, block.until, 1)
				require.WithinDuration(t, start.Add(30*time.Minute), block.until[0], time.Second)
			} else {
				require.Zero(t, repo.tempCalls)
				require.Equal(t, 1, repo.rateLimitedCalls)
				want := start.Add(5 * time.Second)
				if mode == "official_window" {
					want = reset
				}
				require.WithinDuration(t, want, repo.lastRateLimitedAt, time.Second)
			}
		})
	}
}

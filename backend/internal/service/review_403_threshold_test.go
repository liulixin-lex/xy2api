//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestReview403AuthCodesKeepCountedThreshold(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"invalid_api_key","error_code":1010,"type":"cloudflare_error","message":"Invalid API key"}}`,
		`{"error":{"type":"authentication_error","message":"this token cannot access model"}}`,
		`{"error":{"code":"usage_not_included","message":"model not included in plan"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			withResilienceSettings(t, nil)
			for _, platform := range []string{PlatformOpenAI, PlatformKimi, PlatformAnthropic} {
				for _, id := range []int64{1, 2} {
					repo := &rateLimitAccountRepoStub{}
					counter := &openAI403CounterCacheStub{counts: []int64{1, 2, 3}}
					svc := &RateLimitService{accountRepo: repo, openAI403CounterCache: counter}
					account := &Account{ID: id, Platform: platform, Type: AccountTypeAPIKey}
					for attempt := 1; attempt <= 3; attempt++ {
						svc.HandleUpstreamError(context.Background(), account, 403, nil, []byte(body))
						require.Len(t, counter.counts, 3-attempt)
						require.Equal(t, min(attempt, 2), repo.tempCalls)
						if attempt < 3 {
							require.Zero(t, repo.setErrorCalls)
						} else {
							require.Equal(t, 1, repo.setErrorCalls)
						}
					}
				}
			}
			repo := &rateLimitAccountRepoStub{}
			block := &runtimeBlockRecorder{}
			svc := &RateLimitService{accountRepo: repo}
			svc.SetAccountRuntimeBlocker(block)
			start := time.Now()
			svc.HandleUpstreamError(context.Background(), &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, 403, nil, []byte(body))
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Len(t, block.until, 1)
			require.WithinDuration(t, start.Add(time.Duration(resilience.Current().Failure.TransientCooldownSeconds)*time.Second), block.until[0], time.Second)
		})
	}
}

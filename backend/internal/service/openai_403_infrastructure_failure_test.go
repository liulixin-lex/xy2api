//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCounted403InfrastructureFailureStaysTemporary(t *testing.T) {
	for _, seconds := range []int{30, 7} {
		for _, mode := range []string{"missing_counter", "counter_error", "write_error", "success", "threshold"} {
			for _, hard := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%ds/hard=%v", mode, seconds, hard), func(t *testing.T) {
					withResilienceSettings(t, func(settings *resilience.Settings) {
						settings.Failure.TransientCooldownSeconds = seconds
					})
					repo := &rateLimitAccountRepoStub{}
					counter := &openAI403CounterCacheStub{counts: []int64{1}}
					if mode == "write_error" {
						repo.tempErr = errors.New("persistence unavailable")
					}
					if mode == "counter_error" {
						counter.err = errors.New("counter unavailable")
					}
					if mode == "threshold" {
						counter.counts = []int64{3}
					}
					svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
					if mode != "missing_counter" {
						svc.SetOpenAI403CounterCache(counter)
					}
					gateway := &OpenAIGatewayService{}
					svc.SetAccountRuntimeBlocker(gateway)
					account := &Account{ID: 91011, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
					original := time.Now().Add(time.Hour)
					if hard {
						gateway.BlockAccountScheduling(account, original, "auth_error")
					}
					start := time.Now()
					disabled := gatewayWith403Service(gateway, svc, account)
					raw, ok := gateway.openaiAccountRuntimeBlockUntil.Load(account.ID)
					require.True(t, ok)
					until := raw.(time.Time)
					t.Logf("disabled=%v error_writes=%d temp_writes=%d runtime_seconds=%.0f", disabled, repo.setErrorCalls, repo.tempCalls, until.Sub(start).Seconds())
					if mode == "threshold" {
						require.True(t, disabled)
						require.Equal(t, 1, repo.setErrorCalls)
						return
					}
					require.Zero(t, repo.setErrorCalls)
					require.Equal(t, mode == "success", disabled)
					if hard {
						require.Equal(t, original, until)
					} else {
						want := seconds
						if mode == "success" {
							want = 600
						}
						require.InDelta(t, want, until.Sub(start).Seconds(), 1)
					}
				})
			}
		}
	}
}

func gatewayWith403Service(gateway *OpenAIGatewayService, svc *RateLimitService, account *Account) bool {
	gateway.rateLimitService = svc
	return gateway.handleOpenAIAccountUpstreamError(context.Background(), account, 403, nil, []byte(`{"error":{"message":"Access denied"}}`))
}

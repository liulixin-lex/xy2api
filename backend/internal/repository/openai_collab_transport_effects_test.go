//go:build unit

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollabOptionalHTTP2FallbackDoesNotChangeSharedState(t *testing.T) {
	for _, success := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("success_%t/optional_%t", success, optional), func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Gateway.OpenAIHTTP2.Enabled = true
				cfg.Gateway.OpenAIHTTP2.AllowProxyFallbackToHTTP1 = true
				cfg.Gateway.OpenAIHTTP2.FallbackErrorThreshold = 2
				svc := &httpUpstreamService{cfg: cfg}
				proxyKey := "http://proxy.invalid:8080"
				state := svc.getOrCreateOpenAIHTTP2FallbackState(proxyKey)
				observed := time.Now()
				active, _ := state.recordFailure(observed, 2, time.Minute, time.Minute)
				require.False(t, active)
				ctx := resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: optional})
				if success {
					svc.recordOpenAIHTTP2Success(ctx, service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyKey)
				} else {
					svc.recordOpenAIHTTP2Failure(ctx, service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyKey, errors.New("http2: protocol error"))
				}
				if optional {
					require.Equal(t, 1, state.errorCount)
					require.Equal(t, observed, state.windowStart)
					require.True(t, state.fallbackUntil.IsZero())
				} else if success {
					require.Zero(t, state.errorCount)
					require.True(t, state.windowStart.IsZero())
				} else {
					require.True(t, state.fallbackUntil.After(observed))
				}
			})
		}
	}
}

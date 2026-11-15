//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestM7ActualUpstreamTarget(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{}}
	body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	for _, passthrough := range []bool{false, true} {
		state := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), state)
		c, _ := compatStreamContext("/v1/messages", string(body))
		c.Request = c.Request.WithContext(ctx)
		account := &Account{ID: 17, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://EXAMPLE.com:443/prefix?fixture=secret"}}
		var req *http.Request
		var err error
		if passthrough {
			req, _, err = svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "fixture")
		} else {
			req, _, err = svc.buildUpstreamRequest(ctx, c, account, body, "fixture", "apikey", "claude-sonnet-5-5", false, false)
		}
		require.NoError(t, err)
		require.NotNil(t, req)
		require.Equal(t, &resilience.UpstreamAttemptTarget{AccountID: 17, Protocol: "anthropic", Origin: "https://example.com"}, state.UpstreamAttemptTarget())
		failure := &UpstreamFailoverError{}
		copyAnthropicUpstreamTarget(ctx, failure)
		require.Equal(t, state.UpstreamAttemptTarget(), failure.UpstreamTarget)
		account.Credentials["base_url"] = "invalid://"
		if passthrough {
			_, _, err = svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "fixture")
		} else {
			_, _, err = svc.buildUpstreamRequest(ctx, c, account, body, "fixture", "apikey", "claude-sonnet-5-5", false, false)
		}
		require.Error(t, err)
		require.Nil(t, state.UpstreamAttemptTarget())
		copyAnthropicUpstreamTarget(ctx, errors.New("fixture"))
	}
}

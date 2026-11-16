//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOverloadConfiguredModelRuleHTTPAndSSE(t *testing.T) {
	for _, path := range []string{"http", "error", "response.failed", "response.failed_after_output"} {
		for _, mode := range []string{"match", "disabled", "unmatched", "pool", "pool_custom", "custom_excluded", "empty_model", "echo", "write_error"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				withResilienceSettings(t, nil)
				repo := &modelNotFoundAccountRepoStub{}
				if mode == "write_error" {
					repo.modelRateLimitErr = errors.New("model limit persistence failed")
				}
				account := &Account{ID: 906, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Credentials: map[string]any{
						"temp_unschedulable_enabled": mode != "disabled",
						"temp_unschedulable_rules": []any{map[string]any{
							"error_code": 503, "keywords": []any{"overloaded"}, "duration_minutes": 3,
						}},
					},
				}
				if mode == "pool" || mode == "pool_custom" {
					account.Credentials["pool_mode"] = true
				}
				if mode == "pool_custom" || mode == "custom_excluded" {
					account.Credentials["custom_error_codes_enabled"] = true
					account.Credentials["custom_error_codes"] = []any{float64(503)}
					if mode == "custom_excluded" {
						account.Credentials["custom_error_codes"] = []any{float64(401)}
					}
				}
				model := "gpt-6.1-sol"
				if mode == "empty_model" {
					model = ""
				}
				errJSON := `{"code":"server_is_overloaded","message":"server is overloaded"}`
				if mode == "unmatched" || mode == "echo" {
					errJSON = `{"code":"slow_down","message":"slow down"}`
				}
				body := []byte(fmt.Sprintf(`{"error":%s}`, errJSON))
				if mode == "echo" {
					body = []byte(fmt.Sprintf(`{"error":%s,"request":{"message":"server is overloaded"}}`, errJSON))
				}
				if path == "error" {
					body = []byte(fmt.Sprintf(`{"type":"error","error":%s}`, errJSON))
				} else if path != "http" {
					body = []byte(fmt.Sprintf(`{"type":"response.failed","response":{"error":%s},"request":{"message":"server is overloaded"}}`, errJSON))
				}
				blocker := &runtimeBlockRecorder{}
				rates := &RateLimitService{accountRepo: repo}
				rates.SetAccountRuntimeBlocker(blocker)
				gateway := &OpenAIGatewayService{rateLimitService: rates}
				start := time.Now()
				var applied bool
				if path == "http" {
					applied = gateway.handleOpenAIAccountUpstreamError(context.Background(), account, 503, nil, body, model)
				} else {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					if path == "response.failed_after_output" {
						_, _ = c.Writer.WriteString("data: output\n\n")
					}
					status, handled := gateway.handleOpenAIStreamTerminalAccountSideEffectsWithContext(c.Request.Context(), c, account, body, "", nil, model)
					require.Equal(t, 503, status)
					applied = handled
				}
				wantWrite := mode == "match" || mode == "pool_custom" || mode == "write_error"
				require.Equal(t, wantWrite && mode != "write_error", applied)
				require.Zero(t, repo.tempCalls)
				require.Empty(t, blocker.accounts)
				if wantWrite {
					require.Len(t, repo.modelRateLimitCalls, 1)
					call := repo.modelRateLimitCalls[0]
					require.Equal(t, model, call.scope)
					require.WithinDuration(t, start.Add(3*time.Minute), call.resetAt, time.Second)
					setAccountModelRateLimitSnapshot(account, call.scope, call.resetAt, call.reason, start)
					require.True(t, account.isModelRateLimitedWithContext(context.Background(), model))
					require.False(t, account.isModelRateLimitedWithContext(context.Background(), "gpt-6-sol"))
				} else {
					require.Empty(t, repo.modelRateLimitCalls)
				}
			})
		}
	}
	t.Run("401_keeps_auth_contract", func(t *testing.T) {
		repo := &modelNotFoundAccountRepoStub{}
		svc := &RateLimitService{accountRepo: repo}
		account := openAIModelNotFoundTempAccount()
		matched, applied := svc.applyConfiguredModelCooldown(context.Background(), account, 401, []byte(`{"error":{"message":"invalid key"}}`), "gpt-6.1-sol")
		require.False(t, matched)
		require.False(t, applied)
		require.Empty(t, repo.modelRateLimitCalls)
	})
}

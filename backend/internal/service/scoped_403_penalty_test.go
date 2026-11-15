//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestScoped403DoesNotStrikeAccount(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		policy, waf bool
	}{
		{"session", `{"error":{"code":"session_blocked_by_cyber_policy","message":"Access denied"}}`, true, false},
		{"cyber", `{"error":{"code":"cyber_policy","message":"Access denied"}}`, true, false},
		{"policy", `{"error":{"code":"content_policy_violation","message":"Access denied"}}`, true, false},
		{"unknown", `{"error":{"message":"Access denied"}}`, false, false},
		{"echo", `{"error":{"message":"Access denied"},"request":{"code":"session_blocked_by_cyber_policy","policy":"request_check_blocked","error_code":1010,"type":"cloudflare_error"}}`, false, false},
		{"relay_wrapped_text", `{"error":{"message":"error code: 1010","type":"upstream_error","code":"bad_response_status_code"}}`, false, true},
		{"cloudflare_flat", `{"error_code":1010,"error_name":"browser_signature_banned","status":403}`, false, true},
		{"cloudflare_flat_marker", `{"cloudflare_error":true,"error_code":1010,"status":403}`, false, true},
		{"cloudflare_flag", `{"error":{"cloudflare_error":true,"error_code":1010}}`, false, true},
		{"auth_with_waf_text", `{"error":{"code":"invalid_api_key","message":"error code: 1010"}}`, false, false},
		{"json_number", `{"error":{"error_code":1010,"error_name":"browser_signature_banned","type":"cloudflare_error"}}`, false, true},
		{"json_string", `{"error":{"code":"1010","type":"cloudflare_error"}}`, false, true},
		{"json_without_marker", `{"error":{"code":1010,"message":"Access denied"}}`, false, false},
		{"invalid_key", `{"error":{"code":"invalid_api_key","error_code":1010,"type":"cloudflare_error","message":"Invalid API key"}}`, false, false},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				withResilienceSettings(t, nil)
				body := []byte(tc.body)
				source := resilience.SourceHTTP
				if stream {
					if tc.name == "cloudflare_flat" || tc.name == "cloudflare_flat_marker" {
						body = []byte(fmt.Sprintf(`{"type":"response.failed","response":{"error":%s}}`, tc.body))
					} else {
						body = []byte(fmt.Sprintf(`{"type":"response.failed","response":%s}`, tc.body))
					}
					source = resilience.SourceStreamEvent
				}
				classified := classifyUpstreamOutcome(resilience.Outcome{Source: source, StatusCode: 403, Body: body})
				require.Equal(t, tc.policy, classified.ContentPolicy())
				pure := resilience.Classify(resilience.Outcome{Source: source, StatusCode: 403, Body: body}, nil)
				require.Equal(t, tc.policy, pure.ContentPolicy(), "the shared classifier reads the same active error")
				require.Equal(t, tc.waf, isCloudflareBotBlockResponse(body))
				if tc.waf {
					require.Equal(t, resilience.CategoryTransient, classified.Category)
					require.Equal(t, resilience.CategoryTransient, pure.Category)
				}
				repo := &rateLimitAccountRepoStub{}
				counter := &openAI403CounterCacheStub{counts: []int64{1, 2, 3}}
				svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				svc.SetOpenAI403CounterCache(counter)
				blocker := &runtimeBlockRecorder{}
				svc.SetAccountRuntimeBlocker(blocker)
				account := &Account{ID: 912, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)
				if tc.policy || tc.waf {
					require.Len(t, counter.counts, 3)
					require.Zero(t, repo.tempCalls)
					require.Zero(t, repo.setErrorCalls)
					require.Empty(t, blocker.accounts)
				} else {
					require.Len(t, counter.counts, 2)
					require.Equal(t, 1, repo.tempCalls)
					require.Zero(t, repo.setErrorCalls)
				}
				if tc.policy {
					failure := &UpstreamFailoverError{StatusCode: 403, ResponseBody: body}
					require.False(t, ClassifyFailoverError(failure).Category.SwitchAccount())
				}
			})
		}
	}
	for _, body := range []string{"error code: 1010", "<html>error code: 1010</html>"} {
		t.Run(body, func(t *testing.T) {
			require.Equal(t, resilience.CategoryTransient, classifyUpstreamHTTP(403, nil, []byte(body), "").Category)
			require.Equal(t, resilience.CategoryTransient, resilience.Classify(resilience.Outcome{StatusCode: 403, Body: []byte(body)}, nil).Category)
			require.True(t, isCloudflareBotBlockResponse([]byte(body)))
			repo := &rateLimitAccountRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			counter := &openAI403CounterCacheStub{counts: []int64{1}}
			svc.SetOpenAI403CounterCache(counter)
			svc.HandleUpstreamError(context.Background(), &Account{ID: 912, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, 403, nil, []byte(body))
			require.Len(t, counter.counts, 1)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
}

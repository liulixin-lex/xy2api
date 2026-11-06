//go:build unit

package service

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWrappedRequestRejectionStopsBeforeForbidden(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct{ name, body string }{
		{"state", `{"error":{"code":"client_tool_continuation_unavailable","message":"try again"}}`},
		{"context wording", `{"error":{"message":"maximum context length exceeded"}}`},
		{"cyber", `{"error":{"code":"cyber_policy","message":"blocked"}}`},
		{"content safety", `{"error":{"code":"content_policy_violation","message":"request rejected"}}`},
		{"content safety wording", `{"error":{"message":"Request rejected by preflight safety checks"}}`},
	} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, status), func(t *testing.T) {
				c, recorder := newNonStreamingFailoverContext(t)
				account := newNonStreamingFailoverAccount()
				account.Credentials["pool_mode"] = false
				repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
				svc := newNonStreamingFailoverService()
				svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				counter := &countingOpenAI403CounterCache{}
				svc.rateLimitService.SetOpenAI403CounterCache(counter)
				payload := []byte(fmt.Sprintf(`{"type":"proxy.upstream_error","status":%d,"upstream_body":%s}`, status, tc.body))

				failure := svc.nonStreamingTerminalFailureFailover(c, newNonStreamingSSEResponse(), account, false, "error", "error", payload, "relay rejected", "model")

				require.NotNil(t, failure)
				require.Equal(t, status, failure.WrappedStatus)
				require.JSONEq(t, tc.body, string(failure.ResponseBody))
				require.Equal(t, GatewayFailureScopeRequest, failure.Scope)
				require.NotEqual(t, OpenAIStreamForbiddenReason, failure.Reason)
				require.False(t, failure.ShouldRetryNextAccount())
				require.False(t, failure.RetryableOnSameAccount)
				require.False(t, failure.IsCredentialFailure())
				require.False(t, failure.ShouldReportAccountScheduleFailure())
				require.Equal(t, resilience.CategoryClientError, ClassifyFailoverError(failure).Category)
				_, cooled := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "model", time.Now())
				require.False(t, cooled)
				require.Zero(t, counter.increments)
				require.Zero(t, repo.errorCalls)
				require.Zero(t, repo.tempCalls)
				require.Zero(t, repo.modelRateLimitCalls)
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
				require.Empty(t, recorder.Body.String())
			})
		}
	}
}

//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// zhipuPreflightBody is the 403 a Zhipu account answered for one user's
// content in production (49 times in three hours).
const zhipuPreflightBody = `{"code":"request_check_blocked","message":"Request rejected by preflight safety checks"}`

func TestContentPolicy403TakesNoStrike(t *testing.T) {
	// R4: one user's refused content must not cool down or error the
	// account; a single-account group used to go offline after three.
	bodies := []string{
		zhipuPreflightBody,
		// A sub2api-style relay's own moderation answer.
		`{"error":{"type":"invalid_request_error","code":"content_policy_violation","message":"请求内容违反本分组安全策略（敏感话题）"}}`,
	}
	for _, platform := range []string{PlatformZhipu, PlatformOpenAI, PlatformAnthropic} {
		for _, body := range bodies {
			svc, repo, account, _ := newPenaltyFixture(t, platform)
			counter := &resilience403Counter{}
			svc.SetOpenAI403CounterCache(counter)
			for i := 0; i < 3; i++ {
				disable := svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(body))
				require.False(t, disable, "%s %s", platform, body)
			}
			require.Zero(t, counter.n, "%s: no 403 strike", platform)
			require.Zero(t, repo.tempCalls, "%s: no cooldown", platform)
			require.Zero(t, repo.errorCalls, "%s: no error state", platform)
		}
	}
}

func TestContentPolicyWordingOnAccountBanStillStrikes(t *testing.T) {
	// A refusal that names the account is an account failure.
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	counter := &resilience403Counter{}
	svc.SetOpenAI403CounterCache(counter)
	body := []byte(`{"error":{"type":"permission_error","message":"Your account has been suspended for content policy violations"}}`)
	require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body))
	require.Equal(t, int64(1), counter.n)
	require.Equal(t, 1, repo.tempCalls)
}

func TestContentPolicy403SwitchOffKeepsLegacyPenalty(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	svc, repo, account, _ := newPenaltyFixtureKeepSettings(t, PlatformZhipu)
	counter := &resilience403Counter{}
	svc.SetOpenAI403CounterCache(counter)
	require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(zhipuPreflightBody)))
	require.Positive(t, repo.tempCalls+repo.errorCalls, "0.3.0 penalises the account")
}

func TestClassifyFailoverErrorContentPolicy(t *testing.T) {
	withResilienceSettings(t, nil)
	res := ClassifyFailoverError(&UpstreamFailoverError{StatusCode: http.StatusForbidden, ResponseBody: []byte(zhipuPreflightBody), RetryableOnSameAccount: true})
	require.Equal(t, resilience.CategoryClientError, res.Category)
	require.True(t, res.ContentPolicy())

	res = ClassifyFailoverError(&UpstreamFailoverError{StatusCode: http.StatusForbidden, ResponseBody: []byte(`{"error":{"message":"forbidden"}}`)})
	require.Equal(t, resilience.CategoryAuth, res.Category)
	require.False(t, res.ContentPolicy())
}

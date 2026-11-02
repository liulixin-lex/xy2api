//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type subscriptionSharingFailoverRepo struct {
	openAIImagesFailoverAccountRepo
	pausedID int64
	reason   string
}

func (r *subscriptionSharingFailoverRepo) SetBalancePauseTiered(_ context.Context, accountID int64, pause time.Duration, _, startedAt time.Time, evidence string) (service.BalancePauseWrite, error) {
	r.pausedID = accountID
	r.reason = resilience.BalancePauseReasonAtStep(1, evidence)
	return service.BalancePauseWrite{Changed: true, Step: 1, Reason: r.reason, Until: startedAt.Add(pause)}, nil
}

type subscriptionSharingFailoverUpstream struct {
	service.HTTPUpstream
	accountIDs []int64
}

func (u *subscriptionSharingFailoverUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.accountIDs = append(u.accountIDs, accountID)
	body := "event: response.failed\n" + `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","type":"invalid_request_error","message":"The ChatGPT user has reached their Subscription Sharing usage limit."}}}` + "\n\n"
	if accountID == 2 {
		body = `data: {"type":"response.output_text.delta","delta":"healthy reply"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_ok","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestOpenAIResponsesSubscriptionSharingFailsOverBeforeOutput(t *testing.T) {
	previous := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(previous) })
	gin.SetMode(gin.TestMode)
	repo := &subscriptionSharingFailoverRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: []service.Account{
		{ID: 1, Name: "exhausted", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"api_key": "test-key"}},
		{ID: 2, Name: "healthy", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"api_key": "test-key"}},
	}}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	upstream := &subscriptionSharingFailoverUpstream{}
	rl := service.NewRateLimitService(repo, nil, cfg, nil, nil)
	rl.SetResilienceService(service.NewResilienceService(nil, nil))
	gw := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, rl, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gw, service.NewConcurrencyService(nil), billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = 10
	c, recorder := newOpenAIResponsesFailoverTestContext(t, nil)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Responses(c)

	require.Equal(t, []int64{1, 2}, upstream.accountIDs)
	require.Equal(t, int64(1), repo.pausedID)
	require.True(t, resilience.IsBalancePause(repo.reason))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "healthy reply")
	require.Contains(t, recorder.Body.String(), "response.completed")
	require.NotContains(t, recorder.Body.String(), "subscription_sharing_usage_limit_exceeded")
	require.NotContains(t, recorder.Body.String(), "response.failed")
}

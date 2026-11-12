//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type upstreamWaitAdvisor struct {
	fakeRetryAdvisor
	advice resilience.RetryAdvice
	calls  atomic.Int64
}

func (a *upstreamWaitAdvisor) RetryAdvice(context.Context, *int64, string) resilience.RetryAdvice {
	a.calls.Add(1)
	return a.advice
}

func upstreamWaitContext(t *testing.T, endpoint string, heartbeat bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, rec := newGinContextForEndpoint(t, endpoint)
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
	resilience.StateFrom(c.Request.Context()).SetGroup(20)
	if heartbeat {
		n, err := c.Writer.WriteString(":\n\n")
		require.NoError(t, err)
		recordGatewayStreamHeartbeat(c, n)
		c.Writer.Flush()
	}
	return c, rec
}

func upstreamWaitFailure(t *testing.T, body string) gjson.Result {
	t.Helper()
	require.Equal(t, 1, strings.Count(body, "event: response.failed\n"), body)
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "response.failed" {
			return gjson.Get(data, "response.error")
		}
	}
	t.Fatal("missing failed event")
	return gjson.Result{}
}

func TestUpstreamRetryWaitFallback(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct{ name, value, want string }{
		{"eight seconds", "8", "8"},
		{"minimum", "1", "2"},
		{"ceiling", "60", "10"},
		{"future date", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), "10"},
		{"missing", "", ""},
		{"zero", "0", ""},
		{"negative", "-1", ""},
		{"invalid", "tomorrow", ""},
		{"past date", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), ""},
		{"overflow", "18446744073709551616", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := upstreamWaitContext(t, EndpointResponses, true)
			copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {tc.value}})
			advisor := &upstreamWaitAdvisor{advice: resilience.RetryAdvice{Source: "unknown"}}
			status := applyExhaustedRetryHint(c, advisor, http.StatusServiceUnavailable)
			(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "server_error", "Busy", true)
			failed := upstreamWaitFailure(t, rec.Body.String())
			require.Equal(t, tc.want, failed.Get("headers.retry-after").String())
			if tc.want == "" {
				require.Equal(t, "Busy", failed.Get("message").String())
			} else {
				require.Equal(t, "Busy. Please try again in "+tc.want+"s.", failed.Get("message").String())
				require.Equal(t, "recovery", resilience.RetryAdviceFrom(c.Request.Context()).Source)
			}
			require.Equal(t, int64(1), advisor.calls.Load())
		})
	}
}

type upstreamWaitAccounts struct {
	service.AccountRepository
	accounts []service.Account
	reads    atomic.Int64
	forbid   atomic.Bool
}

func (r *upstreamWaitAccounts) ListByGroup(context.Context, int64) ([]service.Account, error) {
	r.reads.Add(1)
	if r.forbid.Load() {
		return nil, fmt.Errorf("terminal advice must not read the repository")
	}
	return append([]service.Account(nil), r.accounts...), nil
}

// Wire the actual advisor and warm its real cache through the public legacy
// advisor. No test-only cache API or positive-advice stub can hide RA-1 here.
func TestUpstreamRetryWaitFromRealSnapshotThroughHandler(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, kind := range []string{"fresh failed", "missing", "expired", "healthy alternative"} {
		t.Run(kind, func(t *testing.T) {
			c, rec := upstreamWaitContext(t, EndpointResponses, true)
			group := int64(20)
			c.Set("api_key", &service.APIKey{GroupID: &group})
			repo := &upstreamWaitAccounts{accounts: []service.Account{{
				ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true,
			}}}
			if kind == "healthy alternative" {
				other := repo.accounts[0]
				other.ID = 2
				repo.accounts = append(repo.accounts, other)
			}
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			t.Cleanup(gateway.StopOpenAICodexTicketHarvester)
			previous := service.Resilience()
			runtime := service.ProvideResilienceService(nil, nil, nil, nil, repo, nil, nil, &service.GatewayService{}, gateway)
			runtime.Stop()
			t.Cleanup(func() { service.SetResilienceInstance(previous) })
			if kind != "missing" {
				require.Positive(t, gateway.NoAccountRetryAfter(c.Request.Context(), &group, "model"))
			}
			if kind == "expired" {
				time.Sleep(2100 * time.Millisecond) // The production snapshot TTL is two seconds.
			}
			resilience.StateFrom(c.Request.Context()).MarkAccountFailed(1)
			want := "8"
			if kind == "healthy alternative" {
				require.Equal(t, 1, gateway.RetryAdvice(c.Request.Context(), &group, "model").Seconds)
				want = "1"
			} else {
				require.Equal(t, "unknown", gateway.RetryAdvice(c.Request.Context(), &group, "model").Source)
			}
			reads := repo.reads.Load()
			repo.forbid.Store(true)
			failure := &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable,
				ResponseHeaders:        http.Header{"Retry-After": {"8"}},
				ResponseBody:           []byte(`{"error":{"code":"server_is_overloaded","type":"overloaded_error","message":"Overloaded"}}`),
				RequestScopedTransient: true, ClientStatusCode: http.StatusServiceUnavailable, ClientMessage: "Busy",
			}
			(&OpenAIGatewayHandler{gatewayService: gateway}).handleFailoverExhausted(c, failure, true)
			failed := upstreamWaitFailure(t, rec.Body.String())
			require.Equal(t, want, failed.Get("headers.retry-after").String())
			require.Equal(t, "Busy. Please try again in "+want+"s.", failed.Get("message").String())
			require.Equal(t, reads, repo.reads.Load(), "terminal path must remain local")
		})
	}
}

func TestUpstreamRetryWaitPreservesAdvicePriority(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, advice := range []resilience.RetryAdvice{
		{Seconds: 1, Source: "healthy_candidate"}, {Seconds: 3, Source: "recovery"},
		{Suppress: true, Source: "turn_cap"}, {Suppress: true, Source: "balance_terminal"},
		{Suppress: true, Source: "content_rejection"}, {Suppress: true, Source: "config_rejection"},
	} {
		t.Run(advice.Source, func(t *testing.T) {
			c, rec := upstreamWaitContext(t, EndpointResponses, true)
			copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
			advisor := &upstreamWaitAdvisor{advice: advice}
			if advice.Suppress {
				resilience.SetRetryAdvice(c.Request.Context(), advice)
			}
			status := applyExhaustedRetryHint(c, advisor, http.StatusServiceUnavailable)
			(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "server_error", "Busy", true)
			failed := upstreamWaitFailure(t, rec.Body.String())
			require.Equal(t, advice.Headers()["retry-after"], failed.Get("headers.retry-after").String())
			require.Equal(t, advice.Message("Busy"), failed.Get("message").String())
			require.Equal(t, advice, resilience.RetryAdviceFrom(c.Request.Context()))
		})
	}
}

func TestUpstreamRetryWaitCannotReviveTerminalErrors(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		status int
		kind   string
	}{
		{400, "invalid_request_error"}, {400, "context_length_exceeded"}, {404, "model_not_found"},
		{503, "billing_service_error"}, {403, errTypeInsufficientBalance}, {400, codeContentPolicyViolation},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			c, rec := upstreamWaitContext(t, EndpointResponses, true)
			copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
			applyExhaustedRetryHint(c, &upstreamWaitAdvisor{}, http.StatusServiceUnavailable)
			(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, tc.status, tc.kind, "Rejected", true)
			failed := upstreamWaitFailure(t, rec.Body.String())
			require.False(t, failed.Get("headers").Exists())
			require.Equal(t, "Rejected", failed.Get("message").String())
			require.True(t, resilience.RetryAdviceFrom(c.Request.Context()).Suppress)
		})
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		t.Run(fmt.Sprintf("upstream %d mapped to 502", status), func(t *testing.T) {
			c, rec := upstreamWaitContext(t, EndpointResponses, true)
			failure := &service.UpstreamFailoverError{
				StatusCode: status, ResponseHeaders: http.Header{"Retry-After": {"8"}},
				ResponseBody: []byte(`{"error":{"type":"invalid_request_error","message":"Rejected"}}`),
			}
			(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failure, true)
			failed := upstreamWaitFailure(t, rec.Body.String())
			require.False(t, failed.Get("headers").Exists(), "mapping a client error to 502 must not create recovery evidence")
			require.NotContains(t, failed.Get("message").String(), "try again in")
		})
	}
}

func TestUpstreamRetryWaitIgnoresUnprovenAndCanceledHints(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, kind := range []string{"local default", "previous upstream", "invalid final upstream", "local overwrites upstream", "legacy overwrites upstream", "canceled", "legacy unknown"} {
		t.Run(kind, func(t *testing.T) {
			c, rec := upstreamWaitContext(t, EndpointResponses, true)
			switch kind {
			case "local default":
				c.Header("Retry-After", "30")
			case "previous upstream", "invalid final upstream":
				copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
				var headers http.Header
				if kind == "invalid final upstream" {
					headers = http.Header{"Retry-After": {"invalid"}}
				}
				copyFailoverRetryAfter(c, http.StatusServiceUnavailable, headers)
			case "canceled":
				copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			case "local overwrites upstream":
				copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
				c.Header("Retry-After", "30")
			case "legacy overwrites upstream":
				copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"30"}})
				setNoAccountRetryAfter(c, &upstreamWaitAdvisor{}, nil, "model")
			}
			status := applyExhaustedRetryHint(c, &upstreamWaitAdvisor{}, http.StatusServiceUnavailable)
			(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "server_error", "Busy", true)
			failed := upstreamWaitFailure(t, rec.Body.String())
			require.False(t, failed.Get("headers").Exists())
			require.Equal(t, "Busy", failed.Get("message").String())
		})
	}
}

func TestUpstreamRetryWaitDisabledPreservesLegacy(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.FailedFrameRetryAfter = false })
	c, rec := upstreamWaitContext(t, EndpointResponses, true)
	copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
	advisor := &upstreamWaitAdvisor{}
	installResilienceRetryAdvice(c, advisor, nil, "model")
	status := applyExhaustedRetryHint(c, advisor, http.StatusServiceUnavailable)
	(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "server_error", "Busy", true)
	failed := upstreamWaitFailure(t, rec.Body.String())
	require.False(t, failed.Get("headers").Exists())
	require.Equal(t, "Busy. Please try again in 8s.", failed.Get("message").String())
	require.Zero(t, advisor.calls.Load())
}

func TestUpstreamRetryWaitReachesEarlierServiceContext(t *testing.T) {
	withHandlerResilience(t, nil)
	c, rec := upstreamWaitContext(t, EndpointResponses, false)
	installResilienceRetryAdvice(c, &upstreamWaitAdvisor{}, nil, "model")
	early := c.Request.Context()
	service.InstallDeliveryTracking(c, time.Now())
	stop := service.StartPreOutputKeepalive(c, service.PreOutputProtocolResponses, 0, time.Hour)
	t.Cleanup(stop)
	require.Eventually(t, func() bool { return service.PreOutputKeepaliveCommitted(c) }, time.Second, time.Millisecond)
	copyFailoverRetryAfter(c, http.StatusServiceUnavailable, http.Header{"Retry-After": {"8"}})
	applyExhaustedRetryHint(c, &upstreamWaitAdvisor{}, http.StatusServiceUnavailable)
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "server_error", "message": "Busy"}})
	failed := upstreamWaitFailure(t, rec.Body.String())
	require.Equal(t, "8", failed.Get("headers.retry-after").String())
	require.Equal(t, "Busy. Please try again in 8s.", failed.Get("message").String())
	require.Equal(t, 8, resilience.ResolveRetryAdvice(early).Seconds)
}

func TestUpstreamRetryWaitPreservesOtherProtocols(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, endpoint := range []string{EndpointResponses, EndpointChatCompletions, "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			stream := endpoint != EndpointResponses
			c, rec := upstreamWaitContext(t, endpoint, stream)
			failure := &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, ResponseHeaders: http.Header{"Retry-After": {"60"}}}
			if endpoint == "/v1/messages" {
				(&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted(c, failure, stream)
			} else {
				(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failure, stream)
			}
			if stream {
				require.Contains(t, rec.Body.String(), "Please try again in 30s.")
			} else {
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.Equal(t, "Upstream service temporarily unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
			}
			require.Equal(t, "30", c.Writer.Header().Get("Retry-After"))
			require.NotContains(t, rec.Body.String(), `"retry-after"`)
		})
	}
}

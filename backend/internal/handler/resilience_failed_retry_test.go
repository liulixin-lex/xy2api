//go:build unit

package handler

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesFailedRetryAdvice(t *testing.T) {
	for _, tc := range []struct {
		name, typ, code, want string
		advice                resilience.RetryAdvice
		mutate                func(*resilience.Settings)
		legacy                bool
	}{
		{name: "healthy", advice: resilience.RetryAdvice{Seconds: 1}, want: "1"},
		{name: "recovery", advice: resilience.RetryAdvice{Seconds: 7}, want: "7"},
		{name: "defensive ceiling", advice: resilience.RetryAdvice{Seconds: 30}, want: "10"},
		{name: "unknown"},
		{name: "turn cap", advice: resilience.RetryAdvice{Suppress: true, Source: "turn_cap"}},
		{name: "balance", typ: errTypeInsufficientBalance, advice: resilience.RetryAdvice{Seconds: 7}},
		{name: "balance code", code: "INSUFFICIENT_BALANCE", advice: resilience.RetryAdvice{Seconds: 7}},
		{name: "content rejection", code: codeContentPolicyViolation, advice: resilience.RetryAdvice{Seconds: 7}},
		{name: "frame switch off", advice: resilience.RetryAdvice{Seconds: 7}, legacy: true, mutate: func(s *resilience.Settings) { s.Errors.FailedFrameRetryAfter = false }},
		{name: "errors off", advice: resilience.RetryAdvice{Seconds: 7}, legacy: true, mutate: func(s *resilience.Settings) { s.Errors.Enabled = false }},
		{name: "master off", advice: resilience.RetryAdvice{Seconds: 7}, legacy: true, mutate: func(s *resilience.Settings) { s.Enabled = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, tc.mutate)
			c, rec := newGinContextForEndpoint(t, EndpointResponses)
			c.Request = c.Request.WithContext(resilience.WithRetryAdvice(c.Request.Context(), tc.advice))
			c.Header("Retry-After", "30")
			c.Set(ginKeyResilienceRetryAfter, 30)
			require.True(t, writeResponsesFailedSSE(c, tc.typ, tc.code, "Busy. Please try again in 30s."))
			_, body := parseResponsesFailedSSE(t, rec.Body.String())
			if tc.want != "" {
				require.Equal(t, map[string]any{"retry-after": tc.want}, body["headers"])
				require.Equal(t, "Busy. Please try again in "+tc.want+"s.", body["message"])
				require.Equal(t, tc.want, c.Writer.Header().Get("Retry-After"))
			} else {
				require.NotContains(t, body, "headers")
				if tc.legacy {
					require.Equal(t, "Busy. Please try again in 30s.", body["message"])
				} else {
					require.Equal(t, "Busy.", body["message"])
					require.Empty(t, c.Writer.Header().Get("Retry-After"))
				}
			}
			if tc.advice.Source == "turn_cap" {
				require.Equal(t, codeServerIsOverloaded, body["code"])
			}
		})
	}
}

type mutableFrameRetryAdvisor struct {
	fakeRetryAdvisor
	seconds atomic.Int64
	calls   atomic.Int64
	group   atomic.Int64
	model   atomic.Value
}

func (a *mutableFrameRetryAdvisor) RetryAdvice(_ context.Context, group *int64, model string) resilience.RetryAdvice {
	a.calls.Add(1)
	if group != nil {
		a.group.Store(*group)
	}
	a.model.Store(model)
	return resilience.RetryAdvice{Seconds: int(a.seconds.Load()), Source: "recovery"}
}

func TestHandlerRetryAdviceProviderUsesLatestRequestState(t *testing.T) {
	withHandlerResilience(t, nil)
	c, _ := newResilienceTestContext(t)
	group := int64(10)
	advisor := &mutableFrameRetryAdvisor{}
	advisor.seconds.Store(9)
	installResilienceRetryAdvice(c, advisor, &service.APIKey{GroupID: &group}, "initial-model")
	require.Zero(t, advisor.calls.Load())
	st := resilience.StateFrom(c.Request.Context())
	st.SetGroup(20)
	st.NoteTurn(20, "routed-model", "session")
	advisor.seconds.Store(3)
	require.Equal(t, 3, resilience.ResolveRetryAdvice(c.Request.Context()).Seconds)
	require.Equal(t, int64(20), advisor.group.Load())
	require.Equal(t, "routed-model", advisor.model.Load())
	resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Suppress: true, Source: "turn_cap"})
	require.True(t, resilience.ResolveRetryAdvice(c.Request.Context()).Suppress)
	require.Equal(t, int64(1), advisor.calls.Load())
}

func TestExhaustedRetryAdviceUsesActualModelWithCopiedHeader(t *testing.T) {
	withHandlerResilience(t, nil)
	c, rec := newGinContextForEndpoint(t, EndpointResponses)
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
	st := resilience.StateFrom(c.Request.Context())
	st.SetGroup(20)
	st.NoteTurn(20, "routed-model", "session")
	c.Header("Retry-After", "60")
	advisor := &mutableFrameRetryAdvisor{}
	advisor.seconds.Store(7)
	status := applyExhaustedRetryHint(c, advisor, http.StatusBadGateway)
	(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, status, "api_error", "Busy", true)
	_, body := parseResponsesFailedSSE(t, rec.Body.String())
	require.Equal(t, "routed-model", advisor.model.Load())
	require.Equal(t, map[string]any{"retry-after": "7"}, body["headers"])
	require.Equal(t, "Busy. Please try again in 7s.", body["message"])
}

func TestTerminalReasonsRemoveStaleHTTPHints(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, typ := range []string{errTypeInsufficientBalance, codeContentPolicyViolation} {
		c, rec := newResilienceTestContext(t)
		c.Header("Retry-After", "30")
		c.Set(ginKeyResilienceRetryAfter, 30)
		resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7})
		(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, http.StatusForbidden, typ, "Rejected. Please try again in 30s.", false)
		require.Empty(t, rec.Header().Get("Retry-After"))
		require.Equal(t, "Rejected.", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
		require.True(t, resilience.RetryAdviceFrom(c.Request.Context()).Suppress)
	}
}

type warmingRetryAdvisor struct{ warmed atomic.Bool }

func (a *warmingRetryAdvisor) NoAccountRetryAfter(context.Context, *int64, string) time.Duration {
	a.warmed.Store(true)
	return 7 * time.Second
}
func (a *warmingRetryAdvisor) RetryAdvice(context.Context, *int64, string) resilience.RetryAdvice {
	if a.warmed.Load() {
		return resilience.RetryAdvice{Seconds: 7, Source: "recovery"}
	}
	return resilience.RetryAdvice{Source: "unknown"}
}

func TestExhaustedRetryAdviceWarmsSnapshotBeforeResolving(t *testing.T) {
	withHandlerResilience(t, nil)
	c, _ := newResilienceTestContext(t)
	resilience.StateFrom(c.Request.Context()).SetGroup(20)
	advisor := &warmingRetryAdvisor{}
	c.Set("api_key", &service.APIKey{GroupID: new(int64(20))})
	status := applyExhaustedRetryHint(c, advisor, http.StatusBadGateway)
	require.Equal(t, http.StatusBadGateway, status)
	require.True(t, advisor.warmed.Load())
	// The exit only prepares the advice (P2-10); a failed frame resolves it.
	require.Equal(t, 7, resilience.ResolveRetryAdvice(c.Request.Context()).Seconds)
}

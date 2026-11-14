//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSchedCaptureHTTPClassificationMatchesFailover(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   resilience.Category
	}{
		{"request restriction", 503, `{"error":{"type":"invalid_request_error","code":"unsupported_feature","message":"Synthetic option is unavailable"}}`, resilience.CategoryUpstreamRequestRestriction},
		{"unknown feature without request evidence", 503, `{"error":{"code":"unsupported_feature","message":"Synthetic option is unavailable"}}`, resilience.CategoryTransient},
		{"nested auth", 502, `{"error":{"code":"invalid_api_key","message":"Synthetic credential rejected"}}`, resilience.CategoryAuth},
		{"nested balance", 503, `{"error":{"code":"insufficient_quota","message":"Synthetic allowance exhausted"}}`, resilience.CategoryBalance},
		{"nested model", 502, `{"error":{"code":"model_not_found","message":"Synthetic deployment unavailable"}}`, resilience.CategoryModelUnsupported},
		{"nested capacity", 502, `{"error":{"type":"overloaded_error","message":"Synthetic capacity rejection"}}`, resilience.CategoryCapacityShed},
		{"ordinary server failure", 503, `{"error":{"message":"Synthetic upstream failure"}}`, resilience.CategoryTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			failover := &UpstreamFailoverError{StatusCode: tc.status, ResponseBody: body}
			require.Equal(t, tc.want, ClassifyFailoverError(failover).Category)
			require.Equal(t, string(tc.want), opsHTTPResilienceCategory(tc.status, nil, body, ""))
			for _, viaFailover := range []bool{false, true} {
				a, rec := newClassificationCapture()
				if viaFailover {
					a.finish(nil, failover)
				} else {
					a.finishHTTP(&http.Response{StatusCode: tc.status}, body)
				}
				requireCaptureCategory(t, rec, tc.want)
			}
		})
	}
}

func TestSchedCaptureClassificationUsesRuntimeSettings(t *testing.T) {
	for _, decisionV2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "decision_v2"}[decisionV2], func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) {
				s.Failure.DecisionV2 = decisionV2
				s.Failure.BalanceKeywordsExtra = []string{"synthetic allowance marker"}
			})
			busyCategory := resilience.CategoryRateLimit
			if decisionV2 {
				busyCategory = resilience.CategoryTransient
			}
			for _, tc := range []struct {
				body string
				want resilience.Category
			}{
				{`{"error":{"message":"The service is busy"}}`, busyCategory},
				{`{"error":{"message":"Synthetic allowance marker"}}`, resilience.CategoryBalance},
			} {
				body := []byte(tc.body)
				require.Equal(t, tc.want, classifyUpstreamHTTP(502, nil, body, "").Category)
				a, rec := newClassificationCapture()
				a.finishHTTP(&http.Response{StatusCode: 502}, body)
				requireCaptureCategory(t, rec, tc.want)
			}
		})
	}
}

func TestSchedCaptureStreamClassificationMatchesOps(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		body string
		want resilience.Category
	}{
		{`{"type":"response.failed","response":{"error":{"code":"invalid_api_key","message":"Synthetic credential rejected"}}}`, resilience.CategoryAuth},
		{`{"type":"response.failed","response":{"error":{"type":"invalid_request_error","message":"Synthetic option rejected"}}}`, resilience.CategoryClientError},
		{`{"type":"error","error":{"code":"insufficient_quota","message":"Synthetic allowance exhausted"}}`, resilience.CategoryBalance},
		{`{"type":"response.failed","response":{"error":{"message":"Synthetic upstream failure"}}}`, resilience.CategoryTransient},
	} {
		a, rec := newClassificationCapture()
		a.observe([]byte(tc.body))
		a.finish(nil, nil)
		requireCaptureCategory(t, rec, tc.want)
		require.Equal(t, string(tc.want), opsResilienceCategory(&OpsUpstreamErrorEvent{
			UpstreamStatusCode: http.StatusOK, UpstreamResponseBody: tc.body,
		}))
	}
}

func newClassificationCapture() (*schedAttemptCapture, *captureServiceRecorder) {
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "test-model"})
	ctx := resilience.WithRequestState(context.Background(), st)
	rec := &captureServiceRecorder{}
	svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
	return svc.beginSchedAttempt(ctx, &Account{ID: 1, Platform: PlatformOpenAI}, nil, 0, true), rec
}

func requireCaptureCategory(t *testing.T, rec *captureServiceRecorder, want resilience.Category) {
	t.Helper()
	require.Len(t, rec.samples, 1)
	require.Equal(t, string(want), rec.samples[0].Detail)
	require.Equal(t, schedquality.OutcomeAccountFault, rec.samples[0].Outcome)
}

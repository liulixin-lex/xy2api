//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRecoveryOutcomeUsesHandlerResult(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name, want                                      string
		output, cancel, budget, completed, disconnected bool
	}{
		{name: "completed", want: "completed", completed: true},
		{name: "empty completed", want: "completed", completed: true},
		{name: "upstream", want: "upstream_failure"},
		{name: "partial", want: "post_output_failure", output: true},
		{name: "cancel", want: "client_cancel", cancel: true},
		{name: "cancel after output", want: "client_cancel", cancel: true, output: true},
		{name: "explicit disconnect before context cancel", want: "client_cancel", output: true, disconnected: true},
		{name: "budget", want: "budget_exhausted", budget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newResilienceTestContext(t)
			ctx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			st := resilience.StateFrom(ctx)
			service.InstallDeliveryTracking(c, time.Now().Add(-time.Second))
			if tc.output {
				st.NoteSemanticOutput(time.Now())
			}
			var err error
			if !tc.completed {
				err = errors.New("upstream failure")
			}
			recordForwardRecovery(c, err, tc.disconnected, tc.completed)
			if tc.cancel {
				cancel()
			}
			if tc.budget {
				b := resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now().Add(-time.Hour))
				st.SetBudget(b)
				require.True(t, resilienceAttemptFailed(ctx, 1, newTestFailoverErr(http.StatusBadGateway, false, false)))
			}
			entry := &service.OpsInsertErrorLogInput{}
			applyOpsLatencyFieldsFromContext(c, entry)
			require.Equal(t, tc.want, *entry.RecoveryOutcome)
			require.Equal(t, tc.output, entry.EndToEndFirstOutputMs != nil)
		})
	}
}

type recoveryRejectedRequestUpstream struct {
	service.HTTPUpstream
	status int
	body   string
	calls  int
}

func (u *recoveryRejectedRequestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: u.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(u.body)), Request: req}, nil
}

func (u *recoveryRejectedRequestUpstream) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

func TestRecoveryOutcomeRejectionAcrossHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body, outcome string
		status              int
	}{
		{"invalid value", `{"error":{"type":"invalid_request_error","code":"invalid_value","param":"max_output_tokens","message":"Invalid value: -1. Expected a value >= 1."}}`, "terminal_rejection", 400},
		{"cyber policy", `{"error":{"code":"cyber_policy","message":"flagged for cyber policy"}}`, "terminal_rejection", 400},
		{"upstream failure exhausts selection", `{"error":{"type":"server_error","message":"upstream unavailable"}}`, "selection_exhausted", 502},
	} {
		for _, endpoint := range []string{"native responses", "passthrough responses", "chat"} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				withHandlerResilience(t, nil)
				upstream := &recoveryRejectedRequestUpstream{status: tc.status, body: tc.body}
				h := newResponsesUsageHandler(t, &gwUsageLogRepo{}, upstream, 0, endpoint == "passthrough responses")
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				recorder := httptest.NewRecorder()
				if endpoint != "chat" {
					servePassthroughResponses(h, ctx, recorder)
				} else {
					groupID := int64(911)
					key := &service.APIKey{ID: 912, GroupID: &groupID, User: &service.User{ID: 913, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}}
					router := gin.New()
					router.Use(middleware.ClientRequestID())
					router.POST("/v1/chat/completions", func(c *gin.Context) {
						c.Set(string(middleware.ContextKeyAPIKey), key)
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.User.ID, Concurrency: 1})
						h.ChatCompletions(c)
					})
					req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(recorder, req)
				}
				require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
				require.Equal(t, 1, upstream.calls, "request rejection must not retry or change accounts")
				require.NotNil(t, st.RecoveryOutcome())
				require.Equal(t, tc.outcome, *st.RecoveryOutcome())
				if endpoint == "native responses" && tc.name == "invalid value" {
					require.Contains(t, recorder.Body.String(), `"code":"invalid_value"`)
				}
			})
		}
	}
}

func TestGeminiTimeoutSwitchThenCancelKeepsFinalOutcome(t *testing.T) {
	withHandlerResilience(t, nil)
	c, _ := newResilienceTestContext(t)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	st := resilience.StateFrom(ctx)
	service.InstallDeliveryTracking(c, time.Now())
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
	fs := NewFailoverState(3, false)
	scheduler := &mockTempUnscheduler{}
	timeout := newTestFailoverErr(http.StatusGatewayTimeout, false, false)
	recordForwardRecovery(c, timeout, false, false)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, scheduler, 1, service.PlatformGemini, 0, timeout))
	cancel()
	recordForwardRecovery(c, context.Canceled, false, false)
	require.Equal(t, FailoverCanceled, fs.HandleFailoverError(ctx, scheduler, 2, service.PlatformGemini, 0, timeout))
	require.Empty(t, scheduler.calls, "cancelled attempt must not add account penalties")
	require.Equal(t, "client_cancel", *recoveryOutcome(c))
}

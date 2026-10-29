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

type recoveryRejectedRequestUpstream struct{ service.HTTPUpstream }

func (u *recoveryRejectedRequestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"invalid_value","param":"max_output_tokens","message":"Invalid value: -1. Expected a value >= 1."}}`)), Request: req}, nil
}

func (u *recoveryRejectedRequestUpstream) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

func TestRecoveryOutcomeDeterministicRejectionInResponsesHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withHandlerResilience(t, nil)
	h := newResponsesUsageHandler(t, &gwUsageLogRepo{}, &recoveryRejectedRequestUpstream{}, 0, false)
	st := resilience.NewRequestState()
	recorder := httptest.NewRecorder()
	servePassthroughResponses(h, resilience.WithRequestState(context.Background(), st), recorder)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":"invalid_value"`)
	require.Equal(t, "terminal_rejection", *st.RecoveryOutcome())
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

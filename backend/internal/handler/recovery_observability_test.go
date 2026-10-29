//go:build unit

package handler

import (
	"context"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestRecoveryOutcomeUsesHandlerResult(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name, want                        string
		output, cancel, budget, completed bool
	}{
		{name: "completed", want: "completed", completed: true},
		{name: "empty completed", want: "completed", completed: true},
		{name: "upstream", want: "upstream_failure"},
		{name: "partial", want: "post_output_failure", output: true},
		{name: "cancel", want: "client_cancel", cancel: true},
		{name: "cancel after output", want: "client_cancel", cancel: true, output: true},
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
			recordForwardRecovery(c, err, false, tc.completed)
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

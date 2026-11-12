//go:build unit

package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func budgetCutTestState(t *testing.T, mode string) (*resilience.RequestState, context.Context, resilience.TimeoutPlan) {
	t.Helper()
	cfg := resilience.Defaults()
	cfg.Budget.BudgetCutAttribution = mode
	now := time.Now()
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, &cfg, resilience.KindText, now.Add(-235*time.Second)))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 7, Model: "frozen-model", AttemptStart: now, Warm: true, Stream: true})
	plan := st.PlanAttemptTimeout(30*time.Second, false, now)
	return st, ctx, plan
}

func TestBudgetCutSupplementsPlannedWithoutChangingClassification(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{resilience.BudgetAttributionObserve, resilience.BudgetAttributionEnforce} {
		t.Run(mode, func(t *testing.T) {
			st, ctx, plan := budgetCutTestState(t, mode)
			// Trigger-time profile still owns planned/Reason; plan owns only cut.
			profile := st.AttemptProfile()
			profile.AttemptStart = time.Now().Add(-25 * time.Second)
			st.SetAttemptProfile(profile)
			svc := &OpenAIGatewayService{}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			account := &Account{ID: 7, Platform: PlatformOpenAI}
			fe := svc.newOpenAIFirstOutputTimeoutError(ctx, c, account, nil, "", profile.AttemptStart, "alias", "", plan.Effective(), "first_output", nil, plan)
			require.Equal(t, PlannedFirstOutputTimeoutReason, fe.Reason)
			require.Equal(t, mode == resilience.BudgetAttributionEnforce, BudgetCutApplied(fmt.Errorf("wrapped: %w", fe)))
			require.True(t, fe.ShouldReportAccountScheduleFailure())
			_, _, counted := classifyOpenAIAPIKeyHealthFailure(fe)
			require.False(t, counted, "planned classifier stays exempt")
			require.Nil(t, svc.openaiModelTransient, "planned timeouts no longer feed the retired slow-failure breaker")
			// A mismatched current profile retains the old non-planned classification.
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 8, Model: "later"})
			fe = svc.newOpenAIFirstOutputTimeoutError(ctx, c, account, nil, "", time.Now(), "alias", "", plan.Effective(), "first_output", nil, plan)
			require.NotEqual(t, PlannedFirstOutputTimeoutReason, fe.Reason)
			_, _, counted = classifyOpenAIAPIKeyHealthFailure(fe)
			require.True(t, counted)
		})
	}
	require.False(t, BudgetCutApplied(nil))
	require.False(t, BudgetCutApplied(&UpstreamFailoverError{}))
}

func TestBudgetLateWarningsReachSystemSink(t *testing.T) {
	previous := slog.Default()
	logger.InitBootstrap()
	defer slog.SetDefault(previous)
	sink := NewOpsSystemLogSink(nil)
	defer sink.Stop()
	logger.SetSink(sink)
	defer logger.SetSink(nil)
	resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
	defer resilience.SetUndeliveredAbuseCheck(nil)
	for _, mode := range []string{resilience.BudgetStartArrival, resilience.BudgetStartBodyEOF} {
		for _, hidden := range []bool{false, true} {
			cfg := resilience.Defaults()
			cfg.Budget.BudgetStart = mode
			start := time.Now().Add(-239 * time.Second)
			st := resilience.NewRequestState()
			ctx := context.WithValue(context.Background(), ctxkey.RequestID, "request-test")
			ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-test")
			ctx, cancel := context.WithCancel(ctx)
			st.SetBudget(resilience.NewBudget(ctx, &cfg, resilience.KindText, start))
			st.InitDelivery(ctx, start, nil)
			st.SetAPIKey(17)
			st.SetStage(resilience.StageHeaderWait)
			st.MarkBodyReceived(start.Add(100*time.Second), "body_eof", 1024)
			at := start.Add(239 * time.Second)
			if hidden || st.Budget().CanAttempt(at) {
				st.SetAttemptProfile(resilience.AttemptProfile{AttemptStart: at})
				st.ObserveAttemptStart()
			}
			cancel()
			_, _, abuse := st.ResolveDisconnect()
			require.True(t, abuse)
			gotLate, gotAbuse := false, false
			for len(sink.queue) > 0 {
				event := <-sink.queue
				switch event.Message {
				case "resilience.late_attempt_undelivered":
					gotLate = true
					require.Equal(t, "request-test", event.Fields["request_id"])
					require.Equal(t, "client-test", event.Fields["client_request_id"])
					require.EqualValues(t, 17, event.Fields["api_key_id"])
				case "resilience.abuse_guard":
					gotAbuse = true
				}
			}
			require.Equal(t, hidden, gotLate)
			require.True(t, gotAbuse)
		}
	}
}

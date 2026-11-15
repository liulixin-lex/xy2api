//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestM8PreDispatchPreservesFailure(t *testing.T) {
	for _, gate := range []string{"not_before", "budget", "deadline"} {
		t.Run(gate, func(t *testing.T) {
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			previous := &UpstreamFailoverError{StatusCode: 429, ResponseBody: []byte(`{"error":{"message":"original rate limit"}}`), ResponseHeaders: http.Header{"Retry-After": {"310"}}, RetryableOnSameAccount: true}
			st.SetLastUpstreamFailure(previous)
			require.Same(t, previous, st.LastUpstreamFailure())
			hint := resilience.UpstreamRecoveryHint{ObservedAt: time.Now(), NotBefore: time.Now().Add(310 * time.Second)}
			st.SetUpstreamRecoveryHint(&hint)
			switch gate {
			case "not_before":
				st.NoteAccountRetryNotBefore(11, hint)
			case "budget":
				settings := resilience.Defaults()
				settings.Budget.MaxAttempts = 1
				budget := resilience.NewBudget(ctx, &settings, resilience.KindText, time.Now())
				budget.RecordAttemptAt(time.Now())
				st.SetBudget(budget)
			case "deadline":
				deadline, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = deadline
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.example/v1/responses", nil)
			require.NoError(t, err)
			transport := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{httpUpstream: transport}
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			var capture *schedAttemptCapture
			_, err = svc.sendSchedHTTP(ctx, account, req, "", nil, false, &capture)
			require.Error(t, err)
			require.ErrorIs(t, err, previous)
			var blocked *UpstreamFailoverError
			require.ErrorAs(t, err, &blocked)
			require.Equal(t, 429, blocked.StatusCode)
			require.Equal(t, previous.ResponseBody, blocked.ResponseBody)
			require.True(t, blocked.PreDispatchSelectionInvalidated)
			require.False(t, blocked.ShouldReportAccountScheduleFailure())
			_, _, healthFailure := classifyOpenAIAPIKeyHealthFailure(blocked)
			require.False(t, healthFailure)
			require.False(t, blocked.ShouldRetryNextAccount())
			require.False(t, previous.PreDispatchSelectionInvalidated)
			require.True(t, previous.ShouldRetryNextAccount())
			require.Same(t, err, svc.handleOpenAIUpstreamTransportError(ctx, c, account, err, false))
			require.Nil(t, capture)
			require.Empty(t, transport.requests)
			require.Nil(t, st.CurrentAttempt())
			_, sent := st.OpsUpstreamSendBoundary(11)
			require.False(t, sent)
			require.Empty(t, currentOpsUpstreamEvents(c))
			require.False(t, st.AccountFailed(11))
			require.Equal(t, hint.NotBefore, st.UpstreamRecoveryHint().NotBefore)
		})
	}
	t.Run("due_without_recorder", func(t *testing.T) {
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), st)
		st.NoteAccountRetryNotBefore(11, resilience.UpstreamRecoveryHint{NotBefore: time.Now().Add(-time.Second)})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.example/v1/responses", nil)
		require.NoError(t, err)
		transport := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}}
		svc := &OpenAIGatewayService{httpUpstream: transport}
		var capture *schedAttemptCapture
		_, err = svc.sendSchedHTTP(ctx, &Account{ID: 11, Platform: PlatformOpenAI}, req, "", nil, false, &capture)
		require.NoError(t, err)
		require.Len(t, transport.requests, 1)
		require.Nil(t, capture)
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err = svc.sendSchedHTTP(ctx, &Account{ID: 11}, req, "", nil, false, &capture)
		require.True(t, errors.Is(err, context.Canceled))
		require.Len(t, transport.requests, 1)
	})
	var absent *resilience.RequestState
	absent.SetLastUpstreamFailure(errors.New("ignored"))
	require.Nil(t, absent.LastUpstreamFailure())
}

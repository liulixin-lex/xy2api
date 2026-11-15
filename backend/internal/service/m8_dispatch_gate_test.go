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
		st.SetLastUpstreamFailure(&UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable})
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

// Credential resolution can finish after the header timer fires, before any
// transport call. Returning the resolved account models a late successful read.
type m8LateCredentialRepo struct {
	AccountRepository
	parent  *Account
	expired bool
}

func (r *m8LateCredentialRepo) GetByID(ctx context.Context, _ int64) (*Account, error) {
	if ctx.Done() != nil {
		select {
		case <-ctx.Done():
			r.expired = true
		case <-time.After(3 * time.Second):
			return nil, errors.New("header timer did not cancel credential lookup")
		}
	}
	return r.parent, nil
}

func TestM8PreDispatchHeaderTimeoutPreservesFailure(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			st := resilience.StateFrom(req.ctx)
			st.SetBudget(nil) // Use the configured one-second response-header timer.
			previous := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests,
				ResponseBody: []byte(`{"error":{"message":"original rate limit"}}`)}
			st.SetLastUpstreamFailure(previous)
			account := webSearchReplayAccount(passthrough)
			account.Type = AccountTypeOAuth
			parentID := int64(379)
			account.ParentAccountID = &parentID
			repo := &m8LateCredentialRepo{parent: &Account{ID: parentID, Platform: PlatformOpenAI,
				Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}}
			st.NoteAccountRetryNotBefore(account.ID, resilience.UpstreamRecoveryHint{NotBefore: time.Now().Add(time.Minute)})
			upstream := &httpUpstreamRecorder{}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
			svc.accountRepo = repo

			_, err := svc.Forward(req.ctx, req.c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":"hello"}`))

			require.True(t, repo.expired, "the actual header guard must have fired")
			require.ErrorIs(t, err, previous)
			var blocked *UpstreamFailoverError
			require.ErrorAs(t, err, &blocked)
			require.True(t, blocked.PreDispatchSelectionInvalidated)
			require.Equal(t, http.StatusTooManyRequests, blocked.StatusCode)
			require.Equal(t, previous.ResponseBody, blocked.ResponseBody)
			require.NotContains(t, string(blocked.ResponseBody), "first_output_timeout")
			require.Empty(t, upstream.requests)
			require.Empty(t, currentOpsUpstreamEvents(req.c))
			require.Empty(t, req.session.failures)
			require.False(t, st.AccountFailed(account.ID))
			_, sent := st.OpsUpstreamSendBoundary(account.ID)
			require.False(t, sent)
		})
	}
}

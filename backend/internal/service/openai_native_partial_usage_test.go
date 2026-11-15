//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAINativeForwardFailureKeepsOnlyPostOutputResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withResilienceSettings(t, nil)
	failed := "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"broke\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n"
	for _, output := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_output", true: "after_output"}[output], func(t *testing.T) {
			stream := failed
			if output {
				stream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" + stream
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			body := []byte(`{"model":"gpt-5.4","stream":true,"input":"hello"}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			account := &Account{ID: 811, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"}}
			result, err := svc.Forward(context.Background(), c, account, body)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			if !output {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failover)
				return
			}
			require.False(t, errors.As(err, &failover))
			require.NotNil(t, result)
			require.Equal(t, 11, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.OutputTokens)
		})
	}
}

func TestOpenAINativePartialFailurePreservesPenalty(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	account.Credentials = map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"}
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"insufficient_balance\",\"message\":\"insufficient account balance\"},\"usage\":{\"input_tokens\":11}}}\n\n"
	svc := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: rl, accountRepo: repo,
		httpUpstream: &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}}}
	now := time.Now()
	svc.getOpenAIAccountModelTransientState().recordFailure(account.ID, "gpt-5.4", now.Add(-time.Second))
	svc.getOpenAIAccountModelTransientState().recordFailure(account.ID, "gpt-5.4", now)
	// A newer pause is already persisted after this attempt's account snapshot.
	repo.tempReason = resilience.BalancePauseReasonAtStep(1, "newer failure")
	repo.tempUntil = now.Add(time.Minute)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","stream":true,"input":"hello"}`))
	require.Error(t, err)
	require.NotNil(t, result)
	require.True(t, svc.getOpenAIAccountModelTransientState().isBlocked(account.ID, "gpt-5.4", time.Now()))
	require.True(t, resilience.IsBalancePause(repo.tempReason), repo.tempReason)
	require.Zero(t, repo.clearCalls)
}

type nativeCancelOnOutput struct {
	gin.ResponseWriter
	cancel context.CancelFunc
}

func (w *nativeCancelOnOutput) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if strings.Contains(string(p[:n]), "hello") {
		w.cancel()
	}
	return n, err
}

type nativeDrainReadError struct{}

func (nativeDrainReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestOpenAINativePartialUsageDoesNotResetSuccessState(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		newer, complete, cancelAfterComplete bool
	}{
		{name: "old balance tier"}, {name: "newer balance pause", newer: true},
		{name: "complete", complete: true}, {name: "complete then client leaves", complete: true, cancelAfterComplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
			account.Credentials = map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"}
			account.TempUnschedulableReason = resilience.BalancePauseReasonAtStep(2, "old balance failure")
			repo.tempReason = account.TempUnschedulableReason
			repo.tempUntil = time.Now().Add(-time.Minute)
			account.TempUnschedulableUntil = &repo.tempUntil
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := resilience.NewRequestState()
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, AttemptStart: time.Now(), Model: "gpt-5.4", Stream: true})
			ctx = resilience.WithRequestState(ctx, st)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			InstallDeliveryTracking(c, time.Now())
			if !tc.complete {
				c.Writer = &nativeCancelOnOutput{ResponseWriter: c.Writer, cancel: cancel}
			}
			stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"usage\":{\"input_tokens\":11,\"output_tokens\":2}}\n\n"
			var body io.Reader = io.MultiReader(strings.NewReader(stream), nativeDrainReadError{})
			if tc.complete {
				body = strings.NewReader(stream + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":11,\"output_tokens\":2}}}\n\n")
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: rl, accountRepo: repo, resilience: r, httpUpstream: &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(body)}}}}
			now := time.Now()
			svc.getOpenAIAccountModelTransientState().recordFailure(account.ID, "gpt-5.4", now.Add(-time.Second))
			svc.getOpenAIAccountModelTransientState().recordFailure(account.ID, "gpt-5.4", now)
			if tc.newer {
				repo.tempUntil = now.Add(time.Minute)
				repo.tempReason = resilience.BalancePauseReasonAtStep(3, "new failure")
			}
			result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.4","stream":true,"input":"hi"}`))
			require.NotNil(t, result)
			require.Equal(t, !tc.complete, result.PartialFailure)
			if tc.complete {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.True(t, st.ClientGone())
				require.False(t, st.AccountFailed(account.ID), "cancel is not account failure")
			}
			if tc.cancelAfterComplete {
				cancel()
			}
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			bill := newOpenAIRecordUsageServiceForTest(usageRepo, nil, nil, nil)
			counter := &partialUsage403Counter{}
			rl.SetOpenAI403CounterCache(counter)
			bill.rateLimitService = rl
			bill.cfg.RunMode = config.RunModeSimple
			bill.resilience = r
			bill.accountRepo = repo
			require.NoError(t, bill.RecordUsage(context.WithoutCancel(ctx), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 912, Group: &Group{RateMultiplier: 1}}, User: &User{ID: 913}, Account: account}))
			require.Equal(t, 1, usageRepo.calls)
			require.Equal(t, 11, usageRepo.lastLog.InputTokens)
			if tc.complete {
				require.Equal(t, 1, counter.resets)
				require.Equal(t, 1, repo.clearCalls)
				require.Empty(t, repo.tempReason)
			} else {
				require.Zero(t, counter.resets)
				require.Zero(t, repo.clearCalls)
				require.True(t, resilience.IsBalancePause(repo.tempReason))
				require.True(t, svc.getOpenAIAccountModelTransientState().isBlocked(account.ID, "gpt-5.4", time.Now()))
			}
		})
	}
}

type partialUsage403Counter struct{ resets int }

func (c *partialUsage403Counter) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 2, nil
}
func (c *partialUsage403Counter) ResetOpenAI403Count(context.Context, int64) error {
	c.resets++
	return nil
}

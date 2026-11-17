//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureDoneHTTPKeepsDeliveryOutcome(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := selectionNewSessionFixture()
	state := resilience.NewRequestState()
	state.EnableGatewayRequest(time.Now())
	ctx := resilience.WithRequestState(context.Background(), state)
	selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	account := selected.Account
	account.Credentials = map[string]any{"api_key": "synthetic-test", "base_url": "https://synthetic.invalid"}
	account.Extra = map[string]any{"openai_responses_mode": "force_responses"}
	state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "m"})
	payload := `{"type":"response.done","response":{"id":"resp_fixture","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":12,"output_tokens":1}}}`
	rec := &captureServiceRecorder{}
	svc := &OpenAIGatewayService{
		cfg: newSchedulerTestOpenAIWSV2Config(), schedAttemptRecorder: rec,
		httpUpstream: &httpUpstreamRecorder{responses: []*http.Response{{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n")),
		}}},
	}
	body := []byte(`{"model":"m","input":"synthetic prompt","stream":true}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	result, err := svc.Forward(ctx, c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, w.Body.String(), payload)
	assert.Equal(t, "response.done", result.UpstreamTerminalEvent)
	assert.True(t, result.SucceededForScheduling())
	// Exercise the same success predicate and report call as the HTTP handler.
	svc.openaiOAuth429RetryStartedAt.Store(account.ID, time.Now())
	svc.ReportOpenAIAccountScheduleResultWithContext(ctx, account, "m", result.SucceededForScheduling(), result.FirstTokenMs)
	_, retryPending := svc.openaiOAuth429RetryStartedAt.Load(account.ID)
	assert.False(t, retryPending, "successful schedule reporting clears retry state")
	require.Len(t, rec.samples, 1)
	assert.Equal(t, schedquality.OutcomeOK, rec.samples[0].Outcome)
	state.CompleteGatewayForward(result.CompletedForDelivery(), false)
	meta, _ := state.ClaimGatewayRequest(false)
	require.NotNil(t, meta)
	assert.Equal(t, resilience.RequestSuccess, meta.Result)
	assert.Equal(t, int64(10), f.binding.owner, "binding waits for delivery")
	FinishOpenAIStickyForward(ctx, account.ID, result, err)
	assert.Equal(t, account.ID, f.binding.owner)
	assert.Len(t, f.binding.casKeys, 1)
	avoided, err := f.session.store.SessionAvoid(ctx, 7, stickyChangeAvoidSession("session"))
	require.NoError(t, err)
	assert.Empty(t, avoided)
}

func TestCaptureDoneWSKeepsSamplingSuccess(t *testing.T) {
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.done","response":{"id":"resp_fixture","status":"failed","model":"gpt-5.1","usage":{"input_tokens":12,"output_tokens":1}}}`)
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	account := passthroughLifecycleAccount()
	state := resilience.NewRequestState()
	state.EnableGatewayRequest(time.Now())
	state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "gpt-5.1"})
	ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), state))
	defer cancel()
	rec := &captureServiceRecorder{}
	svc.SetSchedAttemptRecorder(rec)
	completed := make(chan *OpenAIForwardResult, 1)
	server, _ := startPassthroughHookRecordingServer(t, ctx, svc, account, &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, _ error) { completed <- result },
	})
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	select {
	case result := <-completed:
		require.NotNil(t, result)
		require.Equal(t, "response.done", result.UpstreamTerminalEvent)
		require.True(t, result.CompletedForDelivery())
	case <-time.After(3 * time.Second):
		t.Fatal("OnTurnComplete did not finish the attempt")
	}
	require.Len(t, rec.samples, 1)
	require.Equal(t, schedquality.OutcomeOK, rec.samples[0].Outcome)
}

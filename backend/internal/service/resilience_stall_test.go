//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStallCheckPeriodAndThreshold(t *testing.T) {
	withResilienceSettings(t, nil)
	require.Equal(t, 120*time.Second, resilienceStallCheckBase(180*time.Second))
	require.Equal(t, 120*time.Second, resilienceStallCheckBase(0), "text stall applies even without the legacy interval")
	require.Equal(t, 60*time.Second, resilienceStallCheckBase(60*time.Second))
	require.Equal(t, 5*time.Second, resilienceStallCheckPeriod(120*time.Second))
	require.Equal(t, 2*time.Second, resilienceStallCheckPeriod(8*time.Second))
	require.Equal(t, 180*time.Second, resilienceStallThreshold(180*time.Second, false), "before output: configured interval")
	require.Equal(t, 120*time.Second, resilienceStallThreshold(180*time.Second, true))

	withResilienceSettings(t, func(s *resilience.Settings) { s.Stall.Enabled = false })
	require.Equal(t, 180*time.Second, resilienceStallCheckBase(180*time.Second))
	require.Equal(t, 180*time.Second, resilienceStallCheckPeriod(180*time.Second), "0.3.0 ticker")
	require.Equal(t, 180*time.Second, resilienceStallThreshold(180*time.Second, true))
}

func newStallTestService(intervalSeconds int) *GatewayService {
	return &GatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: intervalSeconds,
			MaxLineSize:               defaultMaxLineSize,
		}},
		rateLimitService: &RateLimitService{},
	}
}

func TestStallDetectedWithinFiveSecondsAfterOutput(t *testing.T) {
	// G2: a text stream that stops after output ends at the text stall
	// threshold plus at most one check period, not at twice the interval.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stall.TextStallSeconds = 1 })
	gin.SetMode(gin.TestMode)
	svc := newStallTestService(30)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
	go func() {
		_, _ = pw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"))
		_, _ = pw.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
	}()
	start := time.Now()
	_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "m", "m", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream data interval timeout")
	require.Less(t, time.Since(start), 3*time.Second)
	require.Contains(t, rec.Body.String(), "stream_timeout")
}

func TestFirstEventGuard(t *testing.T) {
	ctx, g := newFirstEventGuard(context.Background(), 30*time.Millisecond)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("guard did not fire")
	}
	require.True(t, g.Fired())
	require.Same(t, g, firstEventGuardFrom(ctx))
	g.Release()

	ctx, g = newFirstEventGuard(context.Background(), 30*time.Millisecond)
	g.Disarm()
	time.Sleep(60 * time.Millisecond)
	require.NoError(t, ctx.Err())
	require.False(t, g.Fired())
	g.Release()

	ctx, g = newFirstEventGuard(context.Background(), 0)
	require.Nil(t, g)
	require.Nil(t, firstEventGuardFrom(ctx))
	require.False(t, g.Fired())
	g.Disarm()
	g.Release()
}

func TestFirstEventTimeoutFailsOverBeforeOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	gin.SetMode(gin.TestMode)
	svc := newStallTestService(0)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	guarded, g := newFirstEventGuard(context.Background(), 50*time.Millisecond)
	defer g.Release()
	pr, pw := io.Pipe()
	go func() {
		<-guarded.Done()
		_ = pw.CloseWithError(context.Canceled)
	}()
	req := httptest.NewRequest(http.MethodPost, "http://upstream.invalid/v1/messages", nil).WithContext(guarded)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr, Request: req}
	_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "m", "m", false)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.Equal(t, "first_output_timeout", gjson.GetBytes(failoverErr.ResponseBody, "error.type").String())
	require.Zero(t, rec.Body.Len(), "nothing reached the client")
}

func budgetTestCtx(kind resilience.RequestKind) context.Context {
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), kind, time.Now()))
	return ctx
}

func TestFirstOutputTimeoutTiers(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) {
		s.Budget.ModelOverrides = map[string]int{"gpt-5.5-pro*": 200}
	})
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	ctx := budgetTestCtx(resilience.KindText)
	require.Equal(t, 180*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 1, "gpt-5.5", "", time.Time{}))
	require.Equal(t, 200*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 1, "gpt-5.5", "xhigh", time.Time{}))
	require.Equal(t, 220*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 1, "gpt-5.5", "max", time.Time{}))
	require.Equal(t, 200*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 1, "gpt-5.5-pro-2026", "low", time.Time{}))

	// The last usable candidate only answers to the pre-output deadline.
	resilience.StateFrom(ctx).SetLastResort(7)
	last := svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-5.5", "", time.Time{})
	require.Greater(t, last, 230*time.Second)
	require.LessOrEqual(t, last, 240*time.Second)

	// No budget (WebSocket, switch off): the 0.3.0 configuration.
	svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 45
	require.Equal(t, 45*time.Second, svc.openAIFirstOutputTimeoutFor(context.Background(), 1, "gpt-5.5", "", time.Time{}))
	require.Zero(t, svc.openAIFirstOutputTimeoutFor(budgetTestCtx(resilience.KindImage), 1, "gpt-image-1", "", time.Time{})-45*time.Second)

	// Anthropic and Gemini first events.
	require.Equal(t, 90*time.Second, resilienceFirstEventTimeout(budgetTestCtx(resilience.KindText), 1, PlatformAnthropic))
	require.Equal(t, 90*time.Second, resilienceFirstEventTimeout(budgetTestCtx(resilience.KindText), 1, PlatformGemini))
	require.Zero(t, resilienceFirstEventTimeout(context.Background(), 1, PlatformAnthropic))
	require.Zero(t, resilienceFirstEventTimeout(budgetTestCtx(resilience.KindImage), 1, PlatformAnthropic))
}

func TestFirstOutputTimeoutMeasuredFromAttemptStart(t *testing.T) {
	// Staging KA-1: the stream handler asks again when slow upstream headers
	// arrive; the budget must be measured from the attempt start, not from
	// that moment (240 - 150 = 90 s would fire at once).
	withResilienceSettings(t, nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	start := time.Now().Add(-150 * time.Second)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start))
	require.Equal(t, 180*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 1, "gpt-5.5", "", start))
}

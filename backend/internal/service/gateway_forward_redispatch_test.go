//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type sameAccountRedispatchUpstream struct {
	mu       sync.Mutex
	calls    int
	profiles []resilience.AttemptProfile
	sends    []time.Time
}

func (u *sameAccountRedispatchUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls++
	call := u.calls
	if st := resilience.StateFrom(req.Context()); st != nil {
		u.profiles = append(u.profiles, st.AttemptProfile())
	}
	u.sends = append(u.sends, time.Now())
	u.mu.Unlock()
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	if call == 1 {
		return nil, errors.New("dial tcp: connection reset by peer")
	}
	pr, pw := io.Pipe()
	ctx := req.Context()
	go func() {
		select {
		case <-ctx.Done():
			_ = pw.CloseWithError(ctx.Err())
		case <-time.After(300 * time.Millisecond):
			_, _ = pw.Write([]byte(strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[],"usage":{"input_tokens":3}}}`,
				"",
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
				"",
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				"",
				`event: message_stop`,
				`data: {"type":"message_stop"}`,
				"", "",
			}, "\n")))
			_ = pw.Close()
		}
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: pr, Request: req}, nil
}

func (u *sameAccountRedispatchUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestGatewayForwardSameAccountRedispatchGetsFreshFirstEventWindow(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) {
		s.Budget.AnthropicFirstEventTimeoutSeconds = 5
		s.Budget.MaxAttempts = 10
	})
	gin.SetMode(gin.TestMode)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))

	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	account := newAnthropicOAuthAccountForPartialUsageTest()
	up := &sameAccountRedispatchUpstream{}
	base := newForwardPartialUsageServiceForTest(nil)
	svc := &GatewayService{cfg: base.cfg, responseHeaderFilter: base.responseHeaderFilter,
		httpUpstream: up, rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}}

	forward := func() (*ForwardResult, error, time.Duration) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
		require.NoError(t, err)
		start := time.Now()
		res, ferr := svc.Forward(ctx, c, account, parsed)
		return res, ferr, time.Since(start)
	}

	// A handler retry reuses request state and the account, but starts a new Forward.
	_, err1, _ := forward()
	require.Error(t, err1)
	// The previous dispatch and retry delay have exhausted its first-event window.
	time.Sleep(5200 * time.Millisecond)
	redispatchStart := time.Now()

	res, err2, took := forward()
	up.mu.Lock()
	profiles, sends := append([]resilience.AttemptProfile(nil), up.profiles...), append([]time.Time(nil), up.sends...)
	up.mu.Unlock()
	require.Len(t, profiles, 2)
	t.Logf("dispatch2: err=%v took=%s attemptStart-age-at-send=%s", err2, took, sends[1].Sub(profiles[1].AttemptStart))
	require.False(t, profiles[1].AttemptStart.Before(redispatchStart), "dispatch 2 must get its own AttemptStart")
	require.True(t, profiles[1].AttemptStart.After(profiles[0].AttemptStart))
	require.False(t, profiles[1].TimerOwnerStart.Before(redispatchStart), "the timer owner is also fresh on redispatch")
	require.NoError(t, err2, "the event at +300 ms must be delivered within the fresh first-event window")
	require.NotNil(t, res)
	require.Equal(t, 1, res.Usage.OutputTokens)
}

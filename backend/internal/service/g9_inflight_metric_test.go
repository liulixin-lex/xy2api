//go:build unit

package service

import (
	"context"
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

type g9BlockedUpstream struct {
	entered chan struct{}
	release chan struct{}
}

func (u *g9BlockedUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	close(u.entered)
	<-u.release
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_g9","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}
func (u *g9BlockedUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestG9LateMetricVisibleWhileUpstreamBlocked(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.BudgetStart = resilience.BudgetStartBodyEOF })
	gin.SetMode(gin.TestMode)
	st := resilience.NewRequestState()
	st.SetGroup(7521)
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now().Add(-239*time.Second)))
	up := &g9BlockedUpstream{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(up.release) }) }
	defer release()
	base := newForwardPartialUsageServiceForTest(nil)
	svc := &GatewayService{cfg: base.cfg, responseHeaderFilter: base.responseHeaderFilter, httpUpstream: up, rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-3-5-sonnet-latest","stream":false,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := svc.Forward(ctx, c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
		done <- err
	}()
	select {
	case <-up.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not reach transport")
	}
	var found int64
	for _, row := range resilience.Snapshot(time.Minute) {
		if row.Name == "late_attempt_start" && row.GroupID == 7521 && row.Label == "false:body_eof" {
			found += row.Count
		}
	}
	require.EqualValues(t, 1, found, "K10: observe before upstream completion, handler return or FinishTiming")
	require.False(t, st.Finished())
	release()
	require.NoError(t, <-done)
}

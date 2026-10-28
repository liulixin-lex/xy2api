//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type hardBalanceAnthropicUpstream struct {
	calls    int
	response *http.Response
}

func (u *hardBalanceAnthropicUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	resp := *u.response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	u.response.Body = io.NopCloser(bytes.NewReader(body))
	return &resp, nil
}

func (u *hardBalanceAnthropicUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestAnthropicPassthroughBalanceErrorSkipsRetryAndAppliesOnePause(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	limitService, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	account.Credentials = map[string]any{"api_key": "fixture", "anthropic_passthrough": true}
	body := []byte(`{"error":{"type":"api_error","message":"Insufficient account balance"}}`)
	upstream := &hardBalanceAnthropicUpstream{response: &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"X-Request-Id": []string{"upstream-id"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}}
	svc := &GatewayService{
		cfg:              &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		rateLimitService: limitService,
		httpUpstream:     upstream,
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	_, err := svc.forwardAnthropicAPIKeyPassthrough(ctx, c, account,
		[]byte(`{"model":"claude-test","max_tokens":16,"messages":[]}`), "claude-test", "claude-test", false, time.Now())
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.Equal(t, 1, upstream.calls)
	require.True(t, resilience.IsBalancePause(repo.tempReason), repo.tempReason)
	require.Equal(t, 1, resilience.BalancePauseStep(repo.tempReason))
	require.Equal(t, 1, repo.tempCalls)
}

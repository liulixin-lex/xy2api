//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// streamErrorRepo records the account side effects of an in-stream error.
type streamErrorRepo struct {
	AccountRepository
	mu               sync.Mutex
	rateLimitedUntil []time.Time
	overloadCalls    int
	tempCalls        int
	errorCalls       int
}

func (r *streamErrorRepo) SetRateLimited(_ context.Context, _ int64, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rateLimitedUntil = append(r.rateLimitedUntil, until)
	return nil
}

func (r *streamErrorRepo) SetOverloaded(context.Context, int64, time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overloadCalls++
	return nil
}

func (r *streamErrorRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tempCalls++
	return nil
}

func (r *streamErrorRepo) SetError(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errorCalls++
	return nil
}

func (r *streamErrorRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}

func (r *streamErrorRepo) touched() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rateLimitedUntil) > 0 || r.overloadCalls > 0 || r.tempCalls > 0 || r.errorCalls > 0
}

// forwardStreamError runs a streaming Forward whose upstream answers 200 with
// the given SSE body.
func forwardStreamError(t *testing.T, sse string, header http.Header) (error, *streamErrorRepo, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)

	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "text/event-stream")
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	repo := &streamErrorRepo{}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     NewRateLimitService(repo, nil, cfg, nil, nil),
		deferredService:      &DeferredService{},
	}
	result, err := svc.Forward(context.Background(), c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		require.Nil(t, result)
	} else {
		require.NotNil(t, result)
	}
	return err, repo, rec
}

func streamErrorEvent(errType string) string {
	return `{"type":"error","error":{"type":"` + errType + `","message":"upstream says no"}}`
}

func TestStreamErrorStatusFollowsErrorType(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		errType string
		written bool
		want    int
	}{
		{"rate_limit_error", false, http.StatusTooManyRequests},
		{"rate_limit_error", true, http.StatusTooManyRequests},
		{"overloaded_error", false, 529},
		{"overloaded_error", true, 529},
		{"api_error", false, http.StatusBadGateway},
		{"api_error", true, http.StatusBadGateway},
		{"permission_error", false, http.StatusForbidden},
		{"invalid_request_error", true, http.StatusForbidden},
		{"", false, http.StatusForbidden},
	} {
		status, _ := resilienceStreamErrorStatus(tc.errType, tc.written, nil)
		require.Equal(t, tc.want, status, "%s written=%v", tc.errType, tc.written)
	}

	// Side effects only before output; a rate limit never reads the 200
	// answer's window headers.
	h := http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"1"}}
	_, se := resilienceStreamErrorStatus("rate_limit_error", false, h)
	require.NotNil(t, se)
	require.Empty(t, se)
	_, se = resilienceStreamErrorStatus("overloaded_error", false, h)
	require.Equal(t, h, se)
	for _, errType := range []string{"rate_limit_error", "overloaded_error"} {
		_, se = resilienceStreamErrorStatus(errType, true, h)
		require.Nil(t, se, errType)
	}
	_, se = resilienceStreamErrorStatus("api_error", false, h)
	require.Nil(t, se)

	// Switch off: 0.3.0, only a pre-output overload is a 529.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	for _, tc := range []struct {
		errType string
		written bool
		want    int
	}{
		{"overloaded_error", false, 529},
		{"overloaded_error", true, http.StatusForbidden},
		{"rate_limit_error", false, http.StatusForbidden},
		{"api_error", false, http.StatusForbidden},
	} {
		status, se := resilienceStreamErrorStatus(tc.errType, tc.written, h)
		require.Equal(t, tc.want, status, "%s written=%v", tc.errType, tc.written)
		require.Equal(t, tc.want == 529, se != nil, tc.errType)
	}
}

func TestForwardPreOutputStreamRateLimitIsA429(t *testing.T) {
	// R7: a Claude relay ends a queued request with an in-stream concurrency
	// rate limit; 0.3.0 reported it as 403 and clients read "Upstream access
	// forbidden" without Retry-After.
	withResilienceSettings(t, nil)
	const errorJSON = `{"type":"error","error":{"type":"rate_limit_error","code":"gateway_concurrency_limit","message":"Concurrency limit exceeded for account, please retry later"}}`
	// Window headers of a normal 200 answer must not become a reset hours away.
	header := http.Header{}
	header.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(time.Now().Add(3*time.Hour).Unix(), 10))
	header.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(time.Now().Add(72*time.Hour).Unix(), 10))
	err, repo, rec := forwardStreamError(t, "event: error\ndata: "+errorJSON+"\n\n", header)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.JSONEq(t, errorJSON, string(failoverErr.ResponseBody))
	require.Equal(t, resilience.CategoryRateLimit, ClassifyFailoverError(failoverErr).Category)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Empty(t, rec.Body.String(), "nothing written: the handler may switch accounts")
	require.Len(t, repo.rateLimitedUntil, 1, "the RATE_LIMIT soft cooldown")
	require.WithinDuration(t, time.Now().Add(time.Duration(defaultRateLimit429CooldownSeconds)*time.Second), repo.rateLimitedUntil[0], 5*time.Second,
		"the configured 429 cooldown, not the 5h window reset")
	require.Zero(t, repo.errorCalls)
}

func TestForwardPostOutputStreamErrorsMapStatusWithoutSideEffects(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, errType := range []string{"rate_limit_error", "overloaded_error", "api_error", "permission_error", "invalid_request_x"} {
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"partial\"}}\n\n" +
			"event: error\ndata: " + streamErrorEvent(errType) + "\n\n"
		err, repo, rec := forwardStreamError(t, sse, nil)
		var eventErr *sseStreamErrorEventError
		require.ErrorAs(t, err, &eventErr)
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
		require.False(t, repo.touched(), "%s: output was written, no account side effects", errType)
		require.Contains(t, rec.Body.String(), "message_start", errType)
	}
}

func TestForwardPreOutputStreamApiErrorIsA502(t *testing.T) {
	withResilienceSettings(t, nil)
	err, repo, rec := forwardStreamError(t, "event: error\ndata: "+streamErrorEvent("api_error")+"\n\n", nil)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, resilience.CategoryTransient, ClassifyFailoverError(failoverErr).Category)
	require.False(t, repo.touched())
	require.Empty(t, rec.Body.String())
}

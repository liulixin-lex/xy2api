//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// PM instruction 4 (review DELTA-P2-5): an OpenAI Responses passthrough
// stream that breaks after output (failed event, missing terminal, read
// error, stall) returned no result, so the Responses handler, which already
// bills partial results (#5148), recorded nothing. The usage read so far is
// billed exactly once; a client that cancels before output is not billed.

const (
	ptUsageOpening = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_usage\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	ptUsageFailed    = "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_usage\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"broke\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}}\n\n"
	ptUsageCompleted = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_usage\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":5}}}\n\n"
)

type ptUsageUpstream struct{ body func() io.ReadCloser }

func (u *ptUsageUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: u.body(), Request: req}, nil
}

func (u *ptUsageUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func newPassthroughUsageHandler(t *testing.T, usageRepo service.UsageLogRepository, upstream service.HTTPUpstream, streamInterval int, policies ...*service.CachePolicyService) *OpenAIGatewayHandler {
	t.Helper()
	repo := &grokCredentialHandlerRepo{accounts: []service.Account{{
		ID: 811, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"},
		Extra:       map[string]any{"openai_passthrough": true},
	}}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.StreamDataIntervalTimeout = streamInterval
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	var policy *service.CachePolicyService
	if len(policies) > 0 {
		policy = policies[0]
	}
	gateway := service.ProvideOpenAIGatewayServiceWithCachePolicies(
		repo, usageRepo, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billing, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, policy, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	return NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
}

func servePassthroughResponses(h *OpenAIGatewayHandler, ctx context.Context, writer http.ResponseWriter) {
	groupID := int64(911)
	key := &service.APIKey{ID: 912, GroupID: &groupID,
		User:  &service.User{ID: 913, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.User.ID, Concurrency: 1})
		h.Responses(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hi","stream":true}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(writer, req)
}

func TestOpenAIPassthroughBreakAfterOutputBillsPartialUsageOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name           string
		body           func() io.ReadCloser
		streamInterval int
		input, output  int
	}{
		{"failed_event", func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening + ptUsageFailed)) }, 0, 11, 3},
		{"missing_terminal", func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening)) }, 0, 0, 0},
		{"read_error", func() io.ReadCloser {
			return io.NopCloser(io.MultiReader(strings.NewReader(ptUsageOpening), gwUsageReadError{io.ErrUnexpectedEOF}))
		}, 0, 0, 0},
		{"stall", func() io.ReadCloser {
			pr, pw := io.Pipe()
			go func() { _, _ = io.WriteString(pw, ptUsageOpening) }()
			t.Cleanup(func() { _ = pw.Close() })
			return pr
		}, 1, 0, 0},
		{"complete", func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening + ptUsageCompleted)) }, 0, 11, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &gwUsageLogRepo{}
			h := newPassthroughUsageHandler(t, usageRepo, &ptUsageUpstream{body: tc.body}, tc.streamInterval)
			rec := httptest.NewRecorder()
			servePassthroughResponses(h, context.Background(), rec)
			require.Contains(t, rec.Body.String(), "hello", "output reached the client")

			require.Eventually(t, func() bool { return len(usageRepo.recorded()) > 0 }, 3*time.Second, 10*time.Millisecond,
				"the usage read before the stream broke is billed")
			time.Sleep(100 * time.Millisecond)
			logs := usageRepo.recorded()
			require.Len(t, logs, 1, "billed exactly once")
			require.Equal(t, tc.input, logs[0].InputTokens)
			require.Equal(t, tc.output, logs[0].OutputTokens)
		})
	}
}

func TestOpenAIPassthroughCancelBeforeOutputBillsNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withHandlerResilience(t, nil)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	usageRepo := &gwUsageLogRepo{}
	h := newPassthroughUsageHandler(t, usageRepo, &ptUsageUpstream{body: func() io.ReadCloser { return pr }}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		servePassthroughResponses(h, ctx, rec)
	}()
	_, err := io.WriteString(pw, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_cancel\",\"status\":\"in_progress\"}}\n\n")
	require.NoError(t, err)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after the client cancelled")
	}
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, usageRepo.recorded())
	require.NotContains(t, rec.Body.String(), "resp_cancel")
}

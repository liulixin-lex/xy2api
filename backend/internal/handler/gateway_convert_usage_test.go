//go:build unit

package handler

import (
	"bytes"
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
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Review DELTA-P1-1: since 0.4.5 a Gateway conversion of an Anthropic
// upstream that breaks after output returns its partial result with an
// error. The Responses and Chat handlers must bill what the upstream
// already metered (message_start input and cache tokens), exactly once, as
// the native Messages handler does (#5148).

const (
	gwUsageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":40}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
	gwUsageOverloaded = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	gwUsageComplete   = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

type gwUsageReadError struct{ err error }

func (r gwUsageReadError) Read([]byte) (int, error) { return 0, r.err }

type gwUsageUpstream struct{ body func() io.Reader }

func (u *gwUsageUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(u.body()), Request: req}, nil
}

func (u *gwUsageUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

type gwUsageLogRepo struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []*service.UsageLog
}

func (r *gwUsageLogRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	return true, nil
}

func (r *gwUsageLogRepo) recorded() []*service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageLog(nil), r.logs...)
}

// gwUsageWriter fails every write after the first semantic output when
// failAfterOutput is set: the client left after seeing content.
type gwUsageWriter struct {
	*httptest.ResponseRecorder
	failAfterOutput bool
	sawOutput       bool
}

func (w *gwUsageWriter) Write(p []byte) (int, error) {
	if w.failAfterOutput && w.sawOutput {
		return 0, errors.New("client went away")
	}
	if strings.Contains(string(p), "hello") {
		w.sawOutput = true
	}
	return w.ResponseRecorder.Write(p)
}

func TestGatewayConvertBreakAfterOutputBillsPartialUsageOnce(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name, path, body string
		call             func(*GatewayHandler) func(*gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"claude-test","input":"hello","stream":true}`, func(h *GatewayHandler) func(*gin.Context) { return h.Responses }},
		{"chat", "/v1/chat/completions", `{"model":"claude-test","messages":[{"role":"user","content":"hello"}],"stream":true}`, func(h *GatewayHandler) func(*gin.Context) { return h.ChatCompletions }},
	} {
		for _, tc := range []struct {
			name            string
			body            func() io.Reader
			failAfterOutput bool
			wantOutput      int
		}{
			{"error_event", func() io.Reader { return strings.NewReader(gwUsageStart + gwUsageOverloaded) }, false, 0},
			{"missing_terminal", func() io.Reader { return strings.NewReader(gwUsageStart) }, false, 0},
			{"read_error", func() io.Reader {
				return io.MultiReader(strings.NewReader(gwUsageStart), gwUsageReadError{io.ErrUnexpectedEOF})
			}, false, 0},
			{"client_gone_then_error", func() io.Reader { return strings.NewReader(gwUsageStart + gwUsageOverloaded) }, true, 0},
			{"complete", func() io.Reader { return strings.NewReader(gwUsageStart + gwUsageComplete) }, false, 7},
		} {
			t.Run(endpoint.name+"/"+tc.name, func(t *testing.T) {
				usageRepo := &gwUsageLogRepo{}
				h := newGatewayConvertUsageHandler(t, usageRepo, &gwUsageUpstream{body: tc.body})
				writer := &gwUsageWriter{ResponseRecorder: httptest.NewRecorder(), failAfterOutput: tc.failAfterOutput}
				c := newGatewayConvertUsageContext(writer, endpoint.path, endpoint.body)

				endpoint.call(h)(c)

				require.Eventually(t, func() bool { return len(usageRepo.recorded()) > 0 }, 3*time.Second, 10*time.Millisecond,
					"the usage the upstream already metered is billed")
				time.Sleep(100 * time.Millisecond)
				logs := usageRepo.recorded()
				require.Len(t, logs, 1, "billed exactly once")
				require.Equal(t, 100, logs[0].InputTokens)
				require.Equal(t, 40, logs[0].CacheReadTokens)
				require.Equal(t, tc.wantOutput, logs[0].OutputTokens)
			})
		}
	}
}

func newGatewayConvertUsageHandler(t *testing.T, usageRepo service.UsageLogRepository, upstream service.HTTPUpstream) *GatewayHandler {
	t.Helper()
	groupID := int64(9200)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	account := &service.Account{
		ID: 9201, Name: "anthropic-usage", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "sk-test"},
		AccountGroups: []service.AccountGroup{{AccountID: 9201, GroupID: groupID}},
	}
	schedulerSnapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, usageRepo, nil, nil, nil, nil, nil, cfg,
		schedulerSnapshot, nil, service.NewBillingService(cfg, nil), nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil,
		&service.TLSFingerprintProfileService{}, nil, nil, nil, nil, nil,
	)
	billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	return &GatewayHandler{
		gatewayService:      gatewayService,
		billingCacheService: billingCacheService,
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:  1,
		cfg:                 cfg,
	}
}

func newGatewayConvertUsageContext(writer http.ResponseWriter, path, body string) *gin.Context {
	groupID := int64(9200)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	apiKey := &service.APIKey{
		ID: 9202, UserID: 9203, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9203, Concurrency: 10, Balance: 100},
	}
	c, _ := gin.CreateTestContext(writer)
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
	return c
}

// A client that cancels while the opening is still staged is billed
// nothing, and its cancellation is no forward failure (DELTA-P2-3).
func TestGatewayConvertCancelBeforeOutputBillsNothing(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name, path, body string
		call             func(*GatewayHandler) func(*gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"claude-test","input":"hello","stream":true}`, func(h *GatewayHandler) func(*gin.Context) { return h.Responses }},
		{"chat", "/v1/chat/completions", `{"model":"claude-test","messages":[{"role":"user","content":"hello"}],"stream":true}`, func(h *GatewayHandler) func(*gin.Context) { return h.ChatCompletions }},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			usageRepo := &gwUsageLogRepo{}
			h := newGatewayConvertUsageHandler(t, usageRepo, &gwUsageUpstream{body: func() io.Reader { return pr }})
			recorder := httptest.NewRecorder()
			c := newGatewayConvertUsageContext(recorder, endpoint.path, endpoint.body)
			ctx, cancel := context.WithCancel(c.Request.Context())
			c.Request = c.Request.WithContext(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				endpoint.call(h)(c)
			}()
			_, err := io.WriteString(pw, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cancel\",\"usage\":{\"input_tokens\":100}}}\n\n")
			require.NoError(t, err)
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("handler did not return after the client cancelled")
			}
			time.Sleep(100 * time.Millisecond)
			require.Empty(t, usageRepo.recorded())
			require.NotContains(t, recorder.Body.String(), "msg_cancel")
		})
	}
}

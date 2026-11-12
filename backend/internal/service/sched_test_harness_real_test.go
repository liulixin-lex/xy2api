//go:build unit

package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedF6Request struct {
	method, url, host, proxy string
	account                  int64
	headers                  http.Header
	body                     map[string]any
}
type schedF6Upstream struct {
	service.HTTPUpstream
	requests []schedF6Request
	terminal string
	status   int
	errBody  string
}

func (u *schedF6Upstream) Do(req *http.Request, proxy string, account int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err = json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	u.requests = append(u.requests, schedF6Request{req.Method, req.URL.String(), req.Host, proxy, account, req.Header.Clone(), value})
	if u.status != 0 {
		return &http.Response{StatusCode: u.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(u.errBody))}, nil
	}
	response := `{"id":"resp_sched_f6","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":100000,"output_tokens":2,"input_tokens_details":{"cached_tokens":90000}}}`
	terminal := "response.completed"
	if u.terminal != "" {
		terminal = u.terminal
		response = strings.Replace(response, `"completed"`, `"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, 1)
	}
	sse := "data: " + `{"type":"response.output_text.delta","delta":"answer"}` + "\n\ndata: " + `{"type":"` + terminal + `","response":` + response + "}\n\n"
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}, nil
}

func schedF6Normalize(req schedF6Request) schedF6Request {
	req.headers = req.headers.Clone()
	req.body = maps.Clone(req.body)
	for _, key := range []string{"Session_id", "Conversation_id", "X-Session-Id", "X-Conversation-Id", "X-Session-Affinity", "X-Codex-Session-Id", "X-Codex-Turn-Metadata", "X-Codex-Window-Id"} {
		if values, ok := req.headers[key]; ok {
			for i := range values {
				values[i] = "<session>"
			}
		}
	}
	if _, ok := req.body["prompt_cache_key"]; ok {
		req.body["prompt_cache_key"] = "<cache>"
	}
	return req
}

func TestSchedTestRealForwardingF6(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := service.Resilience()
	service.SetResilienceInstance(nil)
	t.Cleanup(func() { service.SetResilienceInstance(old) })
	for _, kind := range []string{"oauth", "oauth_codex_cli_only", "api_key", "api_key_mapping_a_to_b"} {
		t.Run(kind, func(t *testing.T) {
			model, outbound := "gpt-6.1-sol", "gpt-6.1-sol"
			account := &service.Account{ID: 9901, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "upstream-secret", "base_url": "https://f6.example"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
			if strings.HasPrefix(kind, "oauth") {
				account.Type = service.AccountTypeOAuth
				account.Credentials = map[string]any{"access_token": "upstream-secret", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "chatgpt_account_id": "f6-chatgpt-account"}
			}
			if kind == "oauth_codex_cli_only" {
				account.Extra["codex_cli_only"] = true
			}
			if kind == "api_key_mapping_a_to_b" {
				model = "a"
				outbound = "b"
				account.Credentials["model_mapping"] = map[string]any{"a": "b"}
			}
			body := []byte(`{"model":"` + model + `","stream":true,"store":false,"instructions":"Return a short answer","input":[{"role":"user","content":[{"type":"input_text","text":"first prompt"}]},{"role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},{"role":"user","content":[{"type":"input_text","text":"second prompt"}]}],"tools":[],"reasoning":{"effort":"medium","summary":"auto"},"max_output_tokens":128,"prompt_cache_key":"sched-test:f6:small"}`)
			testUp, controlUp := &schedF6Upstream{}, &schedF6Upstream{}
			testGateway, _, _, _, isolated := service.NewSchedTestF6Fixture(t, account, testUp)
			result, samples, answer, err := service.SchedTestForwardForTest(context.Background(), testGateway, account, model, body, true, 90000)
			require.NoError(t, err)
			require.Equal(t, "answer", answer)
			require.NotNil(t, result)
			require.Nil(t, result.CacheDiagnostic)
			require.Len(t, samples, 1)
			require.Equal(t, schedquality.OutcomeOK, samples[0].Outcome)
			require.Equal(t, model, samples[0].Model)
			require.True(t, samples[0].Warm)
			require.NotNil(t, samples[0].TTFOMs)
			require.Equal(t, 100000, *samples[0].PromptTokens)
			require.Equal(t, 90000, *samples[0].PrevPromptTokens)
			isolated()
			gateway, slots, billing, cfg, _ := service.NewSchedTestF6Fixture(t, account, controlUp)
			h := handler.NewOpenAIGatewayHandler(gateway, slots, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				groupID := int64(4201)
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1801, GroupID: &groupID, User: &service.User{ID: 1701, Status: service.StatusActive}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1701, Concurrency: 2})
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
				service.InstallDeliveryTracking(c, time.Now())
				c.Next()
			})
			router.POST("/v1/responses", h.Responses)
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("User-Agent", service.DefaultOpenAICodexUserAgent)
			req.Header.Set("originator", openai.CodexDefaultOriginator)
			req.Header.Set("session_id", "019a6899-7bb9-7000-8000-000000000001")
			req.Header.Set("x-codex-window-id", "019a6899-7bb9-7000-8000-000000000001:0")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Len(t, testUp.requests, 1)
			require.Len(t, controlUp.requests, 1)
			require.Equal(t, outbound, testUp.requests[0].body["model"])
			require.NotEmpty(t, testUp.requests[0].headers.Get("session_id"))
			if strings.HasPrefix(kind, "oauth") {
				require.NotEmpty(t, testUp.requests[0].headers.Get("x-codex-window-id"))
			}
			for _, name := range []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
				require.Empty(t, testUp.requests[0].headers.Get(name))
			}
			require.Equal(t, "Bearer upstream-secret", testUp.requests[0].headers.Get("Authorization"))
			require.Equal(t, schedF6Normalize(controlUp.requests[0]), schedF6Normalize(testUp.requests[0]))
			// The same parser must retain usage on a benign incomplete response.
			testUp.terminal = "response.incomplete"
			result, samples, _, err = service.SchedTestForwardForTest(context.Background(), testGateway, account, model, body, true, 90000)
			require.NoError(t, err)
			require.Len(t, samples, 1)
			require.Equal(t, schedquality.OutcomeOK, samples[0].Outcome)
			require.NotNil(t, result)
			require.Equal(t, 100000, *samples[0].PromptTokens)
			require.Equal(t, testUp.requests[0].headers.Get("session_id"), testUp.requests[1].headers.Get("session_id"))
			require.Equal(t, testUp.requests[0].headers.Get("x-codex-window-id"), testUp.requests[1].headers.Get("x-codex-window-id"))
			isolated()
		})
	}
}

type schedF6ObservedForwarder struct {
	service.SchedTestForwarder
	events []*service.OpsUpstreamErrorEvent
}

func (f *schedF6ObservedForwarder) Forward(ctx context.Context, c *gin.Context, account *service.Account, body []byte) (*service.OpenAIForwardResult, error) {
	result, err := f.SchedTestForwarder.Forward(ctx, c, account, body)
	if events, ok := c.Get(service.OpsUpstreamErrorsKey); ok {
		f.events, _ = events.([]*service.OpsUpstreamErrorEvent)
	}
	return result, err
}

func TestSchedTestRealForwardingF6OpsSourceAndRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := service.Resilience()
	service.SetResilienceInstance(nil)
	t.Cleanup(func() { service.SetResilienceInstance(old) })
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		t.Run(kind, func(t *testing.T) {
			resetAt := time.Now().Add(time.Hour).Truncate(time.Second)
			account := &service.Account{ID: 9902, Platform: service.PlatformOpenAI, Type: kind, Status: service.StatusActive, Schedulable: true, Concurrency: 2,
				Credentials: map[string]any{"api_key": "upstream-secret", "base_url": "https://f6.example"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
			if kind == service.AccountTypeOAuth {
				account.Credentials = map[string]any{"access_token": "upstream-secret", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "chatgpt_account_id": "f6-chatgpt-account"}
				account.Extra["codex_cli_only"] = true
			}
			up := &schedF6Upstream{status: http.StatusTooManyRequests, errBody: fmt.Sprintf(`{"error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"Usage limit reached","resets_at":%d}}`, resetAt.Unix())}
			gw, _, _, _, isolated := service.NewSchedTestF6Fixture(t, account, up)
			forward := &schedF6ObservedForwarder{SchedTestForwarder: gw}
			_, samples, _, err := service.SchedTestForwardForTest(context.Background(), forward, account, "gpt-6.1-sol", []byte(`{"model":"gpt-6.1-sol","stream":true,"input":"hi","prompt_cache_key":"sched-test:f6:limit"}`), false, 0)
			require.Error(t, err)
			require.Len(t, up.requests, 1)
			require.Len(t, samples, 1)
			require.Equal(t, schedquality.OutcomeOther, samples[0].Outcome, "quota errors retain the real capture classification")
			require.NotEmpty(t, forward.events)
			for _, event := range forward.events {
				require.Equal(t, "sched_test", event.Source)
				require.True(t, event.SkipMonitoring)
				require.Equal(t, http.StatusTooManyRequests, event.UpstreamStatusCode)
				require.Equal(t, account.ID, event.AccountID)
			}
			require.NotNil(t, account.RateLimitResetAt, "real Forward must persist upstream rate-limit disposition")
			require.Equal(t, resetAt.Unix(), account.RateLimitResetAt.Unix())
			isolated()
		})
	}
}

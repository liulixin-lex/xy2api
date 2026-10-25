//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler/dto"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type traceVisibilityLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (w *traceVisibilityLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.Write(p)
}
func (w *traceVisibilityLog) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Buffer.String()
}

// Only settings reads are needed to publish an enabled production snapshot.
type traceVisibilityPolicyStore struct{ service.SettingRepository }

func (*traceVisibilityPolicyStore) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{"cache_observability_policy": `{"rev":1,"spec":{"enabled":true,"chain_percent":100}}`}, nil
}

type traceVisibilityFailureUpstream struct{ calls atomic.Int32 }

func (u *traceVisibilityFailureUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"synthetic upstream unavailable"}}`)), Request: req}, nil
}
func (u *traceVisibilityFailureUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

type traceVisibilityFallbackAccounts struct{ service.AccountRepository }

func (traceVisibilityFallbackAccounts) ListByGroup(_ context.Context, id int64) ([]service.Account, error) {
	if id == 911 {
		return []service.Account{{ID: 811, Status: service.StatusActive, Schedulable: true}}, nil
	}
	return nil, nil
}

type traceVisibilityFallbackGroups struct {
	service.GroupRepository
	fallback *service.Group
}

func (r traceVisibilityFallbackGroups) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return r.fallback, nil
}

type traceVisibilityFallbackStore struct{ service.ResilienceStore }

func (*traceVisibilityFallbackStore) FallbackKey(context.Context, int64, int64) (bool, error) {
	return false, nil
}
func (*traceVisibilityFallbackStore) MarkFallbackKey(context.Context, int64, int64, time.Duration) error {
	return nil
}
func (*traceVisibilityFallbackStore) ClearGroupStable(context.Context, int64) error { return nil }

// INV-18: exercise the actual handler, forwarded response and billing row.
// Only internal cache_trace may disclose that an availability fallback ran.
func TestCacheTraceFallbackInvisibleInResponseAndUserUsage(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	defer gin.SetMode(previousMode)
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name                                  string
		sessionHeader, chat, stream, failover bool
	}{
		{"responses_stream_explicit_session", true, false, true, false},
		{"responses_stream_content_session", false, false, true, false},
		{"responses_buffered", true, false, false, false},
		{"chat_stream", true, true, true, false},
		{"chat_buffered", true, true, false, false},
		{"responses_exhausted_stream", true, false, true, true},
		{"responses_exhausted_buffered", true, false, false, true},
		{"chat_exhausted_stream", true, true, true, true},
		{"chat_exhausted_buffered", true, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &gwUsageLogRepo{}
			terminal := strings.Replace(ptUsageCompleted, `"input_tokens":11`, `"input_tokens":12000,"input_tokens_details":{"cached_tokens":0}`, 1)
			terminal = strings.Replace(terminal, `"output":[]`, `"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]`, 1)
			policyConfig := &config.Config{}
			policyConfig.Gateway.OpenAICache.DiagnosticsEnabled = true
			policies := service.NewCachePolicyService(&traceVisibilityPolicyStore{}, policyConfig)
			require.NoError(t, policies.Refresh(context.Background()))
			require.True(t, policies.Snapshot().TraceEnabled())
			var upstream service.HTTPUpstream = &ptUsageUpstream{body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening + terminal)) }}
			failed := &traceVisibilityFailureUpstream{}
			if tc.failover {
				upstream = failed
			}
			h := newPassthroughUsageHandler(t, repo, upstream, 0, policies)
			h.cfg.JWT.Secret = "trace-visibility-synthetic-secret"
			h.cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			h.cfg.Gateway.CacheTrace.MinCacheableTokens = 1024
			originID := int64(918)
			origin := &service.APIKey{ID: 912, UserID: 913, GroupID: &originID,
				User:  &service.User{ID: 913, Status: service.StatusActive},
				Group: &service.Group{ID: originID, Name: "public-group", Platform: service.PlatformOpenAI, Status: service.StatusActive}}
			fallback := &service.Group{ID: 911, Name: "private-fallback-group", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			origin.Group.AvailabilityFallbackGroupID = &fallback.ID
			origin.Group.Hydrated = true
			fallbackService := service.NewResilienceService(nil, &traceVisibilityFallbackStore{})
			fallbackService.ConfigureFallbackGroups(traceVisibilityFallbackAccounts{}, traceVisibilityFallbackGroups{fallback: fallback})
			previous := service.Resilience()
			service.SetResilienceInstance(fallbackService)
			t.Cleanup(func() { service.SetResilienceInstance(previous) })
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			var logs traceVisibilityLog
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(old)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), origin)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 913, Concurrency: 1})
				c.Next()
			}, middleware.ResilienceAvailabilityFallback())
			path := "/v1/responses"
			body := map[string]any{"model": "gpt-5.4", "input": "trace-visibility-private-body", "stream": tc.stream}
			call := h.Responses
			if tc.chat {
				path, call = "/v1/chat/completions", h.ChatCompletions
				delete(body, "input")
				body["messages"] = []map[string]string{{"role": "user", "content": "trace-visibility-private-body"}}
			}
			router.POST(path, call)
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			if tc.sessionHeader {
				req.Header.Set("session_id", "trace-visibility-private-session")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, originID, st.OriginGroupID(), "the actual availability middleware selected fallback")
			if tc.failover {
				require.GreaterOrEqual(t, response.Code, 400)
				require.Positive(t, failed.calls.Load(), "the upstream was attempted before failover exhaustion")
				require.Empty(t, repo.recorded(), "an undelivered upstream error has no billed usage")
				headers, err := json.Marshal(response.Header())
				require.NoError(t, err)
				visible := string(headers) + response.Body.String()
				for _, field := range []string{"cache_trace", "route_reason", "fallback_group", "origin_group", "private-fallback-group", "conv_anchor", "session_digest", "fork_byte"} {
					require.NotContains(t, strings.ToLower(visible), field)
				}
				require.NotContains(t, logs.text(), `"msg":"cache_trace"`)
				return
			}
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), "hello")
			require.Eventually(t, func() bool { return len(repo.recorded()) == 1 }, 3*time.Second, 10*time.Millisecond)
			row := repo.recorded()[0]
			require.NotNil(t, row.OriginGroupID)
			require.Equal(t, originID, *row.OriginGroupID)
			userDTO := dto.UsageLogFromService(row)
			require.NotNil(t, userDTO.GroupID)
			require.Equal(t, originID, *userDTO.GroupID)
			userRow, err := json.Marshal(userDTO)
			require.NoError(t, err)
			headers, err := json.Marshal(response.Header())
			require.NoError(t, err)
			for _, visible := range []string{string(headers), response.Body.String(), string(userRow)} {
				for _, field := range []string{"cache_trace", "route_reason", "fallback_group", "origin_group", "private-fallback-group", "conv_anchor", "session_digest", "fork_byte"} {
					require.NotContains(t, strings.ToLower(visible), field)
				}
			}
			// Billing can save the row before its observer settles. Wait for that
			// event, but do not turn the legal asynchronous not_ready state into a
			// timing-sensitive test failure. The service test independently forces
			// worker readiness and asserts the complete fallback route snapshot.
			require.Eventually(t, func() bool { return strings.Contains(logs.text(), `"msg":"cache_trace"`) }, 3*time.Second, 10*time.Millisecond)
			internal := logs.text()
			events := 0
			for _, line := range strings.Split(strings.TrimSpace(internal), "\n") {
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				if event["msg"] != "cache_trace" {
					continue
				}
				events++
				// HTTP requires model, and the existing content fallback includes model.
				// Omitting the header therefore derives a session; it is not no_session.
				require.Regexp(t, `^(?:v2:)?[0-9a-f]{16}$`, event["session_digest"])
				if route, ok := event["route_reason"]; ok {
					require.Equal(t, "fallback_group", route)
				} else {
					require.Equal(t, "n/a_sampled_out", event["loss_class"])
					require.Equal(t, "not_ready", event["na_reason"])
				}
				require.NotContains(t, event, "origin_group_id")
				require.NotContains(t, event, "origin_group_name")
			}
			require.Equal(t, 1, events)
			for _, secret := range []string{"trace-visibility-private-body", "trace-visibility-private-session", "trace-visibility-synthetic-secret"} {
				require.NotContains(t, internal, secret)
			}
		})
	}
}

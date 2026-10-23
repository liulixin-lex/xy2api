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
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler/dto"
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

// INV-18: exercise the actual handler, forwarded response and billing row.
// Only internal cache_trace may disclose that an availability fallback ran.
func TestCacheTraceFallbackInvisibleInResponseAndUserUsage(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	defer gin.SetMode(previousMode)
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name          string
		sessionHeader bool
	}{
		{"explicit_session_header", true},
		{"content_derived_session_without_header", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &gwUsageLogRepo{}
			terminal := strings.Replace(ptUsageCompleted, `"input_tokens":11`, `"input_tokens":12000,"input_tokens_details":{"cached_tokens":0}`, 1)
			policyConfig := &config.Config{}
			policyConfig.Gateway.OpenAICache.DiagnosticsEnabled = true
			policies := service.NewCachePolicyService(&traceVisibilityPolicyStore{}, policyConfig)
			require.NoError(t, policies.Refresh(context.Background()))
			require.True(t, policies.Snapshot().TraceEnabled())
			h := newPassthroughUsageHandler(t, repo, &ptUsageUpstream{body: func() io.ReadCloser { return io.NopCloser(strings.NewReader(ptUsageOpening + terminal)) }}, 0, policies)
			h.cfg.JWT.Secret = "trace-visibility-synthetic-secret"
			h.cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			h.cfg.Gateway.CacheTrace.MinCacheableTokens = 1024
			originID := int64(918)
			origin := &service.APIKey{ID: 912, UserID: 913, GroupID: &originID,
				User:  &service.User{ID: 913, Status: service.StatusActive},
				Group: &service.Group{ID: originID, Name: "public-group", Platform: service.PlatformOpenAI, Status: service.StatusActive}}
			moved := service.CloneAPIKeyWithGroup(origin, &service.Group{ID: 911, Name: "private-fallback-group", Platform: service.PlatformOpenAI, Status: service.StatusActive})
			st := resilience.NewRequestState()
			require.True(t, st.MarkFallback(originID))
			ctx := resilience.WithRequestState(context.Background(), st)
			service.RememberAvailabilityFallbackOrigin(ctx, origin)
			var logs traceVisibilityLog
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(old)
			router := gin.New()
			router.POST("/v1/responses", func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), moved)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 913, Concurrency: 1})
				h.Responses(c)
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"trace-visibility-private-body","stream":true}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			if tc.sessionHeader {
				req.Header.Set("session_id", "trace-visibility-private-session")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
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

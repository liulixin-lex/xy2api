//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newFastFailRouter(t *testing.T, keyID int64, handler gin.HandlerFunc) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	user := &service.User{ID: keyID, Role: service.RoleUser, Status: service.StatusActive, Balance: 10, Concurrency: 3}
	apiKey := &service.APIKey{ID: keyID, UserID: user.ID, Key: "ff-key", Status: service.StatusActive, User: user}
	repo := &stubApiKeyRepo{getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
		if key != apiKey.Key {
			return nil, service.ErrAPIKeyNotFound
		}
		clone := *apiKey
		u := *user
		clone.User = &u
		return &clone, nil
	}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg), nil, cfg)))
	router.POST("/v1/messages", handler)
	router.GET("/v1/models", handler)
	return router, apiKey.Key
}

func serve(router *gin.Engine, method, path, key string, headers map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	// The request state is attached before authentication in production.
	req = req.WithContext(resilience.WithRequestState(req.Context(), resilience.NewRequestState()))
	req.Header.Set("x-api-key", key)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	router.ServeHTTP(w, req)
	return w
}

func TestFastFailRetryStormOnSingleAccountGroup(t *testing.T) {
	// Spec: after the tenth identical 503 within 30 s, the following requests
	// return the same 503 immediately.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })
	calls := 0
	router, key := newFastFailRouter(t, 9101, func(c *gin.Context) {
		calls++
		resilience.StateFrom(c.Request.Context()).MarkGroupNoAccount()
		c.Header("Retry-After", "20")
		body := gin.H{"type": "error", "error": gin.H{"type": "api_error", "message": "Service temporarily unavailable"}}
		c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: 503, ErrType: "api_error", Message: "Service temporarily unavailable", Body: []byte(`{"type":"error","error":{"type":"api_error","message":"Service temporarily unavailable"}}`)})
		c.JSON(http.StatusServiceUnavailable, body)
	})
	for i := 0; i < 10; i++ {
		w := serve(router, http.MethodPost, "/v1/messages", key, nil)
		require.Equal(t, 503, w.Code)
	}
	require.Equal(t, 10, calls)
	w := serve(router, http.MethodPost, "/v1/messages", key, nil)
	require.Equal(t, 503, w.Code)
	require.Equal(t, 10, calls, "replayed before the handler")
	require.Equal(t, "1", w.Header().Get("X-XY-Fast-Fail"))
	require.NotEmpty(t, w.Header().Get("Retry-After"))
	require.JSONEq(t, `{"type":"error","error":{"type":"api_error","message":"Service temporarily unavailable"}}`, w.Body.String())

	// Exempt: reads and idempotent replays still reach the handler.
	serve(router, http.MethodGet, "/v1/models", key, nil)
	require.Equal(t, 11, calls)
	serve(router, http.MethodPost, "/v1/messages", key, map[string]string{"Idempotency-Key": "abc"})
	require.Equal(t, 12, calls)
}

func TestFastFailOffAndSuccessReset(t *testing.T) {
	orig := *resilience.Current()
	s := resilience.Defaults()
	s.Errors.FastFail.Enabled = false
	resilience.Publish(s)
	t.Cleanup(func() { resilience.Publish(orig) })
	calls := 0
	router, key := newFastFailRouter(t, 9102, func(c *gin.Context) {
		calls++
		c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: 503, ErrType: "api_error", Message: "x", Body: []byte(`{}`)})
		c.JSON(http.StatusServiceUnavailable, gin.H{})
	})
	for i := 0; i < 15; i++ {
		serve(router, http.MethodPost, "/v1/messages", key, nil)
	}
	require.Equal(t, 15, calls, "switch off: never replayed")

	// A success clears an accumulating streak.
	resilience.Publish(resilience.Defaults())
	fail := true
	router2, key2 := newFastFailRouter(t, 9103, func(c *gin.Context) {
		calls++
		if fail {
			c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: 503, ErrType: "api_error", Message: "x", Body: []byte(`{}`)})
			c.JSON(http.StatusServiceUnavailable, gin.H{})
			return
		}
		c.JSON(http.StatusOK, gin.H{})
	})
	for i := 0; i < 9; i++ {
		serve(router2, http.MethodPost, "/v1/messages", key2, nil)
	}
	fail = false
	serve(router2, http.MethodPost, "/v1/messages", key2, nil)
	fail = true
	before := calls
	for i := 0; i < 5; i++ {
		serve(router2, http.MethodPost, "/v1/messages", key2, nil)
	}
	require.Equal(t, before+5, calls, "the streak restarted after the success")
}

func TestResilienceRequestBudgetKinds(t *testing.T) {
	for _, tc := range []struct {
		path, upgrade string
		kind          resilience.RequestKind
		budget        bool
	}{
		{"/v1/responses", "", resilience.KindText, true},
		{"/v1/messages", "", resilience.KindText, true},
		{"/v1/images/generations", "", resilience.KindImage, true},
		{"/v1/images/edits/async", "", resilience.KindImage, true},
		{"/v1/videos/generations", "", resilience.KindImage, true},
		{"/v1/responses", "websocket", resilience.KindText, false},
	} {
		r := httptest.NewRequest(http.MethodPost, tc.path, nil)
		if tc.upgrade != "" {
			r.Header.Set("Upgrade", tc.upgrade)
		}
		ctx := withResilienceRequestState(r.Context(), r)
		b := resilience.StateFrom(ctx).Budget()
		if !tc.budget {
			require.Nil(t, b, tc.path)
			continue
		}
		require.NotNil(t, b, tc.path)
		require.Equal(t, tc.kind, b.Kind(), tc.path)
		require.Equal(t, tc.kind == resilience.KindText, !b.Deadline().IsZero(), tc.path)
	}
}

func TestResilienceEdgeMarkerTrustsOnlyProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Server.TrustedProxiesConfigured = true
	cfg.Server.TrustedProxies = []string{"10.0.0.0/8", "127.0.0.1"}
	for _, tc := range []struct {
		peer, header, want string
	}{
		{"127.0.0.1:5000", "cf", "cf"},
		{"10.1.2.3:5000", "ESA", "esa"},
		{"203.0.113.9:5000", "cf", ""},   // forged by a direct client
		{"127.0.0.1:5000", "akamai", ""}, // not an allowed edge
		{"127.0.0.1:5000", "", ""},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		r.RemoteAddr = tc.peer
		if tc.header != "" {
			r.Header.Set(resilience.EdgeHeader, tc.header)
		}
		c.Request = r.WithContext(resilience.WithRequestState(r.Context(), resilience.NewRequestState()))
		resilienceNoteEdge(c, cfg)
		require.Equal(t, tc.want, resilience.StateFrom(c.Request.Context()).Edge(), tc.peer+" "+tc.header)
	}

	// Trusted proxies not configured: never trusted.
	unconfigured := &config.Config{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set(resilience.EdgeHeader, "cf")
	c.Request = r.WithContext(resilience.WithRequestState(r.Context(), resilience.NewRequestState()))
	resilienceNoteEdge(c, unconfigured)
	require.Empty(t, resilience.StateFrom(c.Request.Context()).Edge())
}

func TestTurnCapResetOnlyOnRealSuccess(t *testing.T) {
	// Spec: a successful request ends the conversation's failed-turn streak;
	// a 200 that carried an in-stream failure does not.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })
	key := resilience.TurnKey(9102, 0, "m", "s")
	t.Cleanup(func() { resilience.ProcessTurnCap().Reset(key) })
	failInStream := false
	router, apiKey := newFastFailRouter(t, 9102, func(c *gin.Context) {
		st := resilience.StateFrom(c.Request.Context())
		st.NoteTurn(0, "m", "s")
		if failInStream {
			st.MarkTurnFailed()
		}
		c.Status(http.StatusOK)
	})
	stateFirst := gin.New()
	stateFirst.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
		router.HandleContext(c)
		c.Abort()
	})
	now := time.Now()
	for i := 0; i < 3; i++ {
		resilience.ProcessTurnCap().Fail(key, time.Minute, now)
	}
	failInStream = true
	serve(stateFirst, http.MethodPost, "/v1/messages", apiKey, nil)
	require.Equal(t, 4, resilience.ProcessTurnCap().Fail(key, time.Minute, now), "in-stream failure keeps the streak")
	failInStream = false
	serve(stateFirst, http.MethodPost, "/v1/messages", apiKey, nil)
	require.Equal(t, 1, resilience.ProcessTurnCap().Fail(key, time.Minute, now), "success resets")
}

func TestFastFailIgnoresCappedTurns(t *testing.T) {
	// A capped turn's final 503 has no Retry-After; the fast fail must not
	// replay it with one, which would restart Codex's retries.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })
	calls := 0
	router, key := newFastFailRouter(t, 9103, func(c *gin.Context) {
		calls++
		resilience.StateFrom(c.Request.Context()).MarkGroupNoAccount()
		c.Set(resilience.GinKeyTurnCapped, true)
		c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: 503, ErrType: "api_error", Message: "x", Body: []byte(`{}`)})
		c.JSON(http.StatusServiceUnavailable, gin.H{})
	})
	for i := 0; i < 15; i++ {
		w := serve(router, http.MethodPost, "/v1/messages", key, nil)
		require.Empty(t, w.Header().Get("Retry-After"))
	}
	require.Equal(t, 15, calls)
}

func TestFastFailReplaysOnlyTheTrippingModel(t *testing.T) {
	// A key whose requests for one model keep failing still reaches the
	// handler for its other models.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })
	calls := map[string]int{}
	handler := func(c *gin.Context) {
		var req struct {
			Model string `json:"model"`
		}
		_ = c.ShouldBindJSON(&req)
		calls[req.Model]++
		resilience.StateFrom(c.Request.Context()).NoteTurn(1, req.Model, "")
		if req.Model == "down" {
			resilience.StateFrom(c.Request.Context()).MarkGroupNoAccount()
			c.Header("Retry-After", "20")
			c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: 503, ErrType: "api_error", Message: "no account", Body: []byte(`{"error":{"message":"no account"}}`)})
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "no account"}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
	// As in production, the request state is attached before authentication.
	inner, key := newFastFailRouter(t, 9105, handler)
	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/messages", func(c *gin.Context) { inner.HandleContext(c) })
	post := func(model string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"`+model+`","messages":[]}`))
		req.Header.Set("x-api-key", key)
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}
	for i := 0; i < 10; i++ {
		require.Equal(t, http.StatusServiceUnavailable, post("down").Code)
	}
	w := post("down")
	require.Equal(t, "1", w.Header().Get("X-XY-Fast-Fail"))
	require.Equal(t, 10, calls["down"], "the failing model is replayed")

	w = post("up")
	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, w.Header().Get("X-XY-Fast-Fail"))
	require.Equal(t, 1, calls["up"], "another model reaches the handler with its body intact")
}

func TestFastFailCountsOnlyGroupLevelNoAccount(t *testing.T) {
	// Review F5: a key with several sessions sees 503s that only concern one
	// session (a new session in a sticky-only group); they must not stop the
	// key's bound sessions.
	orig := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(orig) })
	calls := 0
	router, key := newFastFailRouter(t, 9107, func(c *gin.Context) {
		calls++
		c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: 503, ErrType: "api_error", Message: "Service temporarily unavailable", Body: []byte(`{"error":{"type":"api_error","message":"Service temporarily unavailable"}}`)})
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Service temporarily unavailable"}})
	})
	for i := 0; i < 12; i++ {
		require.Equal(t, 503, serve(router, http.MethodPost, "/v1/messages", key, nil).Code)
	}
	require.Equal(t, 12, calls, "request-dependent 503s never trip the key")

	// A configuration error holds for every request of the key and model.
	calls = 0
	router, key = newFastFailRouter(t, 9108, func(c *gin.Context) {
		calls++
		resilience.StateFrom(c.Request.Context()).MarkConfigError()
		c.Set(resilience.GinKeyErrorResponse, resilience.ErrorResponse{Status: 503, ErrType: "api_error", Message: "config", Body: []byte(`{"error":{"message":"config"}}`)})
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "config"}})
	})
	for i := 0; i < 12; i++ {
		serve(router, http.MethodPost, "/v1/messages", key, nil)
	}
	require.Equal(t, 10, calls, "configuration 503s still trip the key")
}

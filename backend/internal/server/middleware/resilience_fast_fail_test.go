//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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

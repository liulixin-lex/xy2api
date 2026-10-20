package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func serveReady(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/ready", Handler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	return w
}

func TestReadyReportsFailedDependenciesWithoutDetails(t *testing.T) {
	reset()
	t.Cleanup(reset)
	Register("database", func(context.Context) error { return errors.New("dial tcp 10.0.0.5:5432: refused") })
	Register("redis", func(context.Context) error { return nil })

	w := serveReady(t)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body struct {
		Status string   `json:"status"`
		Failed []string `json:"failed"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "not_ready", body.Status)
	require.Equal(t, []string{"database"}, body.Failed)
	require.NotContains(t, w.Body.String(), "10.0.0.5", "no internal detail leaks")
}

func TestReadyWhenEveryCheckPasses(t *testing.T) {
	reset()
	t.Cleanup(reset)
	Register("database", func(context.Context) error { return nil })
	w := serveReady(t)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"status":"ready"}`, w.Body.String())
}

func TestReadyBoundsSlowChecksAndReusesTheResult(t *testing.T) {
	reset()
	t.Cleanup(reset)
	var calls atomic.Int32
	Register("redis", func(ctx context.Context) error {
		calls.Add(1)
		<-ctx.Done()
		return ctx.Err()
	})
	start := time.Now()
	require.Equal(t, []string{"redis"}, Failed(context.Background()))
	require.Less(t, time.Since(start), checkTimeout+time.Second, "a hanging dependency is cut at the timeout")
	require.Equal(t, []string{"redis"}, Failed(context.Background()))
	require.Equal(t, int32(1), calls.Load(), "a second probe within a second reuses the result")
}

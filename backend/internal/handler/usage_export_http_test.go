package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUsageExportHTTP(t *testing.T) {
	dsn := os.Getenv("USAGE_EXPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	schema := fmt.Sprintf("export_http_%d", time.Now().UnixNano())
	_, err = db.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	defer func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) }()
	uri, err := url.Parse(dsn)
	require.NoError(t, err)
	q := uri.Query()
	q.Set("search_path", schema)
	uri.RawQuery = q.Encode()
	pool, err := sql.Open("postgres", uri.String())
	require.NoError(t, err)
	defer func() { _ = pool.Close() }()
	_, err = pool.Exec(`CREATE TABLE users(id bigint PRIMARY KEY);INSERT INTO users VALUES(1),(2)`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/252_usage_export_tasks.sql")
	require.NoError(t, err)
	_, err = pool.Exec(string(migration))
	require.NoError(t, err)
	cfg := usageexport.Defaults()
	cfg.Enabled = true
	cfg.Directory = t.TempDir()
	cfg.FreeBytes = 0
	cfg.MaxFileBytes = 1 << 20
	cfg.TaskDiskBytes = 8 << 20
	engine, err := usageexport.New(pool, cfg, func(_ context.Context, _ *sql.Tx, _ usageexport.Options, emit func([]string) error) error {
		return emit([]string{"now", "=SUM(1,2)"})
	}, nil)
	require.NoError(t, err)
	engine.Start()
	defer engine.Stop()
	user := &service.User{ID: 1, Status: service.StatusActive, Role: service.RoleUser, TokenVersion: 3}
	h := UsageExportHandler{engine: engine, user: &UsageHandler{}, users: exportTestUsers{user}, sessions: exportTestSessions{data: &service.RefreshTokenData{UserID: 1, FamilyID: "family", TokenVersion: 3, ExpiresAt: time.Now().Add(time.Hour)}}}
	router := gin.New()
	router.POST("/api/v1/usage/exports", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		h.Create(c)
	})
	router.GET("/api/v1/usage/exports/:id/download", h.Download)
	create := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/usage/exports", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "http-test-key")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, 400, create(`{"user_id":2}`).Code)
	w := create(`{"timezone":"UTC","language":"en"}`)
	require.Equal(t, 202, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "storage_key")
	var result struct {
		Data usageexport.Task `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	id := result.Data.ID
	require.Equal(t, 202, create(`{"timezone":"UTC","language":"en"}`).Code)
	require.Eventually(t, func() bool {
		task, er := engine.Get(context.Background(), 1, "user", id)
		return er == nil && task.Status == "succeeded"
	}, 10*time.Second, 50*time.Millisecond)
	identity := usageexport.TicketIdentity{Owner: 1, Session: "family", TokenVersion: 3, TokenExpires: time.Now().Add(time.Minute).Unix()}
	ticket := func() string {
		value, er := engine.Ticket(context.Background(), 1, "user", id, identity)
		require.NoError(t, er)
		return value
	}
	download := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/usage/exports/"+id+"/download", nil)
		r.AddCookie(&http.Cookie{Name: "usage_export_ticket", Value: token})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	token := ticket()
	w = download(token)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "'=SUM(1,2)")
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Header().Get("Content-Disposition"), "attachment;")
	require.NotEmpty(t, w.Header().Get("Content-Length"))
	require.Equal(t, 401, download(token).Code)
	token = ticket()
	user.Status = "disabled"
	require.Equal(t, 401, download(token).Code)
	user.Status = service.StatusActive
	token = ticket()
	require.NoError(t, engine.Delete(context.Background(), 1, "user", id))
	require.Equal(t, 410, download(token).Code)
}

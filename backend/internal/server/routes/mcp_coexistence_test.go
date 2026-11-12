//go:build unit

package routes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler"
	readonly "github.com/liulixin-lex/xy2api/internal/mcp"
	"github.com/stretchr/testify/require"
)

type coexistReader struct{}

func (coexistReader) Query(context.Context, readonly.QueryRequest) (readonly.QueryResult, error) {
	panic("discovery must not read business data")
}
func (coexistReader) QueryRuntime(context.Context, readonly.QueryRequest) (readonly.QueryResult, error) {
	panic("discovery must not read runtime data")
}

func TestMCPReadonlyAndManagementDiscoveryCoexist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectClose()
	cfg := &config.Config{
		MCP:           config.MCPConfig{Enabled: true, PublicURL: "https://platform.example", OAuthHMACSecret: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))},
		ManagementMCP: config.ManagementMCPConfig{PublicURL: "https://platform.example/api/v1/mcp"},
	}
	ro, err := readonly.NewServer(cfg, db, &readonly.ReaderDB{DB: db}, nil, nil, nil, nil, coexistReader{}, coexistReader{})
	require.NoError(t, err)
	t.Cleanup(func() { ro.Close(); require.NoError(t, mock.ExpectationsWereMet()) })
	h := mcpRouteHandlers()
	h.MCPOAuth = handler.NewMCPOAuthHandler(nil, nil, nil, nil, cfg)
	router := gin.New()
	require.NotPanics(t, func() {
		ro.RegisterRoutes(router, mcpRouteNext, mcpRouteNext)
		RegisterMCPOAuthRoutes(router, router.Group("/api/v1"), h, nil)
	})
	for _, tc := range []struct{ path, issuer, scope string }{
		{"/.well-known/oauth-authorization-server", "https://platform.example", "xy2api.me.get_profile"},
		{"/.well-known/oauth-authorization-server/api/v1/mcp", "https://platform.example/api/v1/mcp", "mcp"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://platform.example"+tc.path, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var metadata map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metadata))
		require.Equal(t, tc.issuer, metadata["issuer"])
		require.Contains(t, metadata["scopes_supported"], tc.scope)
		require.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	}
	require.Len(t, readonly.ToolPolicies(), 29)
}

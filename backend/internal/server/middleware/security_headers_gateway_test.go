package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/util/responseheaders"
	"github.com/stretchr/testify/require"
)

const expectedGatewayCSP = "sandbox; default-src 'none'"

func TestSecurityHeadersGatewayRoutes(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, path := range []string{
			"/v1/messages", "/v1/responses", "/v1/chat/completions",
			"/v1beta/models?key=test-key", "/antigravity/v1beta/models?key=test-key",
			"/antigravity/v1/messages", "/responses", "/responses/response-id",
			"/images/generations", "/images/edits",
		} {
			t.Run(path+"/csp_enabled="+map[bool]string{true: "true", false: "false"}[enabled], func(t *testing.T) {
				router := gin.New()
				router.Use(SecurityHeaders(config.CSPConfig{Enabled: enabled}, nil))
				router.NoRoute(func(c *gin.Context) {
					require.Empty(t, GetNonceFromContext(c))
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid key"})
				})
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, http.StatusUnauthorized, w.Code)
				require.Equal(t, []string{expectedGatewayCSP}, w.Result().Header.Values("Content-Security-Policy"))
				require.JSONEq(t, `{"error":"invalid key"}`, w.Body.String())
			})
		}
	}
}

func TestSecurityHeadersGatewayCommitPaths(t *testing.T) {
	for _, mode := range []string{"html", "json", "write_string", "sse", "flush_first", "headers_only", "empty", "media"} {
		t.Run(mode, func(t *testing.T) {
			w := httptest.NewRecorder()
			router := gin.New()
			router.Use(SecurityHeaders(config.CSPConfig{Enabled: false}, nil))
			router.GET("/v1/test", func(c *gin.Context) {
				// Exercise upstream copying, an explicitly removed header, and
				// noncanonical map keys before any response is committed.
				filter := responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{
					Enabled: true, AdditionalAllowed: []string{"Content-Security-Policy"},
				})
				responseheaders.WriteFilteredHeaders(c.Writer.Header(), http.Header{
					"Content-Security-Policy": {"default-src * 'unsafe-inline'"},
					"Content-Type":            {"text/html"},
				}, filter)
				remove := responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{
					Enabled: true, ForceRemove: []string{"Content-Security-Policy"},
				})
				responseheaders.WriteFilteredHeaders(c.Writer.Header(), http.Header{
					"Content-Security-Policy": {"default-src *"},
				}, remove)
				c.Header("Content-Security-Policy", "")
				c.Writer.Header()["content-security-policy"] = []string{"default-src *"}
				switch mode {
				case "html":
					c.Data(http.StatusOK, "text/html", []byte("<script>localStorage.clear()</script>"))
				case "json":
					c.Header("Content-Type", "application/json")
					c.JSON(http.StatusOK, gin.H{"ok": true})
				case "write_string":
					_, _ = c.Writer.WriteString("data: first\n\n")
				case "sse":
					c.Header("Content-Type", "text/event-stream")
					_, _ = c.Writer.Write([]byte("data: first\n\n"))
					c.Writer.Flush()
				case "flush_first":
					c.Writer.Flush()
				case "headers_only":
					c.Status(http.StatusNoContent)
					c.Writer.WriteHeaderNow()
				case "empty":
					c.Status(http.StatusNoContent)
				case "media":
					c.Header("Content-Type", "audio/mpeg")
					c.Header("Content-Disposition", `attachment; filename="audio.mp3"`)
					c.Header("Content-Range", "bytes 0-3/4")
					c.Status(http.StatusPartialContent)
					_, _ = io.Copy(c.Writer, strings.NewReader("data"))
				}
				if mode != "empty" {
					// Inspect the committed snapshot while the handler is still running.
					require.Equal(t, []string{expectedGatewayCSP}, w.Result().Header.Values("Content-Security-Policy"))
				}
			})
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/test", nil))
			require.Equal(t, []string{expectedGatewayCSP}, w.Result().Header.Values("Content-Security-Policy"))
			switch mode {
			case "html":
				require.Equal(t, "<script>localStorage.clear()</script>", w.Body.String())
			case "json":
				require.JSONEq(t, `{"ok":true}`, w.Body.String())
			case "sse", "write_string":
				require.Equal(t, "data: first\n\n", w.Body.String())
			}
			if mode == "media" {
				require.Equal(t, http.StatusPartialContent, w.Code)
				require.Equal(t, "audio/mpeg", w.Result().Header.Get("Content-Type"))
				require.Equal(t, "bytes 0-3/4", w.Result().Header.Get("Content-Range"))
				require.Equal(t, `attachment; filename="audio.mp3"`, w.Result().Header.Get("Content-Disposition"))
				require.Equal(t, "data", w.Body.String())
			}
		})
	}
}

func TestSecurityHeadersPagesKeepPagePolicy(t *testing.T) {
	for _, path := range []string{"/", "/admin", "/login", "/assets/app.js", "/api/v1/settings"} {
		router := gin.New()
		router.Use(SecurityHeaders(config.CSPConfig{Enabled: true}, nil))
		router.NoRoute(func(c *gin.Context) { c.String(http.StatusOK, "page") })
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.NotContains(t, w.Result().Header.Get("Content-Security-Policy"), "sandbox", path)
		require.Contains(t, w.Result().Header.Get("Content-Security-Policy"), "'nonce-", path)
	}
}

func TestSecurityHeadersGatewayWebSocket(t *testing.T) {
	router := gin.New()
	router.Use(SecurityHeaders(config.CSPConfig{}, nil))
	router.GET("/v1/responses", func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src *")
		conn, err := websocket.Accept(c.Writer, c.Request, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		typ, msg, err := conn.Read(ctx)
		if err == nil {
			err = conn.Write(ctx, typ, msg)
		}
		if err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	require.Equal(t, expectedGatewayCSP, resp.Header.Get("Content-Security-Policy"))
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("hello")))
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "hello", string(msg))
}

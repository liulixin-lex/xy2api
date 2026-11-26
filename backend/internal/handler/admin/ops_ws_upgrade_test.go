package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// The browser offers ["sub2api-admin", "jwt.<token>"]. Whatever order the
// client uses, the handshake must answer "sub2api-admin" so the token is
// never echoed back.
func TestOpsWSUpgradeNeverEchoesTokenSubprotocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(conn.Subprotocol()))
	})
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	for name, offered := range map[string][]string{
		"admin_first": {"sub2api-admin", "jwt.fixture-token"},
		"token_first": {"jwt.fixture-token", "sub2api-admin"},
	} {
		t.Run(name, func(t *testing.T) {
			dialer := websocket.Dialer{Subprotocols: offered}
			conn, resp, err := dialer.Dial(wsURL, http.Header{"Origin": {server.URL}})
			require.NoError(t, err)
			defer func() { _ = conn.Close() }()
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
			require.Equal(t, "sub2api-admin", resp.Header.Get("Sec-WebSocket-Protocol"))
			_, message, err := conn.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, "sub2api-admin", string(message))
		})
	}

	t.Run("plain_request_is_refused", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/ws")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("missing_upgrade_header_asks_for_websocket", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/ws", nil)
		require.NoError(t, err)
		req.Header.Set("Connection", "Upgrade")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusUpgradeRequired, resp.StatusCode)
		require.Equal(t, "websocket", resp.Header.Get("Upgrade"))
	})
}

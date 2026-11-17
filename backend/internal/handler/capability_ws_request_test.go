//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCapabilityWebSocketScopeBeforeAdmission(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("Upgrade", "websocket")
	c.Request.Header.Set("Connection", "Upgrade")
	handler := &OpenAIGatewayHandler{}
	// Stop at authentication; the actual entrypoint must already carry the
	// tuple that all subsequent scheduler calls will receive.
	handler.ResponsesWebSocket(c)
	protocol, stream, ok := resilience.CapabilityRequestFrom(c.Request.Context())
	require.True(t, ok)
	require.Equal(t, "responses", protocol)
	require.True(t, stream)
}

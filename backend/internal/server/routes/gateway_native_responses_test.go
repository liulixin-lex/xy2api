package routes

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayRoutesNativeResponsesAliasesKeepWebSocketAndCatchAll(t *testing.T) {
	router := newGatewayRoutesTestRouterWithGroup(allowlistGroup(service.PlatformOpenAI, false))
	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, prefix := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			require.True(t, routes[method+" "+prefix], "%s %s must retain create/WebSocket admission", method, prefix)
			require.True(t, routes[method+" "+prefix+"/*subpath"], "%s %s must use the compatible native subpath dispatcher", method, prefix)
		}
	}
}

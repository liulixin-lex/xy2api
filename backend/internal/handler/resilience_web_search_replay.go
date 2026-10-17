package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// resilienceWebSearchHistoryRejected answers a request whose web_search
// history an API-key relay rejected even with the tool declared
// (service/resilience_web_search_replay.go). The request was not tried on
// another account; like any exhausted 5xx it counts toward the failed-turn
// cap (design §9.7), and the client keeps the upstream's reason instead of
// a generic text. It reports false for every other failure.
func resilienceWebSearchHistoryRejected(c *gin.Context, diag any, failoverErr *service.UpstreamFailoverError) (int, string, string, bool) {
	if failoverErr == nil || failoverErr.Reason != service.OpenAIWebSearchHistoryRejectedReason {
		return 0, "", "", false
	}
	status := failoverErr.ClientStatusCode
	if status <= 0 {
		status = http.StatusBadGateway
	}
	message := strings.TrimSpace(failoverErr.ClientMessage)
	service.SetOpsUpstreamError(c, failoverErr.StatusCode, message, "")
	return applyExhaustedRetryHint(c, diag, status), "upstream_error", message, true
}

package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// ResilienceAvailabilityFallback moves a request to its group's availability
// fallback group while the group has no available account (design §5). It
// runs right after API-key authentication, so the system prompt, composite
// routing and selection apply the fallback group. A request falls back at
// most once. The fallback group only routes: the key as authenticated is
// kept, and prices, limits and permissions, the model allow-list included,
// stay those of the group the request was made for (service.BillingAPIKey).
// Model listings route nothing and describe the user's own group, so they
// never move.
func ResilienceAvailabilityFallback() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || isModelListing(c) {
			c.Next()
			return
		}
		apiKey, ok := GetAPIKeyFromContext(c)
		if svc := service.Resilience(); ok && apiKey != nil && apiKey.GroupID != nil && svc != nil {
			st := resilience.StateFrom(c.Request.Context())
			if fallback := svc.AvailabilityFallbackGroup(c.Request.Context(), apiKey); fallback != nil && st.MarkFallback(*apiKey.GroupID) {
				service.RememberAvailabilityFallbackOrigin(c.Request.Context(), apiKey)
				moved := service.CloneAPIKeyWithGroup(apiKey, fallback)
				c.Set(string(ContextKeyAPIKey), moved)
				setGroupContext(c, fallback)
				st.SetGroup(fallback.ID)
			}
		}
		c.Next()
	}
}

// isModelListing reports a GET of a model list or of one model
// (/v1/models, /v1beta/models/:model, /backend-api/codex/models, ...).
func isModelListing(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet {
		return false
	}
	route := c.FullPath()
	return strings.HasSuffix(route, "/models") || strings.HasSuffix(route, "/models/:model")
}

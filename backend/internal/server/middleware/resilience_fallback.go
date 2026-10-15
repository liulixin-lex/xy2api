package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// ResilienceAvailabilityFallback moves a request to its group's availability
// fallback group while the group has no available account (design §5). It
// runs right after API-key authentication, so the model allow-list, system
// prompt, composite routing, selection, limits and billing all apply the
// fallback group. A request falls back at most once.
func ResilienceAvailabilityFallback() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}
		apiKey, ok := GetAPIKeyFromContext(c)
		if svc := service.Resilience(); ok && apiKey != nil && apiKey.GroupID != nil && svc != nil {
			st := resilience.StateFrom(c.Request.Context())
			if fallback := svc.AvailabilityFallbackGroup(c.Request.Context(), apiKey); fallback != nil && st.MarkFallback(*apiKey.GroupID) {
				moved := service.CloneAPIKeyWithGroup(apiKey, fallback)
				c.Set(string(ContextKeyAPIKey), moved)
				setGroupContext(c, fallback)
				st.SetGroup(fallback.ID)
			}
		}
		c.Next()
	}
}

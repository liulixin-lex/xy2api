package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
)

// MonitorProbe verifies the channel monitor's probe header, marks the request
// context, and always removes the header so it never reaches an upstream.
func MonitorProbe() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}
		value := c.Request.Header.Get(monitorprobe.HeaderName)
		if value != "" {
			c.Request.Header.Del(monitorprobe.HeaderName)
			if monitorID, ok := monitorprobe.Verify(value, time.Now()); ok {
				c.Request = c.Request.WithContext(monitorprobe.WithProbe(c.Request.Context(), monitorID))
			}
		}
		c.Next()
	}
}

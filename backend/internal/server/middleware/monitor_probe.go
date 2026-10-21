package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
)

// MonitorProbe verifies the channel monitor's probe header against the API key
// the request carries, marks the request context, and always removes the
// header so it never reaches an upstream.
func MonitorProbe() gin.HandlerFunc {
	canonical := http.CanonicalHeaderKey(monitorprobe.HeaderName)
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}
		values, present := c.Request.Header[canonical]
		if present {
			c.Request.Header.Del(canonical)
			if monitorID, ok := verifyMonitorProbe(values, c.Request); ok {
				c.Request = c.Request.WithContext(monitorprobe.WithProbe(c.Request.Context(), monitorID))
			}
		}
		c.Next()
	}
}

// verifyMonitorProbe accepts exactly one header value signed for one of the
// API keys the request itself presents.
func verifyMonitorProbe(values []string, r *http.Request) (int64, bool) {
	if len(values) != 1 {
		return 0, false
	}
	now := time.Now()
	for _, apiKey := range monitorProbeRequestKeys(r) {
		if monitorID, ok := monitorprobe.Verify(values[0], apiKey, now); ok {
			return monitorID, true
		}
	}
	return 0, false
}

// monitorProbeRequestKeys lists the API keys a gateway request can carry.
func monitorProbeRequestKeys(r *http.Request) []string {
	keys := make([]string, 0, 3)
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		keys = append(keys, strings.TrimSpace(auth[7:]))
	}
	for _, name := range []string{"x-api-key", "x-goog-api-key"} {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			keys = append(keys, v)
		}
	}
	return keys
}

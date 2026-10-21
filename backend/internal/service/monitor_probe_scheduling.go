package service

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
)

// A channel monitor probe (monitorprobe.IsProbe) measures the model on a free
// account, not the queue of a busy one. Its question differs every time, so
// it has no conversation to keep either. The schedulers treat a verified
// probe, and only a probe, like this:
//
//   - it has no sticky session: no binding is read or written, so a probe is
//     never pinned to a bound account and never waits for one.
//
// Every other request is scheduled exactly as before.

// monitorProbeSessionHash drops the sticky session of a probe.
func monitorProbeSessionHash(ctx context.Context, sessionHash string) string {
	if monitorprobe.IsProbe(ctx) {
		return ""
	}
	return sessionHash
}

// monitorProbeRequest reports whether the request of c is a probe.
func monitorProbeRequest(c *gin.Context) bool {
	return c != nil && c.Request != nil && monitorprobe.IsProbe(c.Request.Context())
}

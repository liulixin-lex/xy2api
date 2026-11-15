package service

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// ClaimProtocolTerminal reserves one terminal write; write failure retains it.
// Callers stop heartbeat writers first and use this on the request goroutine.
func ClaimProtocolTerminal(c *gin.Context) bool {
	if c == nil || ProtocolTerminalClaimed(c) {
		return false
	}
	c.Set(resilience.GinKeyProtocolTerminal, true)
	return true
}
func ProtocolTerminalClaimed(c *gin.Context) bool {
	return c != nil && c.GetBool(resilience.GinKeyProtocolTerminal)
}

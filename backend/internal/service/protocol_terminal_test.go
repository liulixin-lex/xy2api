//go:build unit

package service

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type foundationFailedWriter struct{ gin.ResponseWriter }

func (foundationFailedWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestFoundationProtocolTerminal(t *testing.T) {
	if ClaimProtocolTerminal(nil) || ProtocolTerminalClaimed(nil) {
		t.Fatal("nil claimed")
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if !ClaimProtocolTerminal(c) || !ProtocolTerminalClaimed(c) || ClaimProtocolTerminal(c) {
		t.Fatal("claim contract")
	}
	if !c.GetBool(resilience.GinKeyProtocolTerminal) {
		t.Fatal("old key not set")
	}
	c.Writer = foundationFailedWriter{c.Writer}
	_, err := c.Writer.Write([]byte("failed"))
	if err == nil || ClaimProtocolTerminal(c) {
		t.Fatal("write failure released claim")
	}
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Set(resilience.GinKeyProtocolTerminal, true)
	if ClaimProtocolTerminal(c) {
		t.Fatal("legacy claim not honored")
	}
}

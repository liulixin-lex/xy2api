//go:build unit

package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func TestFoundationObservationDoesNotClaimTerminal(t *testing.T) {
	for _, event := range []string{"completed", "failed", "incomplete"} {
		t.Run(event, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/", nil)
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
			InstallDeliveryTracking(c, time.Now())
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.Write([]byte("data: {\"type\":\"response." + event + "\",\"sequence_number\":4,\"response\":{\"id\":\"resp_observed\",\"status\":\"" + event + "\"}}\n\n"))
			if ProtocolTerminalClaimed(c) || !ClaimProtocolTerminal(c) || ClaimProtocolTerminal(c) {
				t.Fatal("observation changed the existing claim contract")
			}
			state := &deliveryWriterFrom(c).terminal
			if state.id != "resp_observed" || state.nextSequence != 5 {
				t.Fatal("envelope observation was lost")
			}
		})
	}
}

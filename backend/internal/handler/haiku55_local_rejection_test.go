//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHaiku55LocalRejectionClientOwnership(t *testing.T) {
	for _, body := range []string{
		`{"model":"claude-haiku-5-5","messages":[{"role":"assistant","content":"prefix"}]}`,
		`{"model":"claude-haiku-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"computer_20250124","name":"computer"}]}`,
		`{"model":"claude-haiku-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"computer_20251124","name":"computer"}]}`,
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		svc := &service.GatewayService{}
		account := &service.Account{ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey}
		_, err := svc.Forward(context.Background(), c, account, &service.ParsedRequest{Model: "claude-haiku-5-5", Body: service.NewRequestBodyRef([]byte(body))})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		parsed := parseOpsErrorResponse(rec.Body.Bytes())
		require.Equal(t, "invalid_request_error", parsed.ErrorType)
		phase, limited, owner, source := classifyOpsErrorLog(c, parsed.ErrorType, parsed.Message, parsed.Code, rec.Code)
		require.Equal(t, "request", phase)
		require.False(t, limited)
		require.Equal(t, "client", owner)
		require.Equal(t, "client_request", source)
		t.Logf("status=%d phase=%s owner=%s source=%s body=%s", rec.Code, phase, owner, source, rec.Body.String())
	}
}

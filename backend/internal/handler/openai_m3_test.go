//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIM3TerminalUsesRetryableResponse(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	for _, committed := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		ctx := resilience.WithRequestState(httptest.NewRequest(http.MethodPost, "/v1/responses", nil).Context(), resilience.NewRequestState())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		if committed {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.WriteHeader(http.StatusOK)
			_, err := c.Writer.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_m3\",\"status\":\"in_progress\"}}\n\n")
			require.NoError(t, err)
		}
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadRequest, Reason: service.OpenAIStateRepairReason,
			ClientMessage: "encrypted state rejected", M3Cause: "no_growth",
		}, false) // The terminal writer must also detect already committed headers itself.
		if !committed {
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"code":"server_error"`)
			continue
		}
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), "event: response.failed")
		require.Contains(t, recorder.Body.String(), `"code":"server_error"`)
	}
}

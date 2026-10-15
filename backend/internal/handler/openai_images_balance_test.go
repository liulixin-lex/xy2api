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
	"github.com/tidwall/gjson"
)

// The 0.3.0 behaviour, kept with xy2api resilience error semantics off.
func TestOpenAIImagesBalanceExhaustionPreservesMachineReadableCause(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Errors.Enabled = false })
	gin.SetMode(gin.TestMode)
	for _, streamStarted := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:       http.StatusBadGateway,
			Reason:           service.OpenAIImagesInsufficientBalanceReason,
			ClientStatusCode: http.StatusPaymentRequired,
			ClientMessage:    service.OpenAIImagesInsufficientBalanceMessage,
			ResponseHeaders:  http.Header{"Retry-After": []string{"23"}, "X-Request-Id": []string{"req-images-balance"}},
		}, streamStarted)
		require.Equal(t, "23", recorder.Header().Get("Retry-After"))

		if streamStarted {
			require.Contains(t, recorder.Body.String(), `"code":"insufficient_balance"`)
		} else {
			require.Equal(t, http.StatusPaymentRequired, recorder.Code)
			require.Equal(t, service.OpenAIImagesInsufficientBalanceCode, gjson.GetBytes(recorder.Body.Bytes(), "error.code").String())
		}
	}
}

// xy2api resilience (design §9.4): an upstream image account out of credit is
// not the user's problem; the user gets the temporary 503 with Retry-After.
func TestOpenAIImagesUpstreamBalanceHiddenFromUsers(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)
	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:       http.StatusBadGateway,
		Reason:           service.OpenAIImagesInsufficientBalanceReason,
		ClientStatusCode: http.StatusPaymentRequired,
		ClientMessage:    service.OpenAIImagesInsufficientBalanceMessage,
	}, false)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "insufficient")
	require.Contains(t, gjson.GetBytes(recorder.Body.Bytes(), "error.message").String(), "Service temporarily unavailable")
	require.NotEmpty(t, recorder.Header().Get("Retry-After"))
}

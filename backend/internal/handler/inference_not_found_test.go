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
	"github.com/tidwall/gjson"
)

func TestInference404AllCandidatesExhausted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name  string
		write func(*gin.Context, *service.UpstreamFailoverError, bool)
	}{
		{"messages", func(c *gin.Context, e *service.UpstreamFailoverError, stream bool) {
			(&GatewayHandler{}).handleFailoverExhausted(c, e, service.PlatformAnthropic, stream)
		}},
		{"gemini_messages", func(c *gin.Context, e *service.UpstreamFailoverError, stream bool) {
			(&GatewayHandler{}).handleFailoverExhausted(c, e, service.PlatformGemini, stream)
		}},
		{"chat", (&GatewayHandler{}).handleCCFailoverExhausted},
		{"responses", (&GatewayHandler{}).handleResponsesFailoverExhausted},
		{"openai", (&OpenAIGatewayHandler{}).handleFailoverExhausted},
		{"openai_messages", (&OpenAIGatewayHandler{}).handleAnthropicFailoverExhausted},
		{"gemini", func(c *gin.Context, e *service.UpstreamFailoverError, _ bool) {
			(&GatewayHandler{}).handleGeminiFailoverExhausted(c, e)
		}},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Set(opsModelKey, "requested-model")
			state := NewFailoverState(3, false)
			for _, id := range []int64{1, 2} {
				e := &service.UpstreamFailoverError{StatusCode: http.StatusNotFound}
				require.Equal(t, FailoverContinue, state.HandleFailoverError(context.Background(), &mockTempUnscheduler{}, id, service.PlatformAnthropic, 0, e))
			}
			require.Len(t, state.FailedAccountIDs, 2)
			require.Equal(t, FailoverExhausted, state.HandleSelectionExhausted(context.Background()))
			endpoint.write(c, state.LastFailoverErr, false)
			require.Equal(t, http.StatusNotFound, recorder.Code)
			message := gjson.Get(recorder.Body.String(), "error.message").String()
			require.Equal(t, `Model "requested-model" is not supported by any configured account in this group`, message)
			require.NotContains(t, recorder.Body.String(), "retry later")
			require.Empty(t, recorder.Header().Get("Retry-After"))
			if endpoint.name == "gemini" {
				require.Equal(t, "NOT_FOUND", gjson.Get(recorder.Body.String(), "error.status").String())
			} else {
				require.Contains(t, recorder.Body.String(), "model_not_found")
			}
		})
	}
}

func TestInference404ExhaustedAfterHeartbeat(t *testing.T) {
	for _, endpoint := range []struct {
		name  string
		write func(*gin.Context, *service.UpstreamFailoverError, bool)
	}{
		{"responses", (&GatewayHandler{}).handleResponsesFailoverExhausted},
		{"chat", (&GatewayHandler{}).handleCCFailoverExhausted},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint.name, nil)
			c.Set(opsModelKey, "requested-model")
			written, err := c.Writer.WriteString(": ping\n\n")
			require.NoError(t, err)
			recordGatewayStreamHeartbeat(c, written)
			c.Writer.Flush()
			endpoint.write(c, &service.UpstreamFailoverError{StatusCode: http.StatusNotFound}, true)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "model_not_found")
			require.Contains(t, recorder.Body.String(), "is not supported by any configured account in this group")
			require.NotContains(t, recorder.Body.String(), "retry later")
			require.Len(t, service.GetOpsStreamErrors(c), 1)
			if endpoint.name == "responses" {
				require.Contains(t, recorder.Body.String(), "response.failed")
			}
		})
	}
}

func TestInference404CountTokensExhaustionKeepsNonInferenceResponse(t *testing.T) {
	c, rec, _, cancel := trackedQueueContext(t)
	defer cancel()
	c.Request.URL.Path = "/v1beta/models/requested-model:countTokens"
	c.Set(opsModelKey, "requested-model")
	failure := &service.UpstreamFailoverError{StatusCode: http.StatusNotFound, NonInference: true,
		ResponseBody: []byte(`{"error":{"code":"model_not_found","message":"Model not found"}}`)}
	(&GatewayHandler{}).handleGeminiFailoverExhausted(c, failure)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.NotContains(t, rec.Body.String(), "not supported by any configured account")
}

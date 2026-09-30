//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Exercise the real service rejection and the handler's error/fallback decision
// together: a service-only assertion would miss the appended SSE regression.
func TestOpenAIForwardEarlyJSONErrorRemainsSingleJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, effort := range []string{"none", "minimal"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", accountType, stream, effort), func(t *testing.T) {
					body, err := json.Marshal(map[string]any{
						"model": "gpt-6.1-sol", "input": "local protocol regression", "stream": stream,
						"reasoning": map[string]any{"effort": effort},
					})
					require.NoError(t, err)
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, bytes.NewReader(body))
					before := c.Writer.Size()
					account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: accountType}
					// Invalid input must return before any upstream dependency is needed.
					svc := &service.OpenAIGatewayService{}
					_, forwardErr := svc.Forward(context.Background(), c, account, body)
					require.Error(t, forwardErr)
					h := &OpenAIGatewayHandler{}
					if !openAIForwardErrorAlreadyCommunicated(c, before, forwardErr) {
						h.ensureForwardErrorResponse(c, false)
					}
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					var response struct {
						Error struct{ Type, Message string } `json:"error"`
					}
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), "body=%s", recorder.Body.String())
					require.Equal(t, "invalid_request_error", response.Error.Type)
					require.Contains(t, response.Error.Message, effort)
					require.NotContains(t, recorder.Body.String(), "event:")
				})
			}
		}
	}
}

func TestOpenAIForwardCompletedJSONGuardPreservesFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("no response written", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
		require.False(t, openAIForwardErrorAlreadyCommunicated(c, c.Writer.Size(), errors.New("local error")))
		require.True(t, (&OpenAIGatewayHandler{}).ensureForwardErrorResponse(c, false))
		require.Equal(t, http.StatusBadGateway, w.Code)
		require.True(t, json.Valid(w.Body.Bytes()))
	})
	t.Run("started SSE heartbeat still terminates", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
		before := c.Writer.Size()
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString(": ping\n\n")
		require.False(t, openAIForwardErrorAlreadyCommunicated(c, before, errors.New("stream read error")))
		require.True(t, (&OpenAIGatewayHandler{}).ensureForwardErrorResponse(c, true))
		require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed\n"))
	})
	t.Run("terminal SSE is not duplicated", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
		before := c.Writer.Size()
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n")
		require.True(t, openAIForwardErrorAlreadyCommunicated(c, before, errors.New("upstream response failed: denied")))
		require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed\n"))
	})
	t.Run("JSON error headers alone still need fallback", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
		before := c.Writer.Size()
		c.Header("Content-Type", "application/json")
		c.Status(http.StatusBadRequest)
		c.Writer.WriteHeaderNow()
		require.Zero(t, c.Writer.Size())
		require.False(t, openAIForwardErrorAlreadyCommunicated(c, before, errors.New("local error")))
	})
	t.Run("successful partial JSON still needs fallback", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
		before := c.Writer.Size()
		c.Data(http.StatusOK, "application/json", []byte("{\"id\":"))
		require.False(t, openAIForwardErrorAlreadyCommunicated(c, before, errors.New("stream read error")))
		require.True(t, (&OpenAIGatewayHandler{}).ensureForwardErrorResponse(c, false))
		require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed\n"))
	})
	for _, tc := range []struct {
		name, contentType string
		status            int
		communicated      bool
	}{
		{"json charset", "application/json; charset=utf-8", 400, true},
		{"json uppercase", "Application/JSON; Charset=UTF-8", 400, true},
		{"jsonp is not json", "application/jsonp", 400, false},
		{"problem json is not application json", "application/problem+json", 400, false},
		{"malformed media type", "application/json; charset", 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
			before := c.Writer.Size()
			c.Data(tc.status, tc.contentType, []byte("{\"error\":{\"type\":\"invalid_request_error\"}}"))
			require.Equal(t, tc.communicated, openAIForwardErrorAlreadyCommunicated(c, before, errors.New("local error")))
		})
	}
}

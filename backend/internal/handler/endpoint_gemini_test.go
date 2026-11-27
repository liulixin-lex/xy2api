package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGeminiInboundEndpointRoutes(t *testing.T) {
	for _, prefix := range []string{"", "/antigravity"} {
		for _, tc := range []struct{ method, path, want string }{
			{http.MethodGet, "/v1beta/models?pageSize=2", EndpointGeminiModels},
			{http.MethodGet, "/v1beta/models/gemini-fixture", EndpointGeminiModel},
			{http.MethodPost, "/v1beta/models/gemini-fixture:generateContent", EndpointGeminiGenerateContent},
			{http.MethodPost, "/v1beta/models/gemini-fixture:streamGenerateContent?alt=sse", EndpointGeminiStreamGenerateContent},
			{http.MethodPost, "/v1beta/models/gemini-fixture:countTokens", EndpointGeminiCountTokens},
			{http.MethodPost, "/v1beta/models/gemini-fixture/generateContent", EndpointGeminiGenerateContent},
			{http.MethodPost, "/v1beta/models/gemini%2Dfixture%3AgenerateContent", EndpointGeminiGenerateContent},
			{http.MethodPost, "/v1beta/models/gemini-fixture:unknown", EndpointGeminiModels},
			{http.MethodPost, "/v1beta/models/gemini-fixture:generateContentExtra", EndpointGeminiModels},
			{http.MethodPost, "/v1beta/models/gemini-fixture:generateContent%3Fextra", EndpointGeminiModels},
			{http.MethodPost, "/v1beta/models/gemini%252Dfixture:generateContent", EndpointGeminiModels},
			{http.MethodPost, "/v1beta/models/..:generateContent", EndpointGeminiModels},
			{http.MethodPost, "/v1beta/models/gemini%2Ffixture:generateContent", EndpointGeminiModels},
			{http.MethodPost, "/v1beta/models/:generateContent", EndpointGeminiModels},
			{http.MethodPost, "/v1beta/models/gemini-fixture", EndpointGeminiModels},
			{http.MethodGet, "/v1beta/models/gemini-fixture:generateContent", EndpointGeminiModels},
		} {
			t.Run(prefix+tc.method+tc.path, func(t *testing.T) {
				request := httptest.NewRequest(tc.method, prefix+tc.path, nil)
				urlBefore := request.URL.String()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = request
				require.Equal(t, tc.want, GetInboundEndpoint(c), "fallback without middleware")
				r := gin.New()
				r.Use(InboundEndpointMiddleware())
				h := func(c *gin.Context) {
					require.Equal(t, tc.want, GetInboundEndpoint(c))
					require.Equal(t, urlBefore, c.Request.URL.String(), "classification cannot rewrite upstream input")
					require.Equal(t, EndpointGeminiModels, GetUpstreamEndpoint(c, service.PlatformAntigravity))
					require.Equal(t, EndpointGeminiModels, GetUpstreamEndpoint(c, service.PlatformGemini))
					body := []byte("synthetic moderation input")
					input := buildContentModerationInput(c, nil, middleware.AuthSubject{}, "gemini", "gemini-fixture", body)
					ping := requestQueuePingFormat(c, SSEPingFormatComment)
					c.Set(ctxKeyInboundEndpoint, EndpointGeminiModels)
					require.Equal(t, buildContentModerationInput(c, nil, middleware.AuthSubject{}, "gemini", "gemini-fixture", body), input)
					require.Equal(t, requestQueuePingFormat(c, SSEPingFormatComment), ping)
					c.Status(http.StatusNoContent)
				}
				r.GET(prefix+"/v1beta/models", h)
				r.GET(prefix+"/v1beta/models/:model", h)
				r.POST(prefix+"/v1beta/models/*modelAction", h)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, request)
				require.Equal(t, http.StatusNoContent, w.Code)
			})
		}
	}
	for _, path := range []string{"/other/v1beta/models/gemini:generateContent", "/v1beta/models-extra/gemini:generateContent"} {
		require.Equal(t, path, NormalizeInboundEndpoint(path), "only registered route families classify Gemini actions")
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodDelete, "/v1beta/models/gemini:generateContent", nil)
	require.Equal(t, EndpointGeminiModels, GetInboundEndpoint(c))
}

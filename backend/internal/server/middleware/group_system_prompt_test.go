package middleware

import (
	"io"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGroupSystemPromptMiddlewareCapturesBeforeMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, prefix := range []string{"/v1", "", "/openai/v1", "/antigravity/v1"} {
		router := gin.New()
		key := &service.APIKey{Group: &service.Group{SystemPromptConfig: service.GroupSystemPromptConfig{ModelPrompts: map[string]string{"alias": "admin"}}}}
		router.Use(func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), key) }, GroupSystemPrompt())
		router.POST(prefix+"/responses", func(c *gin.Context) {
			body, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Equal(t, `{"model":"alias","instructions":"client"}`, string(body))
			mapped := []byte(`{"model":"upstream","instructions":"client"}`)
			got, err := service.ApplyGroupSystemPrompt(c.Request.Context(), mapped, service.GroupPromptResponses)
			require.NoError(t, err)
			require.Equal(t, "admin\n\nclient", gjson.GetBytes(got, "instructions").String())
			c.Status(200)
		})
		resp := doJSON(t, router, http.MethodPost, prefix+"/responses", `{"model":"alias","instructions":"client"}`)
		require.Equal(t, 200, resp.Code)
	}
}

func TestGroupSystemPromptMiddlewareRejectsMalformedPolicyTarget(t *testing.T) {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{SystemPromptConfig: service.GroupSystemPromptConfig{Prompt: "admin"}}})
	}, GroupSystemPrompt())
	router.POST("/v1/responses", func(c *gin.Context) { t.Fatal("invalid instructions reached forwarder") })
	resp := doJSON(t, router, http.MethodPost, "/v1/responses", `{"model":"alias","instructions":[]}`)
	require.Equal(t, 400, resp.Code)
	resp = doJSON(t, router, http.MethodPost, "/v1/responses", `{"model":"alias","model":"different"}`)
	require.Equal(t, 400, resp.Code)
}

func TestGroupSystemPromptMiddlewareResponsesSubpaths(t *testing.T) {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{SystemPromptConfig: service.GroupSystemPromptConfig{Prompt: "admin"}}})
	}, GroupSystemPrompt())
	router.POST("/v1/responses/*subpath", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		wire, err := service.ApplyGroupSystemPrompt(c.Request.Context(), body, service.GroupPromptResponses)
		require.NoError(t, err)
		require.Equal(t, "admin\n\nclient", gjson.GetBytes(wire, "instructions").String())
		c.Status(200)
	})
	for _, suffix := range []string{"compact", "input_tokens", "custom"} {
		resp := doJSON(t, router, http.MethodPost, "/v1/responses/"+suffix, `{"model":"alias","instructions":"client"}`)
		require.Equal(t, 200, resp.Code)
	}
}

package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/pkg/requestmodel"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// GroupSystemPrompt captures the public model before Composite or account mapping.
// It validates only; client detection and auditing still see the original body.
func GroupSystemPrompt() gin.HandlerFunc {
	return func(c *gin.Context) {
		key, ok := GetAPIKeyFromContext(c)
		if !ok || key == nil || key.Group == nil || !service.HasGroupSystemPrompts(key.Group.SystemPromptConfig) {
			c.Next()
			return
		}
		if isResponsesWebSocketRoute(c) {
			c.Request = c.Request.WithContext(service.WithGroupSystemPrompt(c.Request.Context(), key.Group.SystemPromptConfig, ""))
			c.Next()
			return
		}
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		var protocol service.GroupPromptProtocol
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			protocol = service.GroupPromptChat
		case strings.HasSuffix(path, "/responses"), strings.Contains(c.FullPath(), "/responses/"):
			protocol = service.GroupPromptResponses
		case strings.HasSuffix(path, "/messages"), strings.HasSuffix(path, "/messages/count_tokens"):
			protocol = service.GroupPromptAnthropic
		case strings.HasSuffix(path, ":generateContent"), strings.HasSuffix(path, ":streamGenerateContent"), strings.HasSuffix(path, ":countTokens"):
			protocol = service.GroupPromptGemini
		default:
			c.Next()
			return
		}
		body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status := http.StatusBadRequest
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				status = http.StatusRequestEntityTooLarge
			}
			groupModelAllowlistErrorWriter(c)(c, status, "Failed to read request body")
			c.Abort()
			return
		}
		requestmodel.ResetRequestBody(c.Request, body)
		model := groupModelAllowlistModelFromParams(c)
		if model == "" {
			model = requestmodel.FromBodyForRoute(c.FullPath(), c.GetHeader("Content-Type"), body)
			for _, candidate := range requestmodel.FromBodyCandidates(c.FullPath(), c.GetHeader("Content-Type"), body) {
				if candidate != model {
					groupModelAllowlistErrorWriter(c)(c, http.StatusBadRequest, "Conflicting model fields")
					c.Abort()
					return
				}
			}
		}
		if !service.GroupSystemPromptSupportsRequest(model, body) {
			c.Next()
			return
		}
		ctx := service.WithGroupSystemPrompt(c.Request.Context(), key.Group.SystemPromptConfig, model)
		if _, err := service.ApplyGroupSystemPrompt(ctx, body, protocol); err != nil {
			groupModelAllowlistErrorWriter(c)(c, http.StatusBadRequest, err.Error())
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

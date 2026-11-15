package middleware

import (
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/pkg/requestmodel"
)

// InferenceBodyValidation runs after authentication and before routing rewrites.
// Only the scan view is normalized: downstream readers retain the original bytes.
func InferenceBodyValidation(maxBodyBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Method != http.MethodPost || !inferenceJSONBodyPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		mediaType, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if strings.EqualFold(mediaType, "multipart/form-data") && inferenceMultipartImagePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			inferenceBodyReadError(c, err)
			return
		}
		if inferenceResponsesBodyPath(c.Request.URL.Path) {
			if err := httputil.ValidateJSONUTF8(body); err != nil {
				inferenceBodyValidationError(c, "invalid_utf8", "body", err.Error())
				return
			}
		}
		requestmodel.ResetRequestBody(c.Request, body)
		view, err := httputil.NormalizeLenientJSONRequestBody(body, maxBodyBytes)
		if err != nil {
			inferenceBodyReadError(c, err)
			return
		}
		if err := requestmodel.ValidateUniqueTopLevelModel(view); err != nil {
			inferenceBodyValidationError(c, "duplicate_model", "model", "Duplicate top-level field: model")
			return
		}
		c.Next()
	}
}

func inferenceResponsesBodyPath(path string) bool {
	for _, prefix := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func inferenceMultipartImagePath(path string) bool {
	switch path {
	case "/v1/images/generations", "/v1/images/edits", "/images/generations", "/images/edits":
		return true
	}
	return false
}

func inferenceJSONBodyPath(path string) bool {
	switch path {
	case "/v1/messages", "/v1/messages/count_tokens", "/messages/count_tokens", "/antigravity/v1/messages", "/antigravity/v1/messages/count_tokens",
		"/v1/chat/completions", "/chat/completions", "/v1/embeddings", "/embeddings",
		"/v1/images/generations", "/v1/images/edits", "/images/generations", "/images/edits",
		"/v1/responses", "/responses", "/backend-api/codex/responses",
		"/v1/systemone", "/v1/alpha/search", "/alpha/search", "/backend-api/codex/alpha/search":
		return true
	}
	return strings.HasPrefix(path, "/v1/responses/") || strings.HasPrefix(path, "/responses/") ||
		strings.HasPrefix(path, "/backend-api/codex/responses/") || strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/antigravity/v1beta/models/")
}

func inferenceBodyReadError(c *gin.Context, err error) {
	status, message := http.StatusBadRequest, "Failed to read request body"
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		status = http.StatusRequestEntityTooLarge
		message = "Request body is too large"
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
}

func inferenceBodyValidationError(c *gin.Context, code, param, message string) {
	path := c.Request.URL.Path
	switch {
	case strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta"):
		GoogleErrorWriter(c, http.StatusBadRequest, message)
	case strings.Contains(path, "/messages"):
		AnthropicErrorWriter(c, http.StatusBadRequest, message)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": code, "param": param, "message": message}})
	}
	c.Abort()
}

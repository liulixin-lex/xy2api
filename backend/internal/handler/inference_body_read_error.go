package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// WriteInferenceBodyReadError shares the upload classification used by handlers
// at the earlier middleware read boundary.
func WriteInferenceBodyReadError(c *gin.Context, err error) {
	logRequestBodyReadFailure(requestLogger(c, "handler.gateway.ingress"), c.Request, err)
	status, kind, code, message := requestBodyReadFailureResponse(c.Request, err)
	if status == statusClientClosedRequest {
		if !c.Writer.Written() {
			c.Status(status)
		}
		c.Abort()
		return
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta") {
		middleware.GoogleErrorWriter(c, status, message)
	} else {
		protocol := service.PreOutputProtocolResponses
		if strings.Contains(path, "/messages") {
			protocol = service.PreOutputProtocolAnthropic
		}
		service.WriteTargetProtocolError(c, protocol, status, kind, code, "", message)
	}
	c.Abort()
}

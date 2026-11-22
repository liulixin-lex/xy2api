package service

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"go.uber.org/zap"
)

// Record only a claimed conversion terminal. Repeated cleanup writers must not
// duplicate it, and neither the generated values nor the error text is logged.
func writeUpstreamConversionError(c *gin.Context, resp *http.Response, source, protocol, errType, param, message string, err error) {
	if !WriteTargetProtocolError(c, protocol, http.StatusBadGateway, errType, OpenAIUpstreamProtocolErrorCode, param, message) {
		return
	}
	conversion := &apicompat.UpstreamConversionError{SourceProtocol: source}
	switch message {
	case "Upstream stream ended without a complete response", "Upstream stream ended without a terminal event":
		conversion.Field, conversion.Reason = "stream", "missing authenticated terminal"
	}
	var upstream *apicompat.UpstreamConversionError
	if errors.As(err, &upstream) && upstream != nil {
		*conversion = *upstream
		if conversion.SourceProtocol == "" {
			conversion.SourceProtocol = source
		}
	}
	diagnostic := conversion.Diagnostic()
	status := 0
	if resp != nil {
		status = resp.StatusCode
	} else if c.Request != nil {
		status = resilience.StateFrom(c.Request.Context()).UpstreamHTTPStatus(0)
	}
	fields := []zap.Field{
		zap.String("source_protocol", diagnostic.SourceProtocol),
		zap.String("conversion_field", diagnostic.Field),
		zap.String("conversion_reason", diagnostic.Reason),
		zap.Int("upstream_status", status),
		zap.Int("client_status", c.Writer.Status()),
		zap.Int("client_error_status", http.StatusBadGateway),
	}
	if diagnostic.IdentityConflict != "" {
		fields = append(fields, zap.String("identity_conflict", diagnostic.IdentityConflict))
	}
	logger.L().Warn("upstream_conversion_failed", fields...)
}

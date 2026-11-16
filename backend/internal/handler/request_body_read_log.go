package handler

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

// logRequestBodyReadFailure records a bounded, payload-free reason for a body
// read failure. Operators get enough information to distinguish upload timeouts,
// compression failures and a disconnected/truncated upload without logging request content.
func logRequestBodyReadFailure(reqLog *zap.Logger, req *http.Request, err error) {
	if reqLog == nil || err == nil {
		return
	}

	contentLength := int64(-1)
	contentEncoding := "identity"
	if req != nil {
		contentLength = req.ContentLength
		contentEncoding = requestContentEncodingCategory(req.Header.Get("Content-Encoding"))
	}

	reqLog.Warn("read request body failed",
		zap.String("error_kind", requestBodyReadErrorKind(err)),
		zap.String("content_encoding", contentEncoding),
		zap.Int64("content_length", contentLength),
	)
}

func requestContentEncodingCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "identity":
		return "identity"
	case "gzip", "x-gzip":
		return "gzip"
	case "zstd":
		return "zstd"
	case "deflate":
		return "deflate"
	default:
		return "other"
	}
}

func requestBodyReadErrorKind(err error) string {
	if err == nil {
		return "none"
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return "max_bytes"
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "decode content-encoding") {
		if strings.Contains(lower, "unsupported content-encoding") {
			return "unsupported_content_encoding"
		}
		return "decode_content_encoding"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return "client_disconnect"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "truncated_body"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "upload_timeout"
		}
		return "transport"
	}
	return "io_read"
}

// requestBodyReadFailureResponse classifies errors before any upstream send.
// A 499 is for local recording only: the caller must not write a new response
// to a disconnected client.
func requestBodyReadFailureResponse(req *http.Request, err error) (status int, errType, code, message string) {
	errType = "invalid_request_error"
	kind := requestBodyReadErrorKind(err)
	switch kind {
	case "max_bytes":
		var maxErr *http.MaxBytesError
		errors.As(err, &maxErr)
		return http.StatusRequestEntityTooLarge, errType, "", buildBodyTooLargeMessage(maxErr.Limit)
	case "decode_content_encoding", "unsupported_content_encoding":
		return http.StatusBadRequest, errType, "", "Failed to read request body"
	case "client_disconnect":
		return statusClientClosedRequest, "", "", ""
	}
	if req != nil {
		switch req.Context().Err() {
		case context.Canceled:
			return statusClientClosedRequest, "", "", ""
		case context.DeadlineExceeded:
			kind = "upload_timeout"
		}
	}
	if kind == "upload_timeout" {
		return http.StatusRequestTimeout, errType, "request_upload_timeout", "Timed out reading request body"
	}
	return http.StatusBadRequest, errType, "", "Failed to read request body"
}

func writeRequestBodyReadFailure(c *gin.Context, reqLog *zap.Logger, err error, protocol string) {
	logRequestBodyReadFailure(reqLog, c.Request, err)
	status, errType, code, message := requestBodyReadFailureResponse(c.Request, err)
	if errors.Is(c.Request.Context().Err(), context.Canceled) {
		status = statusClientClosedRequest
	}
	if status == statusClientClosedRequest {
		if !service.StopOpenAICompactSSEKeepaliveCommitted(c) && !c.Writer.Written() {
			c.Status(statusClientClosedRequest)
		}
		return
	}
	service.WriteTargetProtocolError(c, protocol, status, errType, code, "", message)
}

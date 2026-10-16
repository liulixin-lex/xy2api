package handler

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"

	"github.com/gin-gonic/gin"
	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
)

// A client whose upload breaks off has gone away: the request is a client
// cancel (499), not a malformed request (400). In production 12 requests in
// three hours were recorded as 400 invalid_request_error after uploads that
// stopped after about 35 seconds (design §8).

const clientBodyReaderKey = "xy_resilience_client_body_reader"

// clientBodyReader records whether reading the request body failed in the
// transport.
type clientBodyReader struct {
	io.ReadCloser
	broken atomic.Bool
}

func (r *clientBodyReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if isClientUploadBreak(err) {
		r.broken.Store(true)
	}
	return n, err
}

// trackClientBodyRead wraps the request body before the handlers read it.
// A body that was already read stays as it is.
func trackClientBodyRead(c *gin.Context) {
	if c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return
	}
	if _, preread := c.Request.Body.(*pkghttputil.PrereadBody); preread {
		return
	}
	r := &clientBodyReader{ReadCloser: c.Request.Body}
	c.Request.Body = r
	c.Set(clientBodyReaderKey, r)
}

// clientUploadStatus returns 499 for a 400 caused by a broken upload.
func clientUploadStatus(c *gin.Context, status int) int {
	if status != http.StatusBadRequest {
		return status
	}
	if r, ok := c.Value(clientBodyReaderKey).(*clientBodyReader); ok && r.broken.Load() {
		return statusClientClosedRequest
	}
	return status
}

func isClientUploadBreak(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return false
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) || errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

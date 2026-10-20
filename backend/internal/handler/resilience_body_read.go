package handler

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/gin-gonic/gin"
	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// A client whose upload breaks off has gone away: the request is a client
// cancel (499), not a malformed request (400). In production 12 requests in
// three hours were recorded as 400 invalid_request_error after uploads that
// stopped after about 35 seconds (design §8). Until the body has arrived the
// request is in the upload stage, so such a client is no abuse strike.

const clientBodyReaderKey = "xy_resilience_client_body_reader"

// clientBodyReader records whether reading the request body failed in the
// transport, and ends the upload stage when the body was read to its end.
type clientBodyReader struct {
	io.ReadCloser
	broken atomic.Bool
	state  *resilience.RequestState
}

func (r *clientBodyReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if isClientUploadBreak(err) {
		r.broken.Store(true)
	} else if errors.Is(err, io.EOF) {
		r.state.EndUpload()
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
	r := &clientBodyReader{ReadCloser: c.Request.Body, state: resilience.StateFrom(c.Request.Context())}
	r.state.BeginUpload()
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

// opsRecordedStatus is the status an ops record keeps: a 400 caused by a
// broken upload is a client cancel (499), and a 502/504 answered as 503
// behind a CDN edge keeps its own status, or it would count as a
// no-account 503 in the success metrics (design §14).
func opsRecordedStatus(c *gin.Context) int {
	status := clientUploadStatus(c, c.Writer.Status())
	if status == http.StatusServiceUnavailable {
		if up, err := strconv.Atoi(c.Writer.Header().Get(service.EdgeUpstreamStatusHeader)); err == nil &&
			(up == http.StatusBadGateway || up == http.StatusGatewayTimeout) {
			return up
		}
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

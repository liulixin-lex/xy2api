//go:build unit

package handler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/stretchr/testify/require"
)

// brokenUpload returns part of a body and then the error a server sees when
// the client connection drops mid-upload.
type brokenUpload struct{ sent bool }

func (b *brokenUpload) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, `{"model":"gpt-6-sol","input":"`), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func uploadStatus(t *testing.T, body io.Reader, limit int64) (written, recorded int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if limit > 0 {
		router.Use(func(c *gin.Context) {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
			c.Next()
		})
	}
	router.Use(func(c *gin.Context) {
		trackClientBodyRead(c)
		c.Next()
		recorded = clientUploadStatus(c, c.Writer.Status())
	})
	router.POST("/v1/responses", func(c *gin.Context) {
		if _, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				c.Status(http.StatusRequestEntityTooLarge)
				return
			}
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusBadRequest) // a malformed but complete body
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code, recorded
}

func TestBrokenUploadIsRecordedAsClientCancel(t *testing.T) {
	written, recorded := uploadStatus(t, &brokenUpload{}, 0)
	require.Equal(t, http.StatusBadRequest, written)
	require.Equal(t, statusClientClosedRequest, recorded)

	_, recorded = uploadStatus(t, strings.NewReader(`{"model":`), 0)
	require.Equal(t, http.StatusBadRequest, recorded, "a complete malformed body stays 400")

	_, recorded = uploadStatus(t, strings.NewReader(strings.Repeat("x", 64)), 16)
	require.Equal(t, http.StatusRequestEntityTooLarge, recorded)

	require.False(t, isClientUploadBreak(io.EOF))
	require.False(t, isClientUploadBreak(&http.MaxBytesError{Limit: 1}))
}

//go:build unit

package routes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type inferenceReadFailure struct{ err error }

func (r inferenceReadFailure) Read([]byte) (int, error) { return 0, r.err }
func (r inferenceReadFailure) Close() error             { return nil }

type inferenceUploadTimeout struct{}

func (inferenceUploadTimeout) Error() string   { return "upload timeout" }
func (inferenceUploadTimeout) Timeout() bool   { return true }
func (inferenceUploadTimeout) Temporary() bool { return true }
func TestInferenceUploadReadFailureProtocols(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/gemini:generateContent"} {
		for _, tc := range []struct {
			name    string
			err     error
			status  int
			message string
		}{
			{"timeout", inferenceUploadTimeout{}, 408, "Timed out reading request body"},
			{"limit", &http.MaxBytesError{Limit: 1024}, 413, "Request body too large, limit is 1024B"},
			{"truncated", io.ErrUnexpectedEOF, 400, "Failed to read request body"},
			{"canceled", context.Canceled, 499, ""},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				router := newGatewayRoutesTestRouterWithGroup(allowlistGroup(service.PlatformOpenAI, false))
				if path == "/v1beta/models/gemini:generateContent" {
					router = gin.New()
					router.POST(path, middleware.InferenceBodyValidation(1<<20, handler.WriteInferenceBodyReadError), func(*gin.Context) { t.Fatal("read failure reached dispatch") })
				}
				req := httptest.NewRequest("POST", path, inferenceReadFailure{tc.err})
				req.Header.Set("Authorization", "Bearer test-key")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				if errors.Is(tc.err, context.Canceled) {
					require.Empty(t, rec.Body.String())
					return
				}
				require.Equal(t, tc.message, gjson.Get(rec.Body.String(), "error.message").String())
				if path == "/v1/messages" {
					require.Equal(t, "error", gjson.Get(rec.Body.String(), "type").String())
				}
				if path == "/v1beta/models/gemini:generateContent" {
					require.Equal(t, int64(tc.status), gjson.Get(rec.Body.String(), "error.code").Int())
				} else if path != "/v1/messages" && tc.status == 408 {
					require.Equal(t, "request_upload_timeout", gjson.Get(rec.Body.String(), "error.code").String())
				}
			})
		}
	}
}

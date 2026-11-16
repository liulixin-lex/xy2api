//go:build unit

package handler

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesUTF8BeforeRewrite(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		for _, fixture := range []struct {
			name, input string
			bad         bool
		}{
			{"invalid", "bad\xff", true},
			{"control_before_invalid", "line\n\xff", true},
			{"unicode", "中文😀", false},
			{"control", "line\nnext", false},
		} {
			for _, compressed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/gzip=%t", path, fixture.name, compressed), func(t *testing.T) {
					withHandlerResilience(t, nil)
					up := &captureUpstream{reply: func(_ int, _ *http.Request, _ []byte) (*http.Response, error) {
						return captureResponse(200, captureResponseJSON, false), nil
					}}
					_, _, router := captureFixture(t, service.Account{}, up)
					raw := []byte("\xef\xbb\xbf" + `{"model":"gpt-5.1","input":"` + fixture.input + `"}`)
					body := raw
					if compressed {
						var b bytes.Buffer
						gz := gzip.NewWriter(&b)
						_, err := gz.Write(raw)
						require.NoError(t, err)
						require.NoError(t, gz.Close())
						body = b.Bytes()
					}
					req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					if compressed {
						req.Header.Set("Content-Encoding", "gzip")
					}
					response := httptest.NewRecorder()
					router.ServeHTTP(response, req)
					if fixture.bad {
						require.Equal(t, 400, response.Code, response.Body.String())
						require.Equal(t, "invalid_utf8", gjson.Get(response.Body.String(), "error.code").String())
						require.Equal(t, "body", gjson.Get(response.Body.String(), "error.param").String())
						require.Equal(t, fmt.Sprintf("body contains invalid UTF-8 at byte %d", bytes.IndexByte(raw, 0xff)), gjson.Get(response.Body.String(), "error.message").String())
						require.Zero(t, up.calls)
					} else {
						require.NotContains(t, response.Body.String(), "invalid_utf8")
						require.Equal(t, 1, up.calls, response.Body.String())
						require.False(t, strings.Contains(response.Body.String(), "Failed to parse request body"))
					}
				})
			}
		}
	}
}

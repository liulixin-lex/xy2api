//go:build unit

package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIStreamForbiddenExhaustedResponse(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name      string
		committed bool
	}{{"before commit", false}, {"after commit", true}} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := newGinContextForEndpoint(t, EndpointResponses)
			if tc.committed {
				_, err := c.Writer.WriteString(":\n\n")
				require.NoError(t, err)
			}
			failoverErr := &service.UpstreamFailoverError{
				StatusCode:   http.StatusForbidden,
				Reason:       service.OpenAIStreamForbiddenReason,
				ResponseBody: []byte(`{"error":{"code":"forbidden","message":"upstream access forbidden"}}`),
			}
			(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, failoverErr, tc.committed)

			const message = "Upstream service temporarily unavailable"
			if tc.committed {
				require.Equal(t, http.StatusOK, recorder.Code)
				_, errObject := parseResponsesFailedSSE(t, strings.TrimPrefix(recorder.Body.String(), ":\n\n"))
				require.Equal(t, "server_error", errObject["code"])
				require.Equal(t, message, errObject["message"])
			} else {
				require.Equal(t, http.StatusBadGateway, recorder.Code)
				require.Equal(t, "server_error", gjson.Get(recorder.Body.String(), "error.type").String())
				require.Equal(t, "server_error", gjson.Get(recorder.Body.String(), "error.code").String())
				require.Equal(t, message, gjson.Get(recorder.Body.String(), "error.message").String())
			}
		})
	}
}

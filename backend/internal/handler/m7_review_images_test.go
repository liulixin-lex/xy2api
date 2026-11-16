//go:build unit

package handler

import (
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"net/http"
	"strings"
	"testing"
)

func TestM7ReviewImageStreamErrors(t *testing.T) {
	for _, endpoint := range []string{"/v1/images/generations", "/v1/images/edits"} {
		for _, prelude := range []string{": heartbeat\n\n", "event: image_generation.partial_image\ndata: {\"b64_json\":\"partial\"}\n\n"} {
			t.Run(endpoint+prelude[:5], func(t *testing.T) {
				c, rec := newGinContextForEndpoint(t, endpoint)
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString(prelude)
				require.NoError(t, err)
				h := &OpenAIGatewayHandler{}
				h.handleStreamingAwareErrorWithCode(c, 503, "server_error", "gateway_queue_full", "failure", false, false)
				before := rec.Body.String()
				h.handleStreamingAwareErrorWithCode(c, 503, "server_error", "gateway_queue_full", "again", true, false)
				require.Equal(t, before, rec.Body.String())
				require.Equal(t, http.StatusOK, rec.Code)
				require.NotContains(t, before, "response.failed")
				require.Equal(t, 1, strings.Count(before, "event: error"))
				events := parseSSEEvents(before)
				require.Equal(t, "gateway_queue_full", gjson.Get(events[len(events)-1].data, "error.code").String())
			})
		}
	}
}

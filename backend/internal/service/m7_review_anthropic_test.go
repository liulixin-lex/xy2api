//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestM7ReviewAnthropicNativeTerminators(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
	for _, tc := range []struct{ name, tail string }{
		{"done_only", "data: [DONE]\n\n"},
		{"usage_then_done", "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":3}}\n\ndata: [DONE]\n\n"},
		{"keepalive_middle", "data: keep-alive\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"message_stop", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		svc := newNativeAnthropicHangTestService(30)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(base + tc.tail))}
		res, err := svc.handleNativeAnthropicStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, "fixture", "fixture", "fixture", nil, time.Now())
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Contains(t, rec.Body.String(), "hello")
		if tc.name == "usage_then_done" {
			require.Equal(t, 3, res.Usage.OutputTokens)
		}
		out := 0
		if res != nil {
			out = res.Usage.OutputTokens
		}
		t.Logf("RED-DONE case=%s err=%v out_tokens=%d body_has_hello=%v", tc.name, err, out, strings.Contains(rec.Body.String(), "hello"))
	}
}

//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestM7ReviewAnthropicFirstOutputDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	saved := resilience.Current().Clone()
	defer resilience.Publish(saved)
	st := resilience.Defaults()
	st.Stall.TextStallSeconds = 1 // stands in for the default 120s; stream_data_interval_timeout=3s stands in for 180s
	resilience.Publish(st)
	for _, target := range []string{"chat", "responses"} {
		t.Run(target, func(t *testing.T) {
			svc := newNativeAnthropicHangTestService(3)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			pr, pw := io.Pipe()
			go func() {
				defer pw.Close()
				_, _ = io.WriteString(pw, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n")
				time.Sleep(1600 * time.Millisecond) // silent model latency before the first token, < interval
				_, _ = io.WriteString(pw, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"+
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"+
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
			var err error
			if target == "chat" {
				_, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, "m", "m", "m", nil, time.Now())
			} else {
				_, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "m", "m", "m", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			}
			require.NoError(t, err)
			require.Contains(t, rec.Body.String(), "hello")
		})
	}
}

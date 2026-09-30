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
	"github.com/tidwall/gjson"
)

func TestReviewNativeStreamDeltaTypeDoesNotInventContent(t *testing.T) {
	cases := []struct{ name, field, text string }{
		{"number", ",\"delta\":42", ""},
		{"bool", ",\"delta\":true", ""},
		{"object", ",\"delta\":{\"text\":\"invented\"}", ""},
		{"array", ",\"delta\":[\"invented\"]", ""},
		{"null", ",\"delta\":null", ""},
		{"missing", "", ""},
		{"empty", ",\"delta\":\"\"", ""},
		{"escaped_text", ",\"delta\":\"hello \\n \\\"世界\\\" \\\\ end\"", "hello \n \"世界\" \\ end"},
	}
	for _, kind := range []string{"response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta"} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				ctx := nativeRelayContext(context.Background())
				out := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(out)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				delta := nativeRelaySSE(kind, "\"sequence_number\":0"+tc.field)
				terminal := nativeRelaySSE("response.completed", "\"sequence_number\":1,\"response\":{\"id\":\"resp_typed_delta\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(delta + terminal))}
				result, err := nativeRelayService().handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, strings.HasPrefix(out.Body.String(), delta), "forward the exact upstream event even when delta has a non-string type")
				var completed []byte
				forEachOpenAISSEFrame(out.Body.String(), func(eventType string, payload []byte) {
					if eventType == "response.completed" {
						completed = append([]byte(nil), payload...)
					}
				})
				require.NotEmpty(t, completed)
				require.Equal(t, "resp_typed_delta", gjson.GetBytes(completed, "response.id").String())
				require.Equal(t, int64(1), gjson.GetBytes(completed, "sequence_number").Int())
				require.Equal(t, int64(1), gjson.GetBytes(completed, "response.usage.input_tokens").Int())
				if tc.text == "" {
					require.Empty(t, gjson.GetBytes(completed, "response.output").Array(), "non-string or empty delta must not become reconstructed model text")
					require.Nil(t, result.firstTokenMs, "non-string or empty delta must not start the content clock")
				} else {
					require.NotNil(t, result.firstTokenMs)
					path := "response.output.0.content.0.text"
					if kind != "response.output_text.delta" {
						path = "response.output.0.summary.0.text"
					}
					require.Equal(t, tc.text, gjson.GetBytes(completed, path).String())
				}
			})
		}
	}
}

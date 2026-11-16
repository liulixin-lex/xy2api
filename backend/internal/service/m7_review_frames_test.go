//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Keep the baseline-compatible Responses upstream frame shapes.
func TestM7ReviewResponsesCompatibleFrames(t *testing.T) {
	withResilienceSettings(t, nil)
	created := `{"type":"response.created","response":{"id":"resp_x","model":"fixture","output":[]}}`
	delta := func(s string) string {
		return `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"` + s + `"}`
	}
	completed := `{"type":"response.completed","response":{"id":"resp_x","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello world"}]}],"usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}}}`
	cases := map[string]string{
		"nonjson_data_line": "data: " + created + "\n\n" + "data: " + delta("hello") + "\n\n" +
			"data: keep-alive\n\n" +
			"data: " + delta(" world") + "\n\n" + "data: " + completed + "\n\n",
		"event_message_everywhere": "event: message\ndata: " + created + "\n\n" + "event: message\ndata: " + delta("hello") + "\n\n" +
			"event: message\ndata: " + delta(" world") + "\n\n" + "event: message\ndata: " + completed + "\n\n",
		"multi_data_lines_no_blank": "data: " + created + "\n\n" + "data: " + delta("hello") + "\n" + "data: " + delta(" world") + "\n\n" +
			"data: " + completed + "\n\n",
	}
	for name, stream := range cases {
		for _, target := range []string{"chat", "messages"} {
			for _, buffered := range []bool{false, true} {
				mode := "stream"
				if buffered {
					mode = "buffer"
				}
				t.Run(name+"/"+target+"/"+mode, func(t *testing.T) {
					c, rec := compatStreamContext("/v1/"+target, "")
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
					account := rawChatCompletionsTestAccount()
					var result *OpenAIForwardResult
					var err error
					switch {
					case target == "chat" && buffered:
						result, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
					case target == "chat":
						result, err = svc.handleChatStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now(), 1)
					case buffered:
						result, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
					default:
						result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.False(t, result.PartialFailure)
					require.Equal(t, 13, result.Usage.InputTokens)
					require.Equal(t, 5, result.Usage.OutputTokens)
					require.Contains(t, rec.Body.String(), "world")
					in, out := -1, -1
					if result != nil {
						in, out = result.Usage.InputTokens, result.Usage.OutputTokens
					}
					body := rec.Body.String()
					if len(body) > 600 {
						body = body[len(body)-600:]
					}
					t.Logf("RESULT err=%v status=%d usage=%d/%d world=%v body_tail=%q", err, rec.Code, in, out, strings.Contains(rec.Body.String(), "world"), body)
				})
			}
		}
	}
}

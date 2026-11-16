//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestM7ReviewBusinessDecodeErrors(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "messages"} {
		for _, buffered := range []bool{false, true} {
			for _, row := range []struct {
				name, progress, terminal string
				fail                     bool
				input, output            int
				source                   string
			}{
				{"unknown_extension", `{"input_tokens":4,"output_tokens":2}`, `{"input_tokens":4,"output_tokens":2}`, false, 4, 2, "upstream_sse_terminal"},
				{"invalid_business_delta", `{"input_tokens":4,"output_tokens":2}`, `{"input_tokens":4,"output_tokens":2}`, true, 4, 2, ""},
			} {
				mode := "stream"
				if buffered {
					mode = "buffer"
				}
				t.Run(target+"/"+mode+"/"+row.name, func(t *testing.T) {
					wire := `data: {"type":"response.created","response":{"id":"resp_blue","output":[],"usage":` + row.progress + `}}` + "\n\n"
					if row.name == "unknown_extension" {
						wire += `data: {"type":"vendor.extension","delta":123}` + "\n\n"
					}
					if row.fail {
						wire += `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":123}` + "\n\n"
					}
					wire += `data: {"type":"response.completed","response":{"id":"resp_blue","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":` + row.terminal + `}}` + "\n\n"
					c, rec := compatStreamContext("/v1/"+target, "")
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
					account := rawChatCompletionsTestAccount()
					var result *OpenAIForwardResult
					var err error
					if target == "chat" {
						if buffered {
							result, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
						} else {
							result, err = svc.handleChatStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now(), 1)
						}
					} else {
						if buffered {
							result, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
						} else {
							result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "fixture", "fixture", "fixture", time.Now())
						}
					}
					if result == nil {
						t.Fatalf("nil result err=%v body=%s", err, rec.Body.String())
					}
					t.Logf("err=%v status=%d input=%d output=%d source=%s partial=%t", err, rec.Code, result.Usage.InputTokens, result.Usage.OutputTokens, result.UsageSource, result.PartialFailure)
					if row.fail {
						if err == nil || !result.PartialFailure || result.Usage.InputTokens != 4 || result.Usage.OutputTokens != 2 {
							t.Errorf("business error lost failure or usage: err=%v result=%+v", err, result)
						}
						return
					}
					if err != nil || result.Usage.InputTokens != row.input || result.Usage.OutputTokens != row.output || result.UsageSource != row.source {
						t.Errorf("want success input=%d output=%d source=%s", row.input, row.output, row.source)
					}
				})
			}
		}
	}
}

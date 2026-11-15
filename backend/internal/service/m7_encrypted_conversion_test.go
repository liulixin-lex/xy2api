//go:build unit

package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM7EncryptedConversionRepair(t *testing.T) {
	for _, target := range []string{"chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"http_repair", "sse_repair", "ambiguous", "after_output", "rejected_again", "encrypted_then_field"} {
				if target == "messages" && mode == "encrypted_then_field" {
					continue
				}
				if !stream && mode == "after_output" {
					continue
				}
				t.Run(fmt.Sprintf("%s/%t/%s", target, stream, mode), func(t *testing.T) {
					withResilienceSettings(t, nil)
					req := newWebSearchReplayRequest(t)
					path := "/v1/chat/completions"
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","top_p":0.7,"stream":%t,"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad-A","summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":"good-B","summary":[]},{"role":"user","content":"hi"}]}`, stream))
					if target == "messages" {
						path = "/v1/messages"
						body = []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"max_tokens":32,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"one","signature":"bad-A"},{"type":"thinking","thinking":"two","signature":"good-B"}]},{"role":"user","content":"hi"}]}`, stream))
					}
					req.c.Request.URL.Path = path
					param := `,"param":"input[0].encrypted_content"`
					if target == "messages" {
						param = `,"param":"input[1].encrypted_content"`
					}
					if mode == "ambiguous" {
						param = ""
					}
					errorBody := `{"error":{"type":"invalid_request_error","code":"invalid_encrypted_content"` + param + `,"message":"Encrypted content could not be verified"}}`
					first := newOpenAIUpstreamErrorResponse(400, errorBody)
					if mode != "http_repair" && mode != "rejected_again" && mode != "encrypted_then_field" {
						frames := "event: response.created\ndata: " + `{"type":"response.created","response":{"id":"resp_one","model":"gpt-5.5","status":"in_progress","output":[]}}` + "\n\n"
						if mode == "after_output" {
							frames += "event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}` + "\n\n"
						}
						frames += "event: response.failed\ndata: " + `{"type":"response.failed","response":{"id":"resp_one","status":"failed","error":` + gjson.Get(errorBody, "error").Raw + `,"usage":{"input_tokens":7,"output_tokens":2}}}` + "\n\n"
						first = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(frames))}
					}
					second := webSearchSuccessStream("resp_repaired")
					if mode == "rejected_again" {
						second = newOpenAIUpstreamErrorResponse(400, errorBody)
					}
					up := &httpUpstreamRecorder{responses: []*http.Response{first, second}}
					if mode == "encrypted_then_field" {
						up.responses = []*http.Response{first, newOpenAIUpstreamErrorResponse(400, `{"error":{"code":"unsupported_parameter","param":"top_p","message":"Unsupported parameter: top_p"}}`), second}
					}
					svc := newOpenAIImageGenerationControlTestService(up)
					svc.cfg.JWT.Secret = "fixture-secret"
					var result *OpenAIForwardResult
					var err error
					if target == "chat" {
						result, err = svc.ForwardAsChatCompletions(req.ctx, req.c, webSearchReplayAccount(false), body, "", "")
					} else {
						result, err = svc.ForwardAsAnthropic(req.ctx, req.c, webSearchReplayAccount(false), body, "", "")
					}
					if mode == "http_repair" || mode == "sse_repair" || mode == "encrypted_then_field" {
						require.NoError(t, err)
						if mode == "encrypted_then_field" {
							require.Len(t, up.bodies, 3)
							require.NotContains(t, string(up.bodies[2]), "bad-A")
							require.Contains(t, string(up.bodies[2]), "good-B")
							require.False(t, gjson.GetBytes(up.bodies[2], "top_p").Exists())
						} else {
							require.Len(t, up.bodies, 2)
						}
						require.NotContains(t, string(up.bodies[1]), "bad-A")
						require.Contains(t, string(up.bodies[1]), "good-B")
						require.True(t, gjson.GetBytes(up.bodies[1], "input").Exists())
						require.False(t, gjson.GetBytes(up.bodies[1], "messages").Exists())
					} else {
						var rejection *OpenAIRequestRejectionError
						var failover *UpstreamFailoverError
						require.ErrorAs(t, err, &rejection)
						require.NotErrorAs(t, err, &failover)
						sends := 1
						if mode == "rejected_again" {
							sends = 2
						}
						require.Len(t, up.bodies, sends)
						if target == "chat" {
							require.Contains(t, req.rec.Body.String(), "encrypted_content_rejected")
						} else {
							require.NotContains(t, req.rec.Body.String(), `"code":`)
						}
						require.Contains(t, req.rec.Body.String(), "Encrypted content could not be verified")
						if mode == "after_output" {
							require.NotNil(t, result)
							require.True(t, result.PartialFailure)
							require.Equal(t, 7, result.Usage.InputTokens)
							require.Contains(t, req.rec.Body.String(), "hello")
							require.Equal(t, 1, strings.Count(req.rec.Body.String(), `"type":"invalid_request_error"`))
						} else {
							require.Equal(t, 400, req.rec.Code)
						}
					}
					require.Empty(t, req.session.failures)
				})
			}
		}
	}
}

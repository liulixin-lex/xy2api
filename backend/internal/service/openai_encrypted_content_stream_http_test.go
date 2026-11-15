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

func TestHTTPEncryptedRepairUniqueItemStreams(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"repair", "ambiguous", "after_output"} {
				if !stream && mode == "after_output" {
					continue
				}
				t.Run(fmt.Sprintf("pass=%t/stream=%t/%s", pass, stream, mode), func(t *testing.T) {
					withResilienceSettings(t, nil)
					req := newWebSearchReplayRequest(t)
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","session_id":"m10-stream","stream":%t,"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad-A","summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":"good-B","summary":[]},{"type":"compaction","id":"cmp_good","encrypted_content":"good-C"}]}`, stream))
					param := `,"param":"input[0].encrypted_content"`
					if mode == "ambiguous" {
						param = ""
					}
					failed := fmt.Sprintf(`{"type":"response.failed","response":{"id":"resp_one","status":"failed","error":{"type":"invalid_request_error","code":"invalid_encrypted_content"%s,"message":"Encrypted content could not be verified"},"usage":{"input_tokens":7,"output_tokens":2}}}`, param)
					frames := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_one\",\"model\":\"gpt-5.5\"}}\n\n"
					if mode == "after_output" {
						frames += "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
					}
					frames += "event: response.failed\ndata: " + failed + "\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_one\",\"status\":\"completed\",\"output\":[]}}\n\n"
					up := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(frames))}, webSearchSuccessStream("resp_repaired")}}
					svc := newOpenAIImageGenerationControlTestService(up)
					svc.cfg.JWT.Secret = "fixture-secret"
					repo := &openAIModelAvailabilityRepo{}
					svc.accountRepo = repo
					result, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(pass), body)
					if mode == "repair" {
						require.NoError(t, err)
						require.Len(t, up.bodies, 2)
						require.False(t, gjson.GetBytes(up.bodies[1], "input.0.encrypted_content").Exists())
						require.Equal(t, "good-B", gjson.GetBytes(up.bodies[1], "input.1.encrypted_content").String())
						require.Equal(t, "good-C", gjson.GetBytes(up.bodies[1], "input.2.encrypted_content").String())
					} else {
						var rejection *OpenAIRequestRejectionError
						var failover *UpstreamFailoverError
						require.ErrorAs(t, err, &rejection)
						require.NotErrorAs(t, err, &failover)
						require.Len(t, up.bodies, 1)
						require.Contains(t, req.rec.Body.String(), "encrypted_content_rejected")
						require.NotEmpty(t, currentOpsUpstreamEvents(req.c), "terminal refusals retain sanitized upstream evidence")
						if mode == "after_output" {
							require.NotNil(t, result)
							require.True(t, result.PartialFailure)
							require.EqualValues(t, 7, result.Usage.InputTokens)
							require.Equal(t, 200, req.rec.Code)
							require.Contains(t, req.rec.Body.String(), "hello")
							require.Equal(t, 1, strings.Count(req.rec.Body.String(), `"type":"response.failed"`))
							require.NotContains(t, req.rec.Body.String(), `"type":"response.completed"`)
							require.Equal(t, 1, strings.Count(req.rec.Body.String(), "event: response.failed"))
						} else {
							require.Equal(t, 400, req.rec.Code)
						}
					}
					require.Zero(t, repo.setErrorCalls)
					require.Zero(t, repo.tempCalls)
					require.Empty(t, req.session.failures)
				})
			}
		}
	}
}

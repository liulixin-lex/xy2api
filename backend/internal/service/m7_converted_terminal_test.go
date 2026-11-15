//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestM7ConvertedTerminalOutcomes(t *testing.T) {
	withResilienceSettings(t, nil)
	opening := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\",\"model\":\"fixture\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n"
	completed := `{"type":"response.completed","response":{"id":"resp_fixture","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}}`
	for _, target := range []string{"chat", "messages"} {
		for _, buffered := range []bool{false, true} {
			for _, tc := range []struct {
				name, tail    string
				failed, usage bool
			}{
				{"completed_no_usage", completed, false, false},
				{"done_completed", strings.Replace(completed, "response.completed", "response.done", 1), false, false},
				{"done_incomplete", `{"type":"response.done","response":{"id":"resp_fixture","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":13,"output_tokens":5}}}`, false, true},
				{"done_failed", `{"type":"response.done","response":{"id":"resp_fixture","status":"failed","error":{"code":"fixture_failed","message":"fixture failed"},"usage":{"input_tokens":13,"output_tokens":5}}}`, true, true},
				{"done_cancelled", `{"type":"response.done","response":{"id":"resp_fixture","status":"cancelled","usage":{"input_tokens":13,"output_tokens":5}}}`, true, true},
				{"unknown_status", `{"type":"response.done","response":{"status":"future","usage":{"input_tokens":13,"output_tokens":5}}}`, true, true},
				{"conflicting_type", `{"type":"response.completed","response":{"status":"failed","usage":{"input_tokens":13,"output_tokens":5}}}`, true, true},
				{"numeric_error_then_completed", `{"error":{"code":12345},"usage":{"input_tokens":13,"output_tokens":5}}` + "\n\ndata: " + completed, true, true},
			} {
				name := target + "/stream/" + tc.name
				if buffered {
					name = target + "/buffer/" + tc.name
				}
				t.Run(name, func(t *testing.T) {
					c, rec := compatStreamContext("/v1/"+target, "")
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(opening + "data: " + tc.tail + "\n\n"))}
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
					if tc.failed {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
						require.True(t, ProtocolTerminalClaimed(c))
					}
					require.NotNil(t, result)
					require.True(t, result.ResponseFactsKnown)
					require.Equal(t, tc.usage, result.UsagePresent)
					require.Equal(t, tc.failed, result.PartialFailure)
					if tc.usage {
						require.Equal(t, 13, result.Usage.InputTokens)
						require.Equal(t, 5, result.Usage.OutputTokens)
					}
					if tc.failed {
						require.NotContains(t, rec.Body.String(), `"finish_reason":"stop"`)
						require.NotContains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
					}
					if tc.name == "done_incomplete" {
						if target == "chat" {
							require.Contains(t, rec.Body.String(), `"finish_reason":"length"`)
						} else {
							require.Contains(t, rec.Body.String(), `"stop_reason":"max_tokens"`)
						}
					}
				})
			}
		}
	}
}

func TestM7NumericErrorKeepsUsage(t *testing.T) {
	TestM7ConvertedTerminalOutcomes(t)
}

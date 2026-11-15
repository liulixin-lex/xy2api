//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestM7ChatFallbackRequiresTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	content := `{"id":"chat_fixture","choices":[{"index":0,"delta":{"content":"hello"}}]}`
	meter := `{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":6,"total_tokens":17}}`
	finish := `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	for _, target := range []string{"responses", "messages"} {
		for _, tc := range []struct {
			name                               string
			payloads                           []string
			readError, failed, terminal, usage bool
		}{
			{"tail_meter", []string{content, meter}, false, false, false, true},
			{"zero_meter", []string{content, `{"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}`}, false, false, false, true},
			{"finish_only", []string{content, finish}, false, false, true, false},
			{"done_only", []string{content, "[DONE]"}, false, false, true, false},
			{"delta_only", []string{content}, false, true, false, false},
			{"early_meter", []string{meter, content}, false, true, false, true},
			{"partial_meter", []string{content, `{"choices":[],"usage":{"prompt_tokens":11}}`}, false, true, false, true},
			{"new_content_after_meter", []string{content, meter, content}, false, true, false, true},
			{"bad_tail", []string{content, meter, `{"choices":`}, false, true, false, true},
			{"reader_error", []string{content, meter}, true, true, false, true},
			{"finish_then_read_error", []string{content, finish}, true, false, true, false},
			{"role_only", []string{`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`}, false, false, false, false},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				var wire strings.Builder
				for _, payload := range tc.payloads {
					wire.WriteString("data: " + payload + "\n\n")
				}
				body := strings.TrimSuffix(wire.String(), "\n")
				reader := io.NopCloser(strings.NewReader(body))
				if tc.readError {
					reader = &openAIChatStreamReadErrorCloser{payload: []byte(body), err: errors.New("fixture read failure")}
				}
				c, rec := compatStreamContext("/v1/"+target, "")
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				var result *OpenAIForwardResult
				var err error
				if target == "responses" {
					result, err = svc.streamChatCompletionsAsResponses(c, resp, rawChatCompletionsTestAccount(), "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now())
				} else {
					result, err = svc.streamChatCompletionsAsAnthropic(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now())
				}
				if tc.failed {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NotNil(t, result)
				require.Equal(t, tc.failed, result.PartialFailure)
				require.True(t, result.ResponseFactsKnown)
				require.Equal(t, tc.terminal, result.TerminalSeen)
				require.Equal(t, tc.terminal, result.TerminalValid)
				require.Equal(t, tc.usage, result.UsagePresent)
				require.Equal(t, "sse", result.ResponseBodyKind)
				if tc.name == "tail_meter" {
					require.Equal(t, 11, result.Usage.InputTokens)
					require.Equal(t, 6, result.Usage.OutputTokens)
					require.Equal(t, "upstream_sse_progressive", result.UsageSource)
				}
				terminal := "event: message_stop"
				if target == "responses" {
					terminal = "event: response.completed"
				}
				if tc.failed {
					require.NotContains(t, rec.Body.String(), terminal)
				} else {
					require.Contains(t, rec.Body.String(), terminal)
				}
			})
		}
	}
}

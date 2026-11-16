//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestM7AnthropicBufferedRequiresStop(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n"
	content := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"partial\"}}\n\n"
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	reason := "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	for _, target := range []string{"chat", "responses", "native_chat", "native_responses"} {
		for _, tc := range []struct {
			name, wire                           string
			readerError, failed, usage, terminal bool
		}{
			{"empty", "", false, true, false, false},
			{"start_only", start, false, true, true, false},
			{"partial", start + content, false, true, true, false},
			{"reader_error", start + content, true, true, true, false},
			{"explicit_error", start + content + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"failed\"}}\n\n", false, true, true, true},
			{"bad_tail", start + content + "event: message_stop\ndata: {\"type\":\"message_stop\"\n\n", false, true, true, false},
			{"event_only", start + content + "event: message_stop\n", false, true, true, false},
			{"done_only", start + content + "data: [DONE]\n\n", false, false, true, true},
			{"usage_then_done", start + content + "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\ndata: [DONE]\n\n", false, false, true, true},
			{"keepalive_middle", start + content + "data: keep-alive\n\n" + stop, false, false, true, true},
			{"complete_stop", start + content + reason + stop, false, false, true, true},
			{"stop_reason_only", start + content + reason, false, false, true, true},
			{"stop_then_reader_error", start + content + stop, true, false, true, true},
			{"reason_then_reader_error", start + content + reason, true, false, true, true},
			{"max_tokens", start + content + strings.Replace(reason, "end_turn", "max_tokens", 1), false, false, true, true},
			{"missing_usage", strings.Replace(start, `,"usage":{"input_tokens":5,"output_tokens":0}`, "", 1) + content + stop, false, false, false, true},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				c, rec := compatStreamContext("/v1/"+target, "")
				reader := io.NopCloser(strings.NewReader(tc.wire))
				if tc.readerError {
					reader = &openAIChatStreamReadErrorCloser{payload: []byte(tc.wire), err: errors.New("fixture read error")}
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}
				var known, present, seen, valid bool
				var tokens int
				var err error
				if strings.HasPrefix(target, "native_") {
					svc := &OpenAIGatewayService{}
					var result *OpenAIForwardResult
					if target == "native_chat" {
						result, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now())
					} else {
						result, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "fixture", "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					}
					require.NotNil(t, result)
					known, present, seen, valid = result.ResponseFactsKnown, result.UsagePresent, result.TerminalSeen, result.TerminalValid
					tokens = result.Usage.InputTokens
					require.Equal(t, tc.failed, result.PartialFailure)
				} else {
					svc := &GatewayService{}
					var result *ForwardResult
					if target == "chat" {
						result, err = svc.handleCCBufferedFromAnthropic(resp, c, "fixture", "fixture", nil, time.Now())
					} else {
						result, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
					}
					require.NotNil(t, result)
					known, present, seen, valid = result.ResponseFactsKnown, result.UsagePresent, result.TerminalSeen, result.TerminalValid
					tokens = result.Usage.InputTokens
				}
				if tc.failed {
					require.Error(t, err)
					require.Equal(t, 502, rec.Code)
				} else {
					require.NoError(t, err)
					require.Equal(t, 200, rec.Code)
				}
				require.True(t, known)
				require.Equal(t, tc.usage, present)
				require.Equal(t, tc.terminal, seen)
				require.Equal(t, tc.terminal, valid)
				if tc.usage {
					require.Equal(t, 5, tokens)
				}
				if tc.name == "max_tokens" {
					if strings.HasSuffix(target, "chat") {
						require.Contains(t, rec.Body.String(), `"finish_reason":"length"`)
					} else {
						require.Contains(t, rec.Body.String(), `"status":"incomplete"`)
					}
				}
			})
		}
	}
}

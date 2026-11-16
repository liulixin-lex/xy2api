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

func m7ReviewChatStream(t *testing.T, target, wire string) (*OpenAIForwardResult, string, error) {
	t.Helper()
	c, rec := compatStreamContext("/v1/"+target, "")
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	account := rawChatCompletionsTestAccount()
	opts := m7BridgeConversionOptions(c.Request.Context(), account)
	var result *OpenAIForwardResult
	var err error
	switch target {
	case "responses":
		result, err = svc.streamChatCompletionsAsResponses(c, resp, account, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now(), opts)
	case "messages":
		result, err = svc.streamChatCompletionsAsAnthropic(c, resp, account, "fixture", "fixture", "fixture", nil, nil, time.Now(), opts)
	default:
		result, err = svc.streamRawChatCompletions(c, resp, account, "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
	}
	return result, rec.Body.String(), err
}

const m7ReviewChatContent = `{"choices":[{"index":0,"delta":{"content":"hello"}}]}`
const m7ReviewChatFinish = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

func TestM7ReviewChatKeepalive(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"raw", "responses", "messages"} {
		for _, row := range []struct {
			name   string
			frames []string
			failed bool
		}{
			{"middle", []string{m7ReviewChatContent, "keep-alive", m7ReviewChatFinish, "[DONE]"}, false},
			{"first", []string{"keep-alive", m7ReviewChatContent, m7ReviewChatFinish, "[DONE]"}, false},
			{"bad_tail", []string{m7ReviewChatContent, `{"choices":`}, true},
		} {
			t.Run(target+"/"+row.name, func(t *testing.T) {
				wire := "data: " + strings.Join(row.frames, "\n\ndata: ") + "\n\n"
				result, body, err := m7ReviewChatStream(t, target, wire)
				if row.failed {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.False(t, result.PartialFailure)
				require.Contains(t, body, "hello")
			})
		}
	}
}

func TestM7ReviewChatStringCompatibility(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"raw", "responses", "messages"} {
		for _, content := range []string{`"content":"",`, `"content":null,`, ``} {
			t.Run(target+content, func(t *testing.T) {
				tool := `{"choices":[{"index":0,"delta":{` + content + `"tool_calls":[{"index":0,"id":"call_fixed","type":"function","function":{"name":"original_tool","arguments":"{}"}}]}}]}`
				finish := `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
				result, body, err := m7ReviewChatStream(t, target, m7ReviewWire(tool, finish, "[DONE]"))
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Contains(t, body, "original_tool")
				require.Contains(t, body, "call_fixed")
			})
		}
	}
}

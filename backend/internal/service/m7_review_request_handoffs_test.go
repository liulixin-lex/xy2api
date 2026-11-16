//go:build unit

package service

import (
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestM7ReviewStreamOptionsAndPartialActual(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"raw", "chat_to_responses", "responses_shape"} {
		for _, options := range []string{`{}`, `{"include_usage":false,"include_obfuscation":true}`, `{"include_usage":true}`} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/partial=%t", route, options, partial), func(t *testing.T) {
					content := `"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"`
					if route == "responses_shape" {
						content = `"input":"hi","reasoning":{"effort":"none"}`
					}
					body := []byte(`{"model":"gpt-6.1-sol","stream":true,"stream_options":` + options + `,` + content + `}`)
					c, _ := compatStreamContext("/v1/chat/completions", string(body))
					ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
					c.Request = c.Request.WithContext(ctx)
					wire := m7ReviewWire(m7ReviewCreated, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`)
					if !partial {
						wire += m7ReviewWire(m7ReviewCompleted(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]`))
					}
					account := newOpenAIRejectedFieldTestAccount()
					if route == "raw" {
						account = rawChatCompletionsTestAccount()
						account.Credentials["api_protocol"] = APIProtocolChatCompletions
						wire = m7ReviewWire(m7ReviewChatContent, `{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
						if partial {
							wire = m7ReviewWire(`{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`, m7ReviewChatContent)
						} else {
							wire += m7ReviewWire(m7ReviewChatFinish, "[DONE]")
						}
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
					svc := newOpenAIRejectedFieldTestService(upstream)
					svc.cfg = rawChatCompletionsTestConfig()
					var result *OpenAIForwardResult
					var err error
					if route == "raw" {
						result, err = svc.forwardAsRawChatCompletions(ctx, c, account, body, "")
					} else {
						result, err = svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
					}
					if partial {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
					require.NotNil(t, result)
					require.Equal(t, partial, result.PartialFailure)
					require.NotNil(t, result.ReasoningEffort)
					require.Equal(t, "low", *result.ReasoningEffort)
					require.Len(t, upstream.requests, 1)
					if route == "raw" {
						require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
						require.Equal(t, gjson.Get(options, "include_obfuscation").Exists(), gjson.GetBytes(upstream.lastBody, "stream_options.include_obfuscation").Exists())
						require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
					} else {
						require.False(t, gjson.GetBytes(upstream.lastBody, "stream_options").Exists())
						require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
						require.Contains(t, resilience.SnapshotConversionNotices(ctx), apicompat.ConversionNotice{Field: "stream_options", Action: "omit"})
					}
				})
			}
		}
	}
}

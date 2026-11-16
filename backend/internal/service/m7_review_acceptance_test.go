//go:build unit

package service

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func m7ReviewResponsesReader(t *testing.T, target string, buffered bool, wire string) (*OpenAIForwardResult, int, string, error) {
	t.Helper()
	c, rec := compatStreamContext("/v1/"+target, "")
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
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
	return result, rec.Code, rec.Body.String(), err
}

const m7ReviewCreated = `{"type":"response.created","response":{"id":"resp_fixture","model":"fixture","output":[],"usage":{"input_tokens":4,"output_tokens":2}}}`

func m7ReviewWire(frames ...string) string {
	return "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
}
func m7ReviewCompleted(output string) string {
	return `{"type":"response.completed","response":{"id":"resp_fixture","model":"fixture","status":"completed","output":` + output + `,"usage":{"input_tokens":4,"output_tokens":2}}}`
}

func TestM7ReviewErrorAtParserFinish(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "messages"} {
		for _, buffered := range []bool{false, true} {
			for _, code := range []string{"503", `"503"`} {
				for _, eof := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%t/%s/eof=%t", target, buffered, code, eof), func(t *testing.T) {
						wire := m7ReviewWire(`{"error":{"code":` + code + `},"usage":{"input_tokens":4,"output_tokens":2}}`)
						if eof {
							wire = strings.TrimRight(wire, "\n")
						} else {
							wire += m7ReviewWire(m7ReviewCompleted("[]"))
						}
						result, status, body, err := m7ReviewResponsesReader(t, target, buffered, wire)
						require.Error(t, err)
						require.NotNil(t, result)
						require.True(t, result.PartialFailure)
						require.Equal(t, 4, result.Usage.InputTokens)
						require.Equal(t, 2, result.Usage.OutputTokens)
						require.NotEqual(t, 503, status, "numeric error code is not a trusted HTTP status")
						require.NotContains(t, body, `"finish_reason":"stop"`)
						require.NotContains(t, body, "event: message_stop")
						if failure, ok := err.(*UpstreamFailoverError); ok {
							require.NotEqual(t, 503, failure.StatusCode)
						}
					})
				}
			}
		}
	}
}

func TestM7BufferedDoneOnlyService(t *testing.T) {
	first := `{"id":"msg_first","type":"message","role":"assistant","content":[{"type":"output_text","text":"done-first"}]}`
	hosted := `{"id":"ws_fixture","type":"web_search_call","status":"completed","action":{"type":"search","query":"fixture"}}`
	last := `{"id":"msg_last","type":"message","role":"assistant","content":[{"type":"output_text","text":"done-last"}]}`
	frames := []string{m7ReviewCreated}
	for i, item := range []string{first, hosted, last} {
		frames = append(frames, fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, i, item))
	}
	frames = append(frames, m7ReviewCompleted("[]"))
	wire := m7ReviewWire(frames...)
	for _, target := range []string{"chat", "messages"} {
		t.Run(target, func(t *testing.T) {
			result, status, body, err := m7ReviewResponsesReader(t, target, true, wire)
			require.NoError(t, err)
			require.Equal(t, 200, status)
			require.NotNil(t, result)
			require.Equal(t, 1, strings.Count(body, "done-first"))
			require.Equal(t, 1, strings.Count(body, "done-last"))
			require.Less(t, strings.Index(body, "done-first"), strings.Index(body, "done-last"))
		})
	}
	// Inspect the actual shared service reader before target projection: hosted
	// output must retain its original position even if a target omits that kind.
	c, _ := compatStreamContext("/v1/chat", "")
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
	response, _, acc, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).readOpenAICompatBufferedTerminal(resp, c, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture")
	require.NoError(t, err)
	require.NotNil(t, response)
	acc.SupplementResponseOutput(response)
	require.NoError(t, acc.Err())
	require.Len(t, response.Output, 3)
	require.Equal(t, "ws_fixture", response.Output[1].ID)
}

func TestM7ReviewConvertedBusinessSequences(t *testing.T) {
	toolA := `{"id":"fc_a","type":"function_call","call_id":"call_a","name":"alpha","arguments":"{\"x\":1}"}`
	toolB := `{"id":"fc_b","type":"function_call","call_id":"call_b","name":"beta","arguments":"{}"}`
	for _, target := range []string{"chat", "messages"} {
		for _, buffered := range []bool{false, true} {
			for _, kind := range []string{"interleaved_tools", "reasoning_text_reasoning", "invalid_tool", "error_echo"} {
				t.Run(fmt.Sprintf("%s/%t/%s", target, buffered, kind), func(t *testing.T) {
					frames := []string{m7ReviewCreated}
					switch kind {
					case "interleaved_tools":
						frames = append(frames,
							`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_a","type":"function_call","call_id":"call_a","name":"alpha","arguments":""}}`,
							`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_b","type":"function_call","call_id":"call_b","name":"beta","arguments":""}}`,
							`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"x\":"}`,
							`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{}"}`,
							`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"1}"}`,
							`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"x\":1}"}`,
							`{"type":"response.output_item.done","output_index":0,"item":`+toolA+`}`,
							`{"type":"response.output_item.done","output_index":1,"item":`+toolB+`}`,
							m7ReviewCompleted("["+toolA+","+toolB+"]"))
					case "reasoning_text_reasoning":
						items := []string{`{"id":"rs_a","type":"reasoning","summary":[{"type":"summary_text","text":"think-first"}]}`, `{"id":"msg_a","type":"message","role":"assistant","content":[{"type":"output_text","text":"visible-answer"}]}`, `{"id":"rs_b","type":"reasoning","summary":[{"type":"summary_text","text":"think-last"}]}`}
						for i, item := range items {
							frames = append(frames, fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, i, item))
						}
						frames = append(frames, m7ReviewCompleted("["+strings.Join(items, ",")+"]"))
					case "invalid_tool":
						frames = append(frames, m7ReviewCompleted(`[ {"id":"fc_bad","type":"function_call","call_id":"call_bad","name":"broken","arguments":"{bad"}]`))
					case "error_echo":
						frames = append(frames, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"the word error is content"}`, m7ReviewCompleted(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the word error is content"}]}]`))
					}
					result, _, body, err := m7ReviewResponsesReader(t, target, buffered, m7ReviewWire(frames...))
					require.NotNil(t, result)
					if kind == "invalid_tool" {
						require.Error(t, err)
						require.True(t, result.PartialFailure)
						return
					}
					require.NoError(t, err)
					require.False(t, result.PartialFailure)
					if kind == "interleaved_tools" {
						require.Equal(t, 1, strings.Count(body, `"name":"alpha"`))
						require.Equal(t, 1, strings.Count(body, `"name":"beta"`))
					}
					if kind == "reasoning_text_reasoning" {
						require.Contains(t, body, "think-first")
						require.Contains(t, body, "visible-answer")
						require.Contains(t, body, "think-last")
					}
					if target == "messages" && !buffered {
						open := map[int64]bool{}
						for _, line := range strings.Split(body, "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							raw := strings.TrimPrefix(line, "data: ")
							idx := gjson.Get(raw, "index").Int()
							switch gjson.Get(raw, "type").String() {
							case "content_block_start":
								require.False(t, open[idx])
								open[idx] = true
							case "content_block_delta":
								require.True(t, open[idx])
							case "content_block_stop":
								require.True(t, open[idx])
								open[idx] = false
							}
						}
						for _, active := range open {
							require.False(t, active)
						}
					}
				})
			}
		}
	}
}

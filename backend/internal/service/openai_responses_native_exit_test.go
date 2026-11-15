//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func m6Nonstream(t *testing.T, pass bool, body, contentType string) (*httptest.ResponseRecorder, *OpenAIUsage, error) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}, "Content-Length": {"99999"}}, Body: io.NopCloser(strings.NewReader(body))}
	account := newOpenAIRejectedFieldTestAccount()
	if pass {
		r, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, "gpt-test", "gpt-test")
		if r != nil {
			return w, r.OpenAIUsage, err
		}
		return w, nil, err
	}
	r, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-test", "gpt-test")
	if r != nil {
		return w, r.OpenAIUsage, err
	}
	return w, nil, err
}

func m6Frame(payload string) string { return "data: " + payload + "\n\n" }

func TestM6NonstreamBodyKindAndContentType(t *testing.T) {
	response := `{"id":"r1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"data: text; event: text"}]}],"usage":{"input_tokens":41,"output_tokens":7}}`
	sse := m6Frame(`{"type":"response.completed","response":` + response + `}`)
	multiline := "event: response.completed\r\ndata: {\r\ndata: \"response\":" + response + "}\r\n\r\n"
	for _, pass := range []bool{false, true} {
		for _, tc := range []struct{ name, body, ct string }{
			{"json", response, "application/json"},
			{"json_wrong_header", response, "text/event-stream"},
			{"json_no_header", response, ""},
			{"sse", sse, "text/event-stream"},
			{"sse_wrong_header", sse, "application/json"},
			{"sse_no_header", sse, ""},
			{"sse_multiline_crlf", multiline, "text/event-stream"},
		} {
			t.Run(fmt.Sprintf("%s/pass=%t", tc.name, pass), func(t *testing.T) {
				w, usage, err := m6Nonstream(t, pass, tc.body, tc.ct)
				t.Logf("status=%d content_type=%q usage=%+v body=%s error=%v", w.Code, w.Header().Get("Content-Type"), usage, w.Body.String(), err)
				require.NoError(t, err)
				require.NotNil(t, usage)
				require.Equal(t, 41, usage.InputTokens)
				require.Equal(t, 7, usage.OutputTokens)
				require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
				require.Equal(t, fmt.Sprint(w.Body.Len()), w.Header().Get("Content-Length"))
				require.True(t, gjson.Valid(w.Body.String()))
				require.Contains(t, w.Body.String(), "data: text; event: text")
			})
		}
	}
}

func TestM6NativeMixedDoneDelta(t *testing.T) {
	raw, ok := collectRawResponsesOutputItemsFromSSE(m6Frame(`{"type":"response.output_item.done","output_index":1,"item":{"id":"b","type":"future"}}`) + m6Frame(`{"type":"response.output_item.done","output_index":0,"item":{"id":"a","type":"future"}}`))
	require.True(t, ok)
	require.JSONEq(t, `[{"id":"a","type":"future"},{"id":"b","type":"future"}]`, string(raw))
	text := `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"answer"}`
	tool := `{"type":"response.output_item.done","output_index":1,"item":{"id":"f1","type":"function_call","call_id":"c1","name":"f","arguments":"{}"}}`
	for _, tc := range []struct {
		name, frames, final, expected string
	}{
		{"mixed", m6Frame(text) + m6Frame(tool), `[]`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"id":"f1","type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]`},
		{"reversed", m6Frame(tool) + m6Frame(text) + m6Frame(tool), `[]`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"id":"f1","type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]`},
		{"partial_terminal", m6Frame(text) + m6Frame(tool), `[{"id":"f1","type":"function_call","call_id":"c1","name":"f","arguments":"{\"final\":true}"}]`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"id":"f1","type":"function_call","call_id":"c1","name":"f","arguments":"{\"final\":true}"}]`},
		{"authoritative_empty", m6Frame(text) + m6Frame(tool), `[{"type":"message","content":[]},{"id":"f1","arguments":""}]`, `[{"type":"message","role":"assistant","content":[]},{"id":"f1","type":"function_call","call_id":"c1","name":"f","arguments":""}]`},
		{"content_indices", m6Frame(`{"type":"response.output_text.delta","output_index":0,"content_index":1,"sequence_number":2,"delta":"second"}`) + m6Frame(text) + m6Frame(`{"type":"response.output_text.delta","output_index":0,"content_index":1,"sequence_number":2,"delta":"second"}`), `[]`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"output_text","text":"second"}]}]`},
		{"text_done_tool_delta", m6Frame(`{"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","content":[{"type":"output_text","text":"answer"}]}}`) + m6Frame(`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"f1","call_id":"c1","name":"f","delta":"{}"}`), `[]`, `[{"id":"m1","type":"message","content":[{"type":"output_text","text":"answer"}]},{"id":"f1","type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]`},
		{"partial_content", m6Frame(text) + m6Frame(`{"type":"response.output_text.delta","output_index":0,"content_index":1,"delta":"second"}`), `[{"type":"message","content":[{"type":"output_text","text":""}]}]`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":""},{"type":"output_text","text":"second"}]}]`},
		{"sparse_content", m6Frame(`{"type":"response.output_text.delta","output_index":0,"content_index":1,"delta":"second"}`), `[{"type":"message","content":[{"type":"output_text","text":"first"}]}]`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"second"}]}]`},
		{"id_only", m6Frame(`{"type":"response.output_item.added","output_index":0,"item":{"id":"m1","type":"message","content":[],"opaque":{"v":1}}}`) + m6Frame(`{"type":"response.output_text.delta","item_id":"m1","delta":"answer"}`), `[]`, `[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}],"opaque":{"v":1}}]`},
		{"unknown_and_compaction", m6Frame(`{"type":"response.output_item.added","output_index":1,"item":{"id":"cmp","type":"compaction","encrypted_content":"fixture"}}`) + m6Frame(`{"type":"response.output_item.done","output_index":0,"item":{"id":"u","type":"future_item","opaque":[1,{"keep":true}]}}`), `[]`, `[{"id":"u","type":"future_item","opaque":[1,{"keep":true}]},{"id":"cmp","type":"compaction","encrypted_content":"fixture"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := `{"id":"r1","status":"completed","output":` + tc.final + `,"usage":{"input_tokens":41,"output_tokens":7}}`
			for _, pass := range []bool{false, true} {
				w, usage, err := m6Nonstream(t, pass, tc.frames+m6Frame(`{"type":"response.completed","response":`+final+`}`), "text/event-stream")
				require.NoError(t, err)
				require.Equal(t, 41, usage.InputTokens)
				require.Equal(t, 7, usage.OutputTokens)
				require.JSONEq(t, tc.expected, gjson.Get(w.Body.String(), "output").Raw, "pass=%t", pass)
			}
		})
	}
}

func TestM6GrokActualAndPartialFailure(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			body := `{"model":"grok-high","input":"hello","stream":` + fmt.Sprint(partial) + `}`
			upstreamBody := `{"id":"r1","output":[],"usage":{"input_tokens":13,"output_tokens":5}}`
			contentType := "application/json"
			if partial {
				body = strings.TrimSuffix(body, "}") + `,"reasoning":{"effort":"high"}}`
				contentType = "text/event-stream"
				upstreamBody = m6Frame(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"answer"}`) + m6Frame(`{"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"server_error","message":"failed after output"},"usage":{"input_tokens":13,"output_tokens":5}}}`)
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(upstreamBody))}}
			svc := newOpenAIRejectedFieldTestService(upstream)
			account := grokProtocolAPIKeyAccount(7104)
			account.Credentials["model_mapping"] = map[string]any{"grok-high": "grok-4.7"}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			result, err := svc.forwardGrokResponses(context.Background(), c, account, []byte(body), "grok-high", partial, time.Now())
			if partial {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			require.Equal(t, partial, result.PartialFailure)
			require.Equal(t, 13, result.Usage.InputTokens)
			require.Equal(t, 5, result.Usage.OutputTokens)
			if partial {
				require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, "high", *result.ReasoningEffort)
			} else {
				require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists())
				require.Nil(t, result.ReasoningEffort, "missing wire effort must not be inferred from the client alias")
			}
		})
	}
}

func TestM6CompactFailureModel(t *testing.T) {
	for _, model := range []string{"client-model", ""} {
		t.Run(model, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			state := resilience.NewRequestState()
			state.NoteTurn(1, model, "fixture")
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil).WithContext(resilience.WithRequestState(context.Background(), state))
			SetOpsUpstreamModel(c, "mapped-model")
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.Write([]byte(": heartbeat\n\n"))
			writeOpenAICompactSSEFailureMessage(c, 502, "upstream_error", "fixture failure")
			writeOpenAICompactSSEFailureMessage(c, 502, "upstream_error", "duplicate")
			require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed"))
			var payload []byte
			forEachOpenAISSEFrame(w.Body.String(), func(_ string, data []byte) { payload = data })
			require.Equal(t, model, gjson.GetBytes(payload, "response.model").String())
			require.Greater(t, gjson.GetBytes(payload, "response.created_at").Int(), int64(0))
			require.True(t, gjson.GetBytes(payload, "sequence_number").Exists())
			require.True(t, ProtocolTerminalClaimed(c))
		})
	}
}

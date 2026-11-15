//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
			require.True(t, result.ResponseFactsKnown)
			require.True(t, result.UsagePresent)
			require.Equal(t, partial, result.TerminalSeen)
			require.Equal(t, partial, result.TerminalValid)
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

func TestM6PassthroughToolHistory400(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, message := range []string{"No tool call found for function call output with call_id call_wrong at input[1]", "No tool output found for function call call_missing at input[1]"} {
		t.Run(message, func(t *testing.T) {
			errorBody := `{"error":{"type":"invalid_request_error","code":"invalid_request_error","param":"input[1].call_id","message":"` + message + `"}}`
			upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(400, errorBody)}
			repo := &openAIModelAvailabilityRepo{}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo, rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
			account := newOpenAIRejectedFieldTestAccount()
			account.Extra["openai_passthrough"] = true
			c, w := newOpenAIUpstreamErrorTestContext(t)
			_, err := svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"gpt-5.6-sol","stream":false,"input":[{"type":"function_call_output","call_id":"call_wrong","output":"ok"}]}`))
			require.Error(t, err)
			var failure *UpstreamFailoverError
			require.NotErrorAs(t, err, &failure)
			require.Len(t, upstream.bodies, 1)
			require.Equal(t, 400, w.Code)
			require.Equal(t, "invalid_request_error", gjson.Get(w.Body.String(), "error.code").String())
			require.Equal(t, "invalid_request_error", gjson.Get(w.Body.String(), "error.type").String())
			require.Equal(t, "input[1].call_id", gjson.Get(w.Body.String(), "error.param").String())
			require.Equal(t, message, gjson.Get(w.Body.String(), "error.message").String())
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Empty(t, repo.modelRateLimitCalls)
		})
	}
}

func m6Stream(t *testing.T, pass bool, body string) (*httptest.ResponseRecorder, *OpenAIUsage, bool, bool, error) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	account := newOpenAIRejectedFieldTestAccount()
	if pass {
		result, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "client-model", "mapped-model")
		if result == nil {
			return w, nil, false, false, err
		}
		return w, result.usage, result.TerminalSeen, result.TerminalValid, err
	}
	result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "client-model", "mapped-model")
	if result == nil {
		return w, nil, false, false, err
	}
	return w, result.usage, result.TerminalSeen, result.TerminalValid, err
}

func TestM6MalformedTerminal(t *testing.T) {
	prefix := m6Frame(`{"type":"response.created","response":{"id":"r1","created_at":123}}`) + m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`) + m6Frame(`{"type":"response.usage","usage":{"input_tokens":13,"output_tokens":5,"input_tokens_details":{"cached_tokens":3}}}`)
	for _, pass := range []bool{false, true} {
		for _, bad := range []string{
			"event: response.completed\ndata: \xff\n\n",
			m6Frame(`{"type":"response.completed","response":`),
			m6Frame(`{"type":"response.completed"}`),
			m6Frame(`{"type":"response.completed","response":{"status":"failed"}}`),
			m6Frame(`{"type":"response.output_text.delta","delta":`),
			"event: response.completed\ndata: {\"type\":\"response.created\"}\ndata: {\"type\":\n\n",
		} {
			t.Run(fmt.Sprintf("pass=%t/%q", pass, bad), func(t *testing.T) {
				w, usage, _, valid, err := m6Stream(t, pass, prefix+bad)
				require.Error(t, err)
				require.False(t, valid)
				require.NotNil(t, usage)
				require.Equal(t, 13, usage.InputTokens)
				require.Equal(t, 5, usage.OutputTokens)
				require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed"), w.Body.String())
				require.NotContains(t, w.Body.String(), "event: response.completed")
				terminal, _, payload := extractOpenAISSETerminalEvent(w.Body.String())
				require.True(t, terminal.Failure)
				require.Equal(t, "r1", gjson.GetBytes(payload, "response.id").String())
				require.Equal(t, int64(13), gjson.GetBytes(payload, "response.usage.input_tokens").Int())
				require.Equal(t, int64(3), gjson.GetBytes(payload, "response.usage.input_tokens_details.cached_tokens").Int())
			})
		}
		for _, validBody := range []string{
			"event: response.completed\ndata: {\ndata: \"response\":{\"id\":\"r1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":13,\"output_tokens\":5}}}\n\n",
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":13,\"output_tokens\":5}}}",
		} {
			_, _, seen, valid, err := m6Stream(t, pass, validBody)
			require.NoError(t, err)
			require.True(t, seen)
			require.True(t, valid)
		}
	}
}

func TestM6DoneStatus(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, status := range []string{"completed", "incomplete", "failed", "cancelled", "canceled", ""} {
			t.Run(fmt.Sprintf("pass=%t/%s", pass, status), func(t *testing.T) {
				response := `{"id":"r1","status":"` + status + `","output":[],"usage":{"input_tokens":13,"output_tokens":5}}`
				body := m6Frame(`{"type":"response.done","response":` + response + `}`)
				_, usage, seen, valid, err := m6Stream(t, pass, body)
				if status == "completed" || status == "incomplete" {
					require.NoError(t, err)
					require.True(t, seen)
					require.True(t, valid)
					require.Equal(t, 13, usage.InputTokens)
					require.Equal(t, 5, usage.OutputTokens)
					_, _, err = m6Nonstream(t, pass, body, "text/event-stream")
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
		for _, names := range [][2]string{{"response.done", "response.completed"}, {"response.completed", "response.done"}} {
			body := "event: " + names[0] + "\n" + m6Frame(`{"type":"`+names[1]+`","response":{"status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5}}}`)
			_, usage, _, valid, err := m6Stream(t, pass, body)
			require.NoError(t, err)
			require.True(t, valid)
			require.Equal(t, 13, usage.InputTokens)
		}
		text := m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`)
		meter := m6Frame(`{"type":"response.usage","usage":{"input_tokens":0,"output_tokens":0}}`)
		for _, tc := range []struct {
			body string
			good bool
		}{
			{m6Frame(`{"type":"response.output_text.done","text":"answer"}`) + m6Frame("[DONE]"), true},
			{text + meter, true}, {text + meter + text, false},
			{text + m6Frame(`{"type":"response.usage","usage":{"input_tokens":13}}`), false},
			{text + meter + m6Frame(`{"type":"response.usage","usage":`), false},
		} {
			_, _, seen, valid, err := m6Stream(t, pass, tc.body)
			if tc.good {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.False(t, seen)
			require.False(t, valid)
		}
	}
}

func TestM6UntypedError(t *testing.T) {
	completed := m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5}}}`)
	prefix := m6Frame(`{"type":"response.created","response":{"id":"r1","created_at":123}}`) + m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`) + m6Frame(`{"type":"response.usage","usage":{"input_tokens":13,"output_tokens":5}}`)
	for _, code := range []int{401, 403, 429, 503} {
		payload := fmt.Sprintf(`{"error":{"code":%d}}`, code)
		terminal, _, raw := extractOpenAISSETerminalEvent(m6Frame(payload) + completed)
		require.True(t, terminal.Failure)
		require.True(t, terminal.Valid)
		require.Zero(t, terminal.WrappedStatus)
		require.Equal(t, int64(code), gjson.GetBytes(raw, "error.code").Int())
		require.Equal(t, 502, openAIStreamFailureStatus([]byte(payload), ""))
		for _, pass := range []bool{false, true} {
			t.Run(fmt.Sprintf("code=%d/pass=%t", code, pass), func(t *testing.T) {
				w, usage, _, _, err := m6Stream(t, pass, prefix+m6Frame(payload)+completed)
				require.Error(t, err)
				require.Equal(t, 13, usage.InputTokens)
				require.Equal(t, 5, usage.OutputTokens)
				require.NotContains(t, w.Body.String(), `"status":"completed"`)
				require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed"), w.Body.String())
				_, _, failedPayload := extractOpenAISSETerminalEvent(w.Body.String())
				require.Equal(t, gjson.String, gjson.GetBytes(failedPayload, "response.error.code").Type)
				require.Equal(t, fmt.Sprint(code), gjson.GetBytes(failedPayload, "response.error.code").String())
				_, _, err = m6Nonstream(t, pass, prefix+m6Frame(payload)+completed, "text/event-stream")
				require.Error(t, err)
			})
		}
	}
	for _, payload := range []string{`{"error":null}`, `{"error":{}}`, `{"type":"response.output_text.delta","delta":"error: provider quoted text"}`} {
		for _, pass := range []bool{false, true} {
			_, _, _, _, err := m6Stream(t, pass, m6Frame(payload)+completed)
			require.NoError(t, err)
			_, _, err = m6Nonstream(t, pass, m6Frame(payload)+completed, "text/event-stream")
			require.NoError(t, err)
		}
	}
}

func TestM6UsageEvidence(t *testing.T) {
	terminal := func(usage string) string {
		return m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]` + usage + `}}`)
	}
	for _, pass := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, body, source, kind string
				present, seen            bool
				input                    int
			}{
				{"missing", `{"id":"r1","output":[]}`, "missing", "json", false, false, 0},
				{"null", `{"id":"r1","usage":null}`, "missing", "json", false, false, 0},
				{"empty", `{"id":"r1","usage":{}}`, "missing", "json", false, false, 0},
				{"zero", `{"id":"r1","usage":{"input_tokens":0,"output_tokens":0}}`, "upstream_json", "json", true, false, 0},
				{"sse_missing", terminal(""), "missing", "sse", false, true, 0},
				{"sse_zero", terminal(`,"usage":{"input_tokens":0,"output_tokens":0}`), "upstream_sse_terminal", "sse", true, true, 0},
				{"progressive", m6Frame(`{"type":"response.usage","usage":{"input_tokens":13,"output_tokens":5}}`) + terminal(""), "upstream_sse_progressive", "sse", true, true, 13},
				{"zero_keeps_progressive", m6Frame(`{"type":"response.usage","usage":{"input_tokens":13,"output_tokens":5}}`) + terminal(`,"usage":{"input_tokens":0,"output_tokens":0}`), "upstream_sse_progressive", "sse", true, true, 13},
			} {
				if stream && tc.kind != "sse" {
					continue
				}
				t.Run(fmt.Sprintf("%s/pass=%t/stream=%t", tc.name, pass, stream), func(t *testing.T) {
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					st := resilience.NewRequestState()
					st.EnableGatewayRequest(time.Now())
					a := newOpenAIRejectedFieldTestAccount()
					st.NoteGatewaySend(a.ID)
					ctx := resilience.WithRequestState(context.Background(), st)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
					svc := &OpenAIGatewayService{cfg: &config.Config{}}
					var known, present, seen, valid bool
					var source, kind string
					var usage *OpenAIUsage
					if stream {
						if pass {
							r, e := svc.handleStreamingResponsePassthrough(ctx, resp, c, a, time.Now(), "gpt-test", "gpt-test")
							require.NoError(t, e)
							require.NotNil(t, r)
							known, present, source, seen, valid, kind, usage = r.ResponseFactsKnown, r.UsagePresent, r.UsageSource, r.TerminalSeen, r.TerminalValid, r.ResponseBodyKind, r.usage
						} else {
							r, e := svc.handleStreamingResponse(ctx, resp, c, a, time.Now(), "gpt-test", "gpt-test")
							require.NoError(t, e)
							require.NotNil(t, r)
							known, present, source, seen, valid, kind, usage = r.ResponseFactsKnown, r.UsagePresent, r.UsageSource, r.TerminalSeen, r.TerminalValid, r.ResponseBodyKind, r.usage
						}
					} else if pass {
						r, e := svc.handleNonStreamingResponsePassthrough(ctx, resp, c, a, "gpt-test", "gpt-test")
						require.NoError(t, e)
						require.NotNil(t, r)
						known, present, source, seen, valid, kind, usage = r.ResponseFactsKnown, r.UsagePresent, r.UsageSource, r.TerminalSeen, r.TerminalValid, r.ResponseBodyKind, r.usage
					} else {
						r, e := svc.handleNonStreamingResponse(ctx, resp, c, a, "gpt-test", "gpt-test")
						require.NoError(t, e)
						require.NotNil(t, r)
						known, present, source, seen, valid, kind, usage = r.ResponseFactsKnown, r.UsagePresent, r.UsageSource, r.TerminalSeen, r.TerminalValid, r.ResponseBodyKind, r.usage
					}
					require.True(t, known)
					require.Equal(t, tc.present, present)
					require.Equal(t, tc.source, source)
					require.Equal(t, tc.seen, seen)
					require.Equal(t, tc.seen, valid)
					require.Equal(t, tc.kind, kind)
					require.Equal(t, tc.input, usage.InputTokens)
					trace := reflect.ValueOf(st).Elem().FieldByName("gatewayRecord").Elem()
					// M8 freezes these facts into the public snapshot in its later commit.
					require.False(t, trace.FieldByName("UsageObserved").IsNil())
					require.Equal(t, tc.present, trace.FieldByName("UsageObserved").Elem().Bool())
					require.Equal(t, tc.source, trace.FieldByName("UsageSource").Elem().String())
					require.Equal(t, tc.kind, trace.FieldByName("ResponseBodyType").Elem().String())
				})
			}
		}
	}
	// A failure before a result exists must still leave authenticated body facts.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	st := resilience.NewRequestState()
	st.EnableGatewayRequest(time.Now())
	a := newOpenAIRejectedFieldTestAccount()
	st.NoteGatewaySend(a.ID)
	ctx := resilience.WithRequestState(context.Background(), st)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	r, err := (&OpenAIGatewayService{cfg: &config.Config{}}).handleNonStreamingResponse(ctx, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("invalid"))}, c, a, "gpt-test", "gpt-test")
	require.Error(t, err)
	require.Nil(t, r)
	trace := reflect.ValueOf(st).Elem().FieldByName("gatewayRecord").Elem()
	require.False(t, trace.FieldByName("UsageObserved").IsNil())
	require.False(t, trace.FieldByName("UsageObserved").Elem().Bool())
	require.Equal(t, "", trace.FieldByName("ResponseBodyType").Elem().String())
	for _, pass := range []bool{false, true} {
		t.Run(fmt.Sprintf("unobserved/pass=%t", pass), func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: passthroughErrReadCloser{err: io.ErrUnexpectedEOF}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			a := newOpenAIRejectedFieldTestAccount()
			if pass {
				r, e := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, a, time.Now(), "m", "m")
				require.Error(t, e)
				require.NotNil(t, r)
				require.False(t, r.ResponseFactsKnown)
				require.Empty(t, r.ResponseBodyKind)
			} else {
				r, e := svc.handleStreamingResponse(c.Request.Context(), resp, c, a, time.Now(), "m", "m")
				require.Error(t, e)
				require.NotNil(t, r)
				require.False(t, r.ResponseFactsKnown)
				require.Empty(t, r.ResponseBodyKind)
			}
		})
	}

}

func TestM6NonstreamMissingTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, pass := range []bool{false, true} {
		for _, edge := range []bool{false, true} {
			for _, tc := range []struct {
				name, body, code string
				input            int
			}{
				{"empty", "", OpenAIUpstreamStreamTruncatedCode, 0},
				{"created", m6Frame(`{"type":"response.created","response":{"id":"r1"}}`), OpenAIUpstreamStreamTruncatedCode, 0},
				{"delta", m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`), OpenAIUpstreamStreamTruncatedCode, 0},
				{"done_sentinel", m6Frame(`{"type":"response.output_text.done","text":"answer"}`) + m6Frame(`[DONE]`), OpenAIUpstreamStreamTruncatedCode, 0},
				{"usage_only", m6Frame(`{"type":"response.usage","usage":{"input_tokens":13,"output_tokens":5}}`), OpenAIUpstreamStreamTruncatedCode, 13},
				{"malformed", m6Frame(`{"type":"response.completed"}`), OpenAIUpstreamProtocolErrorCode, 0},
				{"valid_missing_usage", m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`), "", 0},
			} {
				t.Run(fmt.Sprintf("%s/pass=%t/edge=%t", tc.name, pass, edge), func(t *testing.T) {
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					st := resilience.NewRequestState()
					if edge {
						st.SetEdge("cf")
					}
					ctx := resilience.WithRequestState(context.Background(), st)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
					InstallDeliveryTracking(c, time.Now())
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Length": {"99999"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
					svc := &OpenAIGatewayService{cfg: &config.Config{}}
					a := newOpenAIRejectedFieldTestAccount()
					var err error
					var usage *OpenAIUsage
					if pass {
						r, e := svc.handleNonStreamingResponsePassthrough(ctx, resp, c, a, "m", "m")
						err = e
						require.NotNil(t, r)
						usage = r.usage
						require.True(t, r.ResponseFactsKnown)
					} else {
						r, e := svc.handleNonStreamingResponse(ctx, resp, c, a, "m", "m")
						err = e
						require.NotNil(t, r)
						usage = r.usage
						require.True(t, r.ResponseFactsKnown)
					}
					require.Equal(t, tc.input, usage.InputTokens)
					require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
					require.NotEqual(t, "99999", w.Header().Get("Content-Length"))
					if tc.code == "" {
						require.NoError(t, err)
						require.Equal(t, 200, w.Code)
					} else {
						require.Error(t, err)
						want := 502
						if edge {
							want = 503
							require.Equal(t, "502", w.Header().Get("X-XY-Upstream-Status"))
						}
						require.Equal(t, want, w.Code)
						require.Equal(t, "server_error", gjson.Get(w.Body.String(), "error.type").String())
						require.Equal(t, tc.code, gjson.Get(w.Body.String(), "error.code").String())
						before := w.Body.String()
						_ = svc.writeOpenAINonStreamingProtocolError(resp, c, "second", tc.code)
						require.Equal(t, before, w.Body.String())
					}
				})
			}
		}
	}
}

func TestM6FailureRecoveryHint(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, pass := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				http, nested, ms string
				seconds          int
			}{
				{"2", "30", "90000", 90}, {"0", "5", "0", 5}, {"0", "0", "0", 0},
			} {
				t.Run(fmt.Sprintf("pass=%t/stream=%t/delay=%d", pass, stream, tc.seconds), func(t *testing.T) {
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					st := resilience.NewRequestState()
					ctx := resilience.WithRequestState(context.Background(), st)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
					a := newNonStreamingFailoverAccount()
					payload := []byte(fmt.Sprintf(`{"type":"response.failed","response":{"id":"r1","status":"failed","error":{"type":"server_error","message":"temporary failure","headers":{"retry-after":%q,"retry-after-ms":%s}},"usage":{"input_tokens":13,"output_tokens":5}}}`, tc.nested, tc.ms))
					body := m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`) + m6Frame(string(payload)) + m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Retry-After": {tc.http}}, Body: io.NopCloser(strings.NewReader(body))}
					svc := newNonStreamingFailoverService()
					var err error
					if stream {
						if pass {
							_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, a, time.Now(), "m", "m")
						} else {
							_, err = svc.handleStreamingResponse(ctx, resp, c, a, time.Now(), "m", "m")
						}
					} else if pass {
						_, err = svc.handleNonStreamingResponsePassthrough(ctx, resp, c, a, "m", "m")
					} else {
						_, err = svc.handleNonStreamingResponse(ctx, resp, c, a, "m", "m")
					}
					require.Error(t, err)
					hint := st.UpstreamRecoveryHint()
					if tc.seconds == 0 {
						require.Nil(t, hint)
						return
					}
					require.NotNil(t, hint)
					require.Equal(t, time.Duration(tc.seconds)*time.Second, hint.Delay)
					require.Equal(t, hint.ObservedAt.Add(hint.Delay), hint.NotBefore)
					accountHint, ok := st.AccountRetryNotBefore(a.ID)
					require.True(t, ok)
					require.Equal(t, hint.NotBefore, accountHint.NotBefore)
					for _, wrapped := range []bool{false, true} {
						var failure *UpstreamFailoverError
						if wrapped {
							failure = svc.newOpenAIWrappedStreamFailoverError(ctx, c, a, pass, "rid", payload, "temporary failure", "m", resp.Header, 503)
						} else {
							failure = svc.newOpenAIStreamFailoverError(c, a, pass, "rid", payload, "temporary failure", resp.Header)
						}
						require.NotNil(t, failure)
						require.NotNil(t, failure.UpstreamRecoveryHint)
						require.Equal(t, hint.NotBefore, failure.UpstreamRecoveryHint.NotBefore)
					}
					local := openAIResponseFailureError(ctx, 503, "server_error", "server_error", "temporary failure")
					want := tc.seconds
					if want > 10 {
						want = 10
					}
					require.Equal(t, fmt.Sprint(want), local["headers"].(map[string]string)["retry-after"])
					require.Equal(t, hint.NotBefore, st.UpstreamRecoveryHint().NotBefore)
					resilience.SetRetryAdvice(ctx, resilience.RetryAdvice{Suppress: true})
					require.NotContains(t, openAIResponseFailureError(ctx, 503, "server_error", "server_error", "temporary failure"), "headers")
					st.SetUpstreamRecoveryHint(nil) // The next send boundary is owned by M8.
					noteOpenAIResponsesRecoveryHint(c, a, http.Header{}, []byte(`{"error":{"code":503}}`), time.Now())
					require.Nil(t, st.UpstreamRecoveryHint())
				})
			}
		}
	}
	// Both M3 early returns retain the already parsed raw hint.
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("early/wrapped=%t", wrapped), func(t *testing.T) {
			req := newWebSearchReplayRequest(t)
			svc := &OpenAIGatewayService{}
			a := webSearchReplayAccount(false)
			st := resilience.NewRequestState()
			req.ctx = resilience.WithRequestState(req.ctx, st)
			req.c.Request = req.c.Request.WithContext(req.ctx)
			r := armOpenAIRequestRepair(req.c, a, nil)
			r.m3 = svc.armOpenAIM3(req.c, a, []byte(m3TwoItemRequest), "fixture-version")
			r.m3.state.replaced[openAIM3SlotID{}] = struct{}{}
			payload := []byte(`{"error":{"type":"invalid_request_error","message":"shape rejected","headers":{"retry-after":30}}}`)
			noteOpenAIResponsesRecoveryHint(req.c, a, nil, payload, time.Now())
			var failure *UpstreamFailoverError
			if wrapped {
				failure = svc.newOpenAIWrappedStreamFailoverError(req.ctx, req.c, a, false, "rid", payload, "shape rejected", "m", nil, 400)
			} else {
				failure = svc.newOpenAIStreamFailoverError(req.c, a, false, "rid", payload, "shape rejected")
			}
			require.NotNil(t, failure)
			require.NotNil(t, failure.UpstreamRecoveryHint)
			require.Equal(t, 30*time.Second, failure.UpstreamRecoveryHint.Delay)
		})
	}
}

func TestM6PassthroughFinalEffort(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			body := []byte(`{"model":"gpt-6.1-sol","input":"hi","stream":` + fmt.Sprint(partial) + `,"reasoning":{"effort":"low"}}`)
			ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "", []ReasoningEffortMapping{{From: "low", To: "medium", Model: "gpt-6.1-sol", MatchType: "exact"}, {From: "medium", To: "high"}}, "")
			c := newOpenAIRejectedFieldTestContext(body)
			c.Request = c.Request.WithContext(ctx)
			a := newOpenAIRejectedFieldTestAccount()
			a.Extra["openai_passthrough"] = true
			payload := `{"id":"r1","output":[],"usage":{"input_tokens":13,"output_tokens":5}}`
			ct := "application/json"
			if partial {
				ct = "text/event-stream"
				payload = m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`) + m6Frame(`{"type":"response.failed","response":{"id":"r1","status":"failed","error":{"type":"server_error","message":"late failure"},"usage":{"input_tokens":13,"output_tokens":5}}}`)
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(400, `{"error":{"code":"unsupported_parameter","param":"metadata","message":"Unsupported parameter: metadata"}}`), {StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(payload))}}}
			body = []byte(strings.TrimSuffix(string(body), "}") + `,"metadata":{"tag":"fixture"}}`)
			result, err := newOpenAIRejectedFieldTestService(upstream).Forward(ctx, c, a, body)
			if partial {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			require.Equal(t, partial, result.PartialFailure)
			require.Len(t, upstream.bodies, 2)
			for _, wire := range upstream.bodies {
				require.Equal(t, "medium", gjson.GetBytes(wire, "reasoning.effort").String())
			}
			require.NotNil(t, result.ReasoningEffort)
			require.Equal(t, "medium", *result.ReasoningEffort)
			require.Equal(t, "low", *RequestedReasoningEffortFromContext(ctx))
			require.Equal(t, 13, result.Usage.InputTokens)
		})
	}
}

type m6CloseFuncBody struct {
	io.Reader
	close func()
}

func (b m6CloseFuncBody) Close() error {
	if b.close != nil {
		b.close()
	}
	return nil
}

func TestM6PassthroughRejectedFieldRecord(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cap, buildFail bool
		sends          int
		resent         bool
	}{
		{"resent", false, false, 2, true}, {"budget_denied", true, false, 1, false}, {"build_failed", false, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.5","input":"hi","metadata":{"tag":"fixture"},"max_output_tokens":20}`)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			c := newOpenAIRejectedFieldTestContext(body)
			c.Request = c.Request.WithContext(ctx)
			a := newOpenAIRejectedFieldTestAccount()
			a.Extra["openai_passthrough"] = true
			if tc.cap {
				c.Set(openAIResponsesRejectedFieldRetryBudgetContextKey, &openAIResponsesRejectedFieldRetryBudget{attempts: maxOpenAIResponsesRejectedFieldRetries})
			}
			response := newOpenAIRejectedFieldTestResponse(400, `{"error":{"code":"unsupported_parameter","param":"max_output_tokens","message":"Unsupported parameter: max_output_tokens"}}`)
			if tc.buildFail {
				response.Body = m6CloseFuncBody{Reader: response.Body, close: func() { a.Credentials["base_url"] = "://invalid" }}
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{response, newOpenAIRejectedFieldTestResponse(200, `{"id":"r1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}}
			_, err := newOpenAIRejectedFieldTestService(upstream).Forward(ctx, c, a, body)
			if tc.resent {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Len(t, upstream.bodies, tc.sends)
			value, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events := value.([]*OpsUpstreamErrorEvent)
			require.Len(t, events, 1)
			require.Equal(t, "max_output_tokens", events[0].RejectedField)
			require.Equal(t, tc.resent, events[0].Resent)
		})
	}
}

func TestM6TerminalWriteClaim(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, pass := range []bool{false, true} {
		t.Run(fmt.Sprintf("pass=%t", pass), func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			InstallDeliveryTracking(c, time.Now())
			body := m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			a := newOpenAIRejectedFieldTestAccount()
			if pass {
				_, err := svc.handleStreamingResponsePassthrough(ctx, resp, c, a, time.Now(), "m", "m")
				require.NoError(t, err)
			} else {
				_, err := svc.handleStreamingResponse(ctx, resp, c, a, time.Now(), "m", "m")
				require.NoError(t, err)
			}
			require.True(t, ProtocolTerminalClaimed(c))
			before := w.Body.String()
			require.False(t, WriteTargetProtocolError(c, PreOutputProtocolResponses, 502, "server_error", "failed", "", "late"))
			require.Equal(t, before, w.Body.String())
		})
	}
	t.Run("compact_success", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil)
		MarkOpenAICompactClientStream(c)
		require.True(t, writeOpenAICompactSSEBridge(c, 200, []byte(`{"id":"r1","output":[{"type":"compaction","encrypted_content":"fixture"}]}`)))
		require.True(t, ProtocolTerminalClaimed(c))
		before := w.Body.String()
		writeOpenAICompactSSEFailureMessage(c, 502, "server_error", "late")
		require.Equal(t, before, w.Body.String())
	})
	t.Run("heartbeat_failure", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		st := resilience.NewRequestState()
		st.NoteTurn(1, "client-model", "")
		c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil).WithContext(resilience.WithRequestState(context.Background(), st))
		InstallDeliveryTracking(c, time.Now())
		c.Header("Content-Type", "text/event-stream")
		n, err := c.Writer.Write([]byte(": heartbeat\n\n"))
		require.NoError(t, err)
		require.True(t, NoteDeliveryHeartbeat(c, n))
		writeOpenAICompactSSEFailureMessage(c, 502, "server_error", "failed")
		var frames [][]byte
		forEachOpenAIResponsesFrame(w.Body.String(), func(_ string, data []byte) { frames = append(frames, data) })
		require.Len(t, frames, 2)
		require.Equal(t, "response.created", gjson.GetBytes(frames[0], "type").String())
		require.Equal(t, "response.failed", gjson.GetBytes(frames[1], "type").String())
		require.Equal(t, gjson.GetBytes(frames[0], "response.id").String(), gjson.GetBytes(frames[1], "response.id").String())
		require.Equal(t, int64(1), gjson.GetBytes(frames[1], "sequence_number").Int())
	})
}

func TestM6ReviewGenericTerminalEventNames(t *testing.T) {
	for _, event := range []string{"message", "error", "vendor.event"} {
		for _, pass := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/pass=%t/stream=%t", event, pass, stream), func(t *testing.T) {
					body := m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`) + "event: " + event + "\n" + m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5}}}`)
					if stream {
						w, u, seen, valid, err := m6Stream(t, pass, body)
						require.NoError(t, err)
						require.True(t, seen)
						require.True(t, valid)
						require.Equal(t, 13, u.InputTokens)
						require.Equal(t, 5, u.OutputTokens)
						require.NotContains(t, w.Body.String(), `"type":"response.failed"`)
					} else {
						w, u, err := m6Nonstream(t, pass, body, "text/event-stream")
						require.NoError(t, err)
						require.Equal(t, 200, w.Code)
						require.Equal(t, 13, u.InputTokens)
						require.Equal(t, 5, u.OutputTokens)
					}
				})
			}
		}
	}
}

func TestM6ReviewOAuthTypedErrorRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, untyped := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/untyped=%t", stream, untyped), func(t *testing.T) {
				payload := `{"type":"error","error":{"code":"transient","message":"retrying"}}`
				if untyped {
					payload = `{"error":{"code":503}}`
				}
				body := m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`) + m6Frame(payload) + m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5}}}`)
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
				a := newOpenAIRejectedFieldTestAccount()
				a.Type = AccountTypeOAuth
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
				var usage *OpenAIUsage
				var err error
				if stream {
					r, e := svc.handleStreamingResponse(ctx, resp, c, a, time.Now(), "m", "m")
					require.NotNil(t, r)
					usage, err = r.usage, e
				} else {
					r, e := svc.handleNonStreamingResponse(ctx, resp, c, a, "m", "m")
					require.NotNil(t, r)
					usage, err = r.usage, e
				}
				require.Equal(t, 13, usage.InputTokens)
				require.Equal(t, 5, usage.OutputTokens)
				if untyped {
					require.Error(t, err)
					require.NotContains(t, w.Body.String(), `"status":"completed"`)
					if stream {
						require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed"))
					}
				} else {
					require.NoError(t, err)
					require.Contains(t, w.Body.String(), `"status":"completed"`)
					require.NotContains(t, w.Body.String(), `"type":"response.failed"`)
					require.Nil(t, st.UpstreamRecoveryHint())
				}
			})
		}
	}
}

func TestM6ReviewAdjacentDataLines(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("pass=%t/stream=%t", pass, stream), func(t *testing.T) {
				body := "data: " + `{"type":"response.output_text.delta","delta":"answer"}` + "\ndata: " + `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5}}}` + "\n\n"
				if stream {
					w, u, seen, valid, err := m6Stream(t, pass, body)
					require.NoError(t, err)
					require.True(t, seen)
					require.True(t, valid)
					require.Contains(t, w.Body.String(), "answer")
					require.Contains(t, w.Body.String(), "response.completed")
					require.Equal(t, 13, u.InputTokens)
					require.Equal(t, 5, u.OutputTokens)
				} else {
					w, u, err := m6Nonstream(t, pass, body, "text/event-stream")
					require.NoError(t, err)
					require.Equal(t, 200, w.Code)
					require.Equal(t, 13, u.InputTokens)
					require.Equal(t, 5, u.OutputTokens)
				}
			})
		}
	}
}

func TestM6ReviewFailoverResultIsNil(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("pass=%t/stream=%t", pass, stream), func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.5","input":"hi","stream":` + fmt.Sprint(stream) + `}`)
				a := newOpenAIRejectedFieldTestAccount()
				a.Extra["openai_passthrough"] = pass
				payload := m6Frame(`{"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"server_is_overloaded","message":"overloaded"},"usage":{"input_tokens":1200,"output_tokens":0}}}`)
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}}
				c := newOpenAIRejectedFieldTestContext(body)
				st := resilience.NewRequestState()
				st.EnableGatewayRequest(time.Now())
				ctx := resilience.WithRequestState(c.Request.Context(), st)
				c.Request = c.Request.WithContext(ctx)
				r, err := newOpenAIRejectedFieldTestService(upstream).Forward(ctx, c, a, body)
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure)
				require.Nil(t, r)
				require.False(t, c.Writer.Written())
				trace := reflect.ValueOf(st).Elem().FieldByName("gatewayRecord").Elem()
				require.True(t, trace.FieldByName("UsageObserved").Elem().Bool())
				require.True(t, trace.FieldByName("TerminalEventValid").Elem().Bool())
			})
		}
	}
}

func TestM6ReviewFailedJSONIsNotSuccessfulUsage(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			for _, usage := range []string{"", `,"usage":{"input_tokens":1200,"output_tokens":0}`} {
				t.Run(fmt.Sprintf("pass=%t/compact=%t/usage=%t", pass, compact, usage != ""), func(t *testing.T) {
					body := []byte(`{"model":"gpt-5.5","input":"hi"}`)
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
					if compact {
						MarkOpenAICompactClientStream(c)
					}
					a := newOpenAIRejectedFieldTestAccount()
					a.Extra["openai_passthrough"] = pass
					responseBody := `{"id":"r1","status":"failed","error":{"code":"server_error","message":"synthetic","param":"input"}` + usage + `}`
					up := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(200, responseBody)}
					r, err := newOpenAIRejectedFieldTestService(up).Forward(c.Request.Context(), c, a, body)
					require.Error(t, err)
					require.Nil(t, r)
					require.Equal(t, http.StatusOK, w.Code)
					require.JSONEq(t, responseBody, w.Body.String())
				})
			}
		}
	}
}

type m6ReviewBudgetUpstream struct {
	httpUpstreamRecorder
	delayHeaders bool
}

func (u *m6ReviewBudgetUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	if u.delayHeaders {
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(1200 * time.Millisecond):
		}
	}
	body := m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	var reader io.ReadCloser = io.NopCloser(strings.NewReader(body))
	if !u.delayHeaders {
		gate := make(chan struct{})
		time.AfterFunc(1200*time.Millisecond, func() { close(gate) })
		reader = &stagedOpenAISSEReadCloser{segments: [][]byte{[]byte(body)}, gates: []<-chan struct{}{gate}}
	}
	u.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
	return u.httpUpstreamRecorder.Do(req, proxy, id, concurrency)
}
func TestM6ReviewPassthroughBudgetUsesLegacyEffort(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.Enabled = false })
	for _, headers := range []bool{false, true} {
		t.Run(fmt.Sprintf("headers=%t", headers), func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4-xhigh","input":"hi","stream":true}`)
			c := newOpenAIRejectedFieldTestContext(body)
			ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "low", nil, "")
			c.Request = c.Request.WithContext(ctx)
			a := newOpenAIRejectedFieldTestAccount()
			a.Extra["openai_passthrough"] = true
			up := &m6ReviewBudgetUpstream{delayHeaders: headers}
			svc := newOpenAIRejectedFieldTestService(nil)
			svc.httpUpstream = up
			svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
			svc.cfg.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds = 3
			r, err := svc.Forward(c.Request.Context(), c, a, body)
			require.NoError(t, err)
			require.NotNil(t, r)
			require.Nil(t, r.ReasoningEffort)
			require.Len(t, up.bodies, 1)
			require.False(t, gjson.GetBytes(up.lastBody, "reasoning.effort").Exists())
		})
	}
}

func TestM6ReviewOAuthRecoveryDoesNotUnlockPriorFailure(t *testing.T) {
	typed := m6Frame(`{"type":"error","error":{"code":"transient","message":"retrying"}}`)
	completed := m6Frame(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5}}}`)
	for _, failure := range []string{m6Frame(`{"error":{"code":503}}`) + typed, m6Frame(`{"type":"error","error":`)} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, failure), func(t *testing.T) {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				a := newOpenAIRejectedFieldTestAccount()
				a.Type = AccountTypeOAuth
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(m6Frame(`{"type":"response.output_text.delta","delta":"answer"}`) + failure + completed))}
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				var err error
				if stream {
					_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, a, time.Now(), "m", "m")
				} else {
					_, err = svc.handleNonStreamingResponse(c.Request.Context(), resp, c, a, "m", "m")
				}
				require.Error(t, err)
				require.NotContains(t, w.Body.String(), `"status":"completed"`)
			})
		}
	}
}

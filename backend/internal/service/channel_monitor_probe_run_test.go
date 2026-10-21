//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
	"github.com/stretchr/testify/require"
)

// probeSeen 假上游收到的一次请求。
type probeSeen struct {
	Path   string
	URI    string
	Header http.Header
	Body   map[string]any
}

// probeUpstream 假上游：按 handle 回答，并记录收到的请求。
type probeUpstream struct {
	mu     sync.Mutex
	seen   []probeSeen
	handle func(w http.ResponseWriter, r *http.Request, body map[string]any, n int)
}

func newProbeUpstream(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body map[string]any, n int)) (*probeUpstream, string) {
	t.Helper()
	swapMonitorHTTPClient(t)
	u := &probeUpstream{handle: handle}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	return u, srv.URL
}

func (u *probeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	u.mu.Lock()
	u.seen = append(u.seen, probeSeen{Path: r.URL.Path, URI: r.RequestURI, Header: r.Header.Clone(), Body: body})
	n := len(u.seen)
	u.mu.Unlock()
	u.handle(w, r, body, n)
}

func (u *probeUpstream) requests() []probeSeen {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]probeSeen(nil), u.seen...)
}

// probeTestAnswer 从任意协议的请求体里找出题目并算出答案。
func probeTestAnswer(body map[string]any) string {
	prompt, _ := body["input"].(string)
	if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
		if msg, ok := messages[0].(map[string]any); ok {
			prompt, _ = msg["content"].(string)
		}
	}
	if contents, ok := body["contents"].([]any); ok && len(contents) > 0 {
		first, _ := contents[0].(map[string]any)
		parts, _ := first["parts"].([]any)
		if len(parts) > 0 {
			part, _ := parts[0].(map[string]any)
			prompt, _ = part["text"].(string)
		}
	}
	return answerFromChallengePrompt(prompt)
}

func writeProbeSSE(w http.ResponseWriter, events ...string) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	for _, ev := range events {
		_, _ = fmt.Fprint(w, ev+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func chatDelta(text string) string {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
	return "data: " + string(b)
}

// probeAnswerEvents 各协议“正常回答 answer”的 SSE 事件。
func probeAnswerEvents(protocol probeProtocol, answer string) []string {
	switch protocol {
	case probeProtocolResponses:
		return []string{
			`event: response.created` + "\n" + `data: {"type":"response.created","response":{"status":"in_progress"}}`,
			`event: response.output_text.delta` + "\n" + `data: {"type":"response.output_text.delta","delta":"` + answer + `"}`,
			`event: response.output_text.done` + "\n" + `data: {"type":"response.output_text.done","text":"` + answer + `"}`,
			`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed"}}`,
		}
	case probeProtocolAnthropic:
		return []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"content":[]}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + answer + `"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
	case probeProtocolGemini:
		return []string{`data: {"candidates":[{"content":{"parts":[{"text":"` + answer + `"}],"role":"model"},"finishReason":"STOP"}]}`}
	default:
		return []string{chatDelta(answer), `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, "data: [DONE]"}
	}
}

func fastProbeLimits() probeLimits {
	return probeLimits{Budget: 3 * time.Second, DegradedAfter: 2 * time.Second, RetryMinRemaining: 0, FastFailWindow: time.Second}
}

func TestRunProbe_StreamsEveryProtocol(t *testing.T) {
	for _, tc := range []struct {
		provider string
		apiMode  string
		model    string
		protocol probeProtocol
		path     string
	}{
		{MonitorProviderOpenAI, "", "gpt-5.5", probeProtocolChat, "/v1/chat/completions"},
		{MonitorProviderOpenAI, MonitorAPIModeResponses, "gpt-6.1-sol", probeProtocolResponses, "/v1/responses"},
		{MonitorProviderGrok, "", "grok-4.5", probeProtocolChat, "/v1/chat/completions"},
		{MonitorProviderGrok, MonitorAPIModeResponses, "grok-4.6", probeProtocolResponses, "/v1/responses"},
		{MonitorProviderAnthropic, "", "claude-opus-5-5", probeProtocolAnthropic, "/v1/messages"},
		{MonitorProviderGemini, "", "gemini-2.5-flash", probeProtocolGemini, "/v1beta/models/gemini-2.5-flash:streamGenerateContent"},
		{MonitorProviderKimi, "", "kimi-k2.5", probeProtocolChat, "/v1/chat/completions"},
		{MonitorProviderZhipu, "", "glm-5.3-flash", probeProtocolChat, "/v1/chat/completions"},
		{MonitorProviderDeepseek, "", "deepseek-chat", probeProtocolChat, "/v1/chat/completions"},
		{MonitorProviderMiniMax, "", "MiniMax-M2", probeProtocolChat, "/v1/chat/completions"},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			up, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, _ int) {
				writeProbeSSE(w, probeAnswerEvents(tc.protocol, probeTestAnswer(body))...)
			})
			res := runProbe(context.Background(), tc.provider, endpoint, "key", tc.model,
				&CheckOptions{MonitorID: 42, APIMode: tc.apiMode}, fastProbeLimits())
			require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
			require.NotNil(t, res.LatencyMs)
			reqs := up.requests()
			require.Len(t, reqs, 1)
			require.Equal(t, tc.path, reqs[0].Path)
			require.Equal(t, "text/event-stream", reqs[0].Header.Get("Accept"))
			id, ok := monitorprobe.Verify(reqs[0].Header.Get(monitorprobe.HeaderName), "key", time.Now())
			require.True(t, ok, "probe header must verify")
			require.Equal(t, int64(42), id)
		})
	}
}

func TestRunProbe_NoSignatureWithoutMonitorID(t *testing.T) {
	up, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, _ int) {
		writeProbeSSE(w, probeAnswerEvents(probeProtocolChat, probeTestAnswer(body))...)
	})
	res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, fastProbeLimits())
	require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
	require.Empty(t, up.requests()[0].Header.Get(monitorprobe.HeaderName))
}

// TestRunProbe_StopsReadingAtAnswer 读到答案即停：上游在答案后继续慢慢输出，响应时间不受影响。
func TestRunProbe_StopsReadingAtAnswer(t *testing.T) {
	_, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any, _ int) {
		writeProbeSSE(w, chatDelta(probeTestAnswer(body)), chatDelta(" because"))
		for i := 0; i < 20; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
				writeProbeSSE(w, chatDelta(" and so on"))
			}
		}
		writeProbeSSE(w, "data: [DONE]")
	})
	limits := fastProbeLimits()
	limits.Budget = 10 * time.Second
	start := time.Now()
	res := runProbe(context.Background(), MonitorProviderGrok, endpoint, "key", "grok-4.5", nil, limits)
	require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
	require.Less(t, *res.LatencyMs, 1000)
	require.Less(t, time.Since(start), 1500*time.Millisecond, "the probe kept reading after the answer")
}

func TestRunProbe_DegradedAboveThreshold(t *testing.T) {
	_, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, _ int) {
		time.Sleep(150 * time.Millisecond)
		writeProbeSSE(w, probeAnswerEvents(probeProtocolChat, probeTestAnswer(body))...)
	})
	limits := fastProbeLimits()
	limits.DegradedAfter = 50 * time.Millisecond
	res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, limits)
	require.Equal(t, MonitorStatusDegraded, res.Status)
	require.Contains(t, res.Message, "slow response")
	require.GreaterOrEqual(t, *res.LatencyMs, 150)

	limits.DegradedAfter = 5 * time.Second
	res = runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, limits)
	require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
	require.Empty(t, res.Message)
}

func TestRunProbe_BudgetExhausted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prelude []string
		message string
	}{
		{"no response headers", nil, "no response headers"},
		{"heartbeats only", []string{": keepalive", "event: ping\ndata: {}"}, "no reply text"},
		{"reasoning only", []string{`data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}`}, "reasoning only"},
		{"partial text", []string{chatDelta("The answer is")}, "The answer is"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
				if tc.prelude != nil {
					writeProbeSSE(w, tc.prelude...)
				}
				<-r.Context().Done()
			})
			limits := fastProbeLimits()
			limits.Budget = 300 * time.Millisecond
			start := time.Now()
			res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, limits)
			require.Less(t, time.Since(start), 2*time.Second)
			require.Equal(t, MonitorStatusError, res.Status)
			require.Contains(t, res.Message, "timeout after 300ms")
			require.Contains(t, res.Message, tc.message)
			require.GreaterOrEqual(t, *res.LatencyMs, 300)
		})
	}
}

func TestRunProbe_FailureVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		write   func(w http.ResponseWriter, answer string)
		status  string
		message string
	}{
		{"in-stream error before DONE", func(w http.ResponseWriter, _ string) {
			writeProbeSSE(w, `data: {"error":{"message":"upstream overloaded","type":"server_error"}}`, "data: [DONE]")
		}, MonitorStatusError, "stream error: upstream overloaded"},
		{"empty reply", func(w http.ResponseWriter, _ string) {
			writeProbeSSE(w, `data: {"choices":[{"delta":{"role":"assistant"}}]}`, "data: [DONE]")
		}, MonitorStatusFailed, "empty reply"},
		{"reasoning only", func(w http.ResponseWriter, _ string) {
			writeProbeSSE(w, `data: {"choices":[{"delta":{"reasoning_content":"so"}}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`, "data: [DONE]")
		}, MonitorStatusFailed, "empty reply (reasoning only, stop: length)"},
		{"wrong answer", func(w http.ResponseWriter, answer string) {
			wrong, _ := strconv.Atoi(answer)
			writeProbeSSE(w, chatDelta(strconv.Itoa(wrong+1)), "data: [DONE]")
		}, MonitorStatusFailed, "challenge mismatch"},
		{"rate limited", func(w http.ResponseWriter, _ string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down sk-abcdefghijklmnopqrstuvwxyz"}}`))
		}, MonitorStatusError, "upstream HTTP 429"},
		{"HTML page with 200", func(w http.ResponseWriter, _ string) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>Just a moment...</body></html>"))
		}, MonitorStatusError, "unexpected non-JSON response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, _ int) {
				tc.write(w, probeTestAnswer(body))
			})
			res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-4o", nil, fastProbeLimits())
			require.Equal(t, tc.status, res.Status, res.Message)
			require.Contains(t, res.Message, tc.message)
			require.NotContains(t, res.Message, "sk-abcdefghijklmnopqrstuvwxyz")
			require.NotNil(t, res.LatencyMs)
			require.Len(t, up.requests(), 1, "these failures are not retried")
		})
	}
}

// TestRunProbe_ReadsJSONWhenUpstreamIgnoresStream 上游忽略 stream:true 回 JSON 时按 JSON 读。
func TestRunProbe_ReadsJSONWhenUpstreamIgnoresStream(t *testing.T) {
	_, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, _ int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":"%s"}}]}`, probeTestAnswer(body))
	})
	res := runProbe(context.Background(), MonitorProviderDeepseek, endpoint, "key", "deepseek-chat", nil, fastProbeLimits())
	require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
}

func TestRunProbe_ReplaceModeReadsByStreamFlag(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			up, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, _ int) {
				if body["stream"] == true {
					writeProbeSSE(w, chatDelta("ok"), "data: [DONE]")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			})
			userBody := map[string]any{"model": "gpt-5.5", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": stream}
			res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-5.5", &CheckOptions{
				BodyOverrideMode: MonitorBodyOverrideModeReplace, BodyOverride: userBody,
			}, fastProbeLimits())
			require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
			seen := up.requests()[0]
			require.Equal(t, userBody["stream"], seen.Body["stream"])
			require.NotContains(t, seen.Body, "reasoning_effort")
			require.NotContains(t, seen.Body, "max_tokens")
			if stream {
				require.Equal(t, "text/event-stream", seen.Header.Get("Accept"))
			} else {
				require.Equal(t, "application/json", seen.Header.Get("Accept"))
			}
		})
	}
}

func TestRunProbe_InvalidConfigFailsLocally(t *testing.T) {
	up, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		w.WriteHeader(http.StatusOK)
	})
	res := runProbe(context.Background(), MonitorProviderAnthropic, endpoint, "key", "claude-x",
		&CheckOptions{APIMode: MonitorAPIModeResponses}, fastProbeLimits())
	require.Equal(t, MonitorStatusError, res.Status)
	require.Empty(t, up.requests())
	require.True(t, strings.Contains(res.Message, "api_mode") || strings.Contains(res.Message, "responses"), res.Message)
}

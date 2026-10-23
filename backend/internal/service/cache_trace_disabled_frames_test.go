package service

// Baseline-compatible fixture (no P0a symbols). Disabled cache_trace with
// diagnostics on (production config); measures allocs/op of full streaming
// paths whose per-frame hooks convert string payloads to []byte.

import (
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
)

func traceAllocationSSE(frames int, raw bool) string {
	var b strings.Builder
	if raw {
		for i := 0; i < frames; i++ {
			_, _ = b.WriteString(`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"tok tok tok tok tok tok tok tok tok tok"}}]}` + "\n\n")
		}
		_, _ = b.WriteString(`data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":1024}}}` + "\n\ndata: [DONE]\n\n")
		return b.String()
	}
	_, _ = b.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
	for i := 0; i < frames; i++ {
		_, _ = b.WriteString("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"tok tok tok tok tok tok tok tok tok tok\"}\n\n")
	}
	_, _ = b.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":2000,\"input_tokens_details\":{\"cached_tokens\":1024},\"output_tokens\":1,\"output_tokens_details\":{\"reasoning_tokens\":0},\"total_tokens\":2001}}}\n\n")
	return b.String()
}

func BenchmarkCacheTraceDisabledFramePipelines(b *testing.B) {
	oldLog, oldSlog := log.Writer(), slog.Default()
	log.SetOutput(io.Discard)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	defer func() { log.SetOutput(oldLog); slog.SetDefault(oldSlog) }()
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		stream    bool
		raw       bool
		responses bool
	}{{"chat_stream", true, false, false}, {"chat_buffered", false, false, false}, {"rawcc_stream", true, true, false}, {"responses_via_cc", true, true, true}, {"responses_stream", true, false, true}, {"responses_buffered", false, false, true}} {
		b.Run(tc.name, func(b *testing.B) {
			body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"x"}],"stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] + `}`
			if tc.responses {
				body = strings.Replace(body, "\"messages\"", "\"input\"", 1)
			}
			sse := traceAllocationSSE(200, tc.raw)
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			up := &httpUpstreamRecorder{}
			s := &OpenAIGatewayService{cfg: cfg, httpUpstream: up}
			a := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": !tc.raw}}
			group := int64(10)
			run := func() {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, map[bool]string{false: "/v1/chat/completions", true: "/v1/responses"}[tc.responses], strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
				up.requests, up.bodies = nil, nil
				up.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}
				s.GenerateSessionHash(c, []byte(body))
				var res *OpenAIForwardResult
				var err error
				if tc.responses {
					res, err = s.Forward(c.Request.Context(), c, a, []byte(body))
				} else {
					res, err = s.ForwardAsChatCompletions(c.Request.Context(), c, a, []byte(body), "", "")
				}
				if err != nil || res == nil || res.Usage.CacheReadInputTokens != 1024 {
					b.Fatalf("pipeline failed: err=%v res=%v", err, res)
				}
			}
			run()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				run()
			}
		})
	}
}

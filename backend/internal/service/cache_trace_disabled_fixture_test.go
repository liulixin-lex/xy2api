//go:build unit

package service

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// This fixture deliberately uses only baseline APIs so the identical code can
// measure Forward -> usage billing on an unmodified baseline through Go overlay.
func traceDisabledPipeline(t testing.TB, protocol string, diagnostics bool) func() {
	t.Helper()
	oldLog, oldSlog, oldMode := log.Writer(), slog.Default(), gin.Mode()
	log.SetOutput(io.Discard)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { log.SetOutput(oldLog); slog.SetDefault(oldSlog); gin.SetMode(oldMode) })
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.cfg.JWT.Secret = "disabled-synthetic-secret"
	svc.cfg.Gateway.OpenAICache.DiagnosticsEnabled = diagnostics
	upstream := &httpUpstreamRecorder{}
	svc.httpUpstream = upstream
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": true}}
	api := &APIKey{ID: 1, Group: &Group{RateMultiplier: 1}}
	user := &User{ID: 2}
	path, body := "/v1/responses", `{"model":"gpt-5.1","input":[{"role":"user","content":"disabled-private-body"}],"stream":false}`
	response := `{"id":"resp_disabled","model":"gpt-5.1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2048,"output_tokens":1,"input_tokens_details":{"cached_tokens":1024}}}`
	contentType := "application/json"
	if protocol == "chat" {
		path, body = "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"disabled-private-body"}],"stream":false}`
		response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\ndata: [DONE]\n\n"
		contentType = "text/event-stream"
	}
	return func() {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("session_id", "disabled-private-header")
		c.Set("api_key", api)
		upstream.requests = nil
		upstream.bodies = nil
		upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}
		svc.GenerateSessionHash(c, []byte(body))
		var result *OpenAIForwardResult
		var err error
		if protocol == "chat" {
			result, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, []byte(body), svc.ExtractSessionID(c, []byte(body)), "")
		} else {
			result, err = svc.Forward(c.Request.Context(), c, account, []byte(body))
		}
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, diagnostics, result.CacheDiagnostic != nil)
		require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
		require.NoError(t, svc.RecordUsage(context.WithoutCancel(c.Request.Context()), &OpenAIRecordUsageInput{Result: result, APIKey: api, User: user, Account: account}))
		require.NotNil(t, usageRepo.lastLog)
		require.Equal(t, 1024, usageRepo.lastLog.CacheReadTokens)
	}
}

func TestCacheTraceDisabledPipelineSuccess(t *testing.T) {
	for _, protocol := range []string{"responses", "chat"} {
		for _, diagnostics := range []bool{false, true} {
			t.Run(protocol+"/diagnostics="+fmtBool(diagnostics), func(t *testing.T) { traceDisabledPipeline(t, protocol, diagnostics)() })
		}
	}
}

func BenchmarkCacheTraceDisabledPipeline(b *testing.B) {
	for _, protocol := range []string{"responses", "chat"} {
		for _, diagnostics := range []bool{false, true} {
			b.Run(protocol+"/diagnostics="+fmtBool(diagnostics), func(b *testing.B) {
				run := traceDisabledPipeline(b, protocol, diagnostics)
				run()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					run()
				}
			})
		}
	}
}

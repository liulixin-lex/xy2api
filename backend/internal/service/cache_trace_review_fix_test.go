package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func traceSuccessfulForward(t *testing.T, protocol, kind string, stream, eof bool) (*OpenAIGatewayService, *gin.Context, *OpenAIForwardResult) {
	t.Helper()
	field, path := "input", "/v1/responses"
	if protocol == "chat" {
		field, path = "messages", "/v1/chat/completions"
	}
	body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","%s":[{"role":"user","content":"trace-private-body"}],"stream":%t}`, field, stream))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("session-id", "trace-private-header")
	group := int64(10)
	c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	traceTestConfig(cfg)
	resp := traceForwardOK()
	if eof {
		resp.Body = io.NopCloser(strings.NewReader(strings.TrimRight(traceForwardSSE, "\n")))
	}
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{resp: resp}}
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": true}}
	if kind == "oauth" {
		account.Type = AccountTypeOAuth
		account.Credentials = map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}
	}
	s.GenerateSessionHash(c, body)
	var result *OpenAIForwardResult
	var err error
	if protocol == "chat" {
		result, err = s.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
	} else {
		result, err = s.Forward(c.Request.Context(), c, account, body)
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.CacheTrace)
	awaitCacheTrace(t, s, c, result)
	return s, c, result
}

func TestCacheTraceUsageThroughProductionResponseHandlers(t *testing.T) {
	for _, protocol := range []string{"responses", "chat"} {
		for _, kind := range []string{"apikey", "oauth"} {
			for _, stream := range []bool{false, true} {
				for _, eof := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/stream=%t/eof=%t", protocol, kind, stream, eof), func(t *testing.T) {
						_, _, result := traceSuccessfulForward(t, protocol, kind, stream, eof)
						require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
						require.Equal(t, uint8(1), result.CacheTrace.data.UsageFields&15, "presence must agree with parsed cached>0")
					})
				}
			}
		}
	}
}

func TestCacheTraceIdentifiersMatchExistingUsageEvent(t *testing.T) {
	s, _, result := traceSuccessfulForward(t, "chat", "apikey", false, false)
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	s.observeCacheUsage(result, &Account{ID: 9, Platform: PlatformOpenAI}, time.Now())
	result.CacheTrace.emitCacheTrace()
	events := map[string]map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var e map[string]any
		require.NoError(t, json.Unmarshal(line, &e))
		name, ok := e["msg"].(string)
		require.True(t, ok, "structured log event must have a string message")
		events[name] = e
	}
	require.Contains(t, events, "openai_cache_usage")
	require.Contains(t, events, "cache_trace")
	for _, field := range []string{"session_digest", "request_id_hmac"} {
		require.NotEmpty(t, events["cache_trace"][field])
		require.Equal(t, events["openai_cache_usage"][field], events["cache_trace"][field])
	}
}

func TestCacheTraceMessagesSuccessExcluded(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	for _, kind := range []string{"apikey", "oauth"} {
		c, _, _, _, result := traceExtraRun(t, traceExtraCase{Scenario: "messages_" + kind, Key: "none", Header: "session_id"}, traceTestConfig, traceForwardOK())
		require.NotNil(t, result, "successful Messages bridge response")
		require.Nil(t, cacheTraceFromGin(c))
		require.Nil(t, result.CacheTrace)
		result.CacheTrace.emitCacheTrace()
	}
	require.NotContains(t, logs.String(), `"msg":"cache_trace"`)
}

func TestCacheTraceInvalidReadValuesAreNotUsage(t *testing.T) {
	for _, value := range []string{"null", `"0"`, `"private-read"`, "false", "{}", "[]", "0", "1024"} {
		t.Run(value, func(t *testing.T) {
			_, c, _, _ := cacheTraceWireFixture(t, "responses", "apikey", "none", "none", traceTestConfig)
			observeCacheTraceUsage(c, []byte(`{"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":`+value+`}}}`))
			want := uint8(0)
			if value == "0" || value == "1024" {
				want = 1
			}
			require.Equal(t, want, cacheTraceFromGin(c).data.UsageFields&15)
		})
	}
}

func TestCacheTraceProductionPrivacyMarkers(t *testing.T) {
	for _, variant := range []string{"codex", "rich", "private"} {
		s, c, _, wire, _ := traceForwardRun(t, traceForwardCase{Proto: "responses", Kind: "oauth", Key: "trace-private-key", Header: "session_id", Variant: variant}, traceTestConfig)
		if variant == "private" {
			require.True(t, cacheTraceFromGin(c).data.ThreadPresent)
			require.Contains(t, string(wire), "client_metadata")
		}
		result := &OpenAIForwardResult{RequestID: "trace-private-request"}
		s.finishCacheTrace(c, result)
		require.NotNil(t, result.CacheTrace)
		var logs bytes.Buffer
		old := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
		result.CacheTrace.emitCacheTrace()
		slog.SetDefault(old)
		for _, marker := range []string{"trace-private", "synthetic-token", "synthetic-account", "trace-test-secret"} {
			require.NotContains(t, logs.String(), marker)
		}
		// Includes the account-rewritten PCK, which never appears in trace logs.
		rewritten := parseRawJSONView(wire).Get("prompt_cache_key").Str
		require.NotEmpty(t, rewritten)
		require.NotContains(t, logs.String(), rewritten)
	}
}

func TestCacheTraceExcludedIngresses(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/images/generations", "/v1/alpha/search", "/v1/responses"} {
		cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
		cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
		traceTestConfig(cfg)
		s := &OpenAIGatewayService{cfg: cfg}
		c := cacheTestContext(1, 10)
		c.Request.URL.Path = path
		if path == "/v1/responses" {
			SetOpenAIClientTransport(c, OpenAIClientTransportWS)
		}
		s.GenerateSessionHash(c, []byte(`{"model":"gpt-5.4","input":"trace-private-body"}`))
		require.Nil(t, cacheTraceFromGin(c))
	}
}

func BenchmarkCacheTraceEnabled200KB(b *testing.B) {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	traceTestConfig(cfg)
	s := &OpenAIGatewayService{cfg: cfg}
	c := cacheTestContext(1, 10)
	body := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"` + strings.Repeat("x", 200*1024) + `"}]}`)
	s.GenerateSessionHash(c, body)
	req, _ := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", bytes.NewReader(body))
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.captureCacheTraceRequest(c, req, account, "responses")
	}
}

const traceForwardSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":2000,\"input_tokens_details\":{\"cached_tokens\":1024},\"output_tokens\":1,\"output_tokens_details\":{\"reasoning_tokens\":0},\"total_tokens\":2001}}}\n\n"

func traceForwardOK() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-review"}}, Body: io.NopCloser(strings.NewReader(traceForwardSSE))}
}

func TestCacheTraceCompatOAuthWithoutNamespaceHasSource(t *testing.T) {
	_, c, _, body, _ := traceForwardRun(t, traceForwardCase{Proto: "chat", Kind: "oauth_no_namespace", Key: "none", Header: "none", Variant: "plain"}, traceTestConfig)
	require.NotEmpty(t, parseRawJSONView(body).Get("prompt_cache_key").Str)
	require.Equal(t, "gw_compat_hex", cacheTraceFromGin(c).data.PCKClassOut)
}

// Production Forward must actually resolve a seeded fingerprint. The legacy
// golden fixtures only select a mode without a seed, so convergence is inactive.
// turn_id is per-request random and is not a stable byte snapshot; source,
// session/header/body consistency, and privacy are asserted on the real output.
func TestCacheTraceActiveFingerprintProduction(t *testing.T) {
	const seed = "8cfa43f4-f819-4c98-a643-08a04259e5c7"
	for _, mode := range []string{"fp_session_oauth", "fp_full_oauth"} {
		for _, key := range []string{"none", "trace-private-key"} {
			for _, header := range []string{"none", "session_id", "session-id"} {
				t.Run(mode+"/"+key+"/"+header, func(t *testing.T) {
					var previousWire string
					for _, enabled := range []bool{false, true} {
						configure := func(cfg *config.Config) { traceTestConfig(cfg); cfg.Gateway.CacheTrace.Enabled = enabled }
						c, wire, body, headers, _ := traceExtraRun(t, traceExtraCase{Scenario: mode, Key: key, Header: header}, configure, nil, func(a *Account) { a.Extra[codexFingerprintSeedExtraKey] = seed })
						ids := stagedCodexFingerprintIDs(c, &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
						require.NotNil(t, ids, "seeded convergence must be active")
						require.NotEmpty(t, ids.sessionID)
						normalized := strings.ReplaceAll(wire, ids.turnID, strings.Repeat("0", len(ids.turnID)))
						stamp := strconv.FormatInt(ids.turnStartedAtUnixMs, 10)
						normalized = strings.ReplaceAll(normalized, stamp, strings.Repeat("0", len(stamp)))
						if enabled {
							require.Equal(t, previousWire, normalized, "seeded convergence must preserve normalized wire bytes")
						} else {
							previousWire = normalized
						}
						require.Equal(t, ids.sessionID, headers.Get("session_id"))
						require.Equal(t, ids.sessionID, headers.Get("session-id"))
						require.Equal(t, ids.sessionID, parseRawJSONView(body).Get("client_metadata.session_id").Str)
						trace := cacheTraceFromGin(c)
						if !enabled {
							require.Nil(t, trace)
							continue
						}
						require.NotNil(t, trace)
						require.Equal(t, "fingerprint_converged", trace.data.SessOutRelation)
						var logs bytes.Buffer
						old := slog.Default()
						slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
						trace.emitCacheTrace()
						slog.SetDefault(old)
						for _, private := range []string{seed, ids.sessionID, ids.turnID, "trace-private"} {
							require.NotContains(t, logs.String(), private)
						}
					}
				})
			}
		}
	}
}

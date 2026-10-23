package service

// Adapted from reviewer R: raw CC, fingerprint
// convergence and the Messages bridge, plus 200-OK usage paths.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type traceExtraCase struct{ Scenario, Key, Header string }

func (rc traceExtraCase) name() string { return rc.Scenario + "/" + rc.Key + "/" + rc.Header }

func traceExtraCases() []traceExtraCase {
	var out []traceExtraCase
	for _, sc := range []string{"rawcc_apikey", "fp_session_oauth", "fp_full_oauth", "messages_apikey", "messages_oauth"} {
		for _, key := range []string{"none", "trace-private-key"} {
			for _, h := range []string{"none", "session_id", "session-id"} {
				out = append(out, traceExtraCase{sc, key, h})
			}
		}
	}
	return out
}

func traceExtraBody(rc traceExtraCase) (string, string) {
	pck := ""
	if rc.Key != "none" {
		enc, _ := json.Marshal(rc.Key)
		pck = `,"prompt_cache_key":` + string(enc)
	}
	switch {
	case strings.HasPrefix(rc.Scenario, "rawcc"):
		return "/v1/chat/completions", `{"model":"gpt-5.4","messages":[{"role":"user","content":"trace-private-body"}],"stream":false` + pck + `}`
	case strings.HasPrefix(rc.Scenario, "messages"):
		return "/v1/messages", `{"model":"gpt-5.4","max_tokens":16,"system":"trace-private-sys","messages":[{"role":"user","content":"trace-private-body"}],"metadata":{"user_id":"trace-private-user"}}`
	default:
		return "/v1/responses", `{"model":"gpt-5.4","input":[{"role":"user","content":"trace-private-body"}],"stream":false` + pck + `}`
	}
}

func traceExtraRun(t *testing.T, rc traceExtraCase, configure func(*config.Config), resp *http.Response, configureAccount ...func(*Account)) (*gin.Context, string, []byte, http.Header, *OpenAIForwardResult) {
	t.Helper()
	path, body := traceExtraBody(rc)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if rc.Header != "none" {
		c.Request.Header.Set(rc.Header, "trace-private-header")
	}
	if strings.HasPrefix(rc.Scenario, "fp_") {
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.120.0 (Linux; x86_64) xterm")
		c.Request.Header.Set("originator", "codex_cli_rs")
	}
	group := int64(10)
	c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
	if resp == nil {
		resp = &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"synthetic stop"}}`))}
	}
	upstream := &httpUpstreamRecorder{resp: resp}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	if configure != nil {
		configure(cfg)
	}
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	a := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": true}}
	if strings.HasSuffix(rc.Scenario, "_oauth") {
		a.Type = AccountTypeOAuth
		a.Credentials = map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}
	}
	switch rc.Scenario {
	case "rawcc_apikey":
		a.Extra["openai_responses_supported"] = false
	case "fp_session_oauth":
		a.Extra["codex_fingerprint_mode"] = "session"
	case "fp_full_oauth":
		a.Extra["codex_fingerprint_mode"] = "full"
	}
	for _, configure := range configureAccount {
		configure(a)
	}
	s.GenerateSessionHash(c, []byte(body))
	var result *OpenAIForwardResult
	func() {
		switch {
		case strings.HasPrefix(rc.Scenario, "rawcc"):
			result, _ = s.ForwardAsChatCompletions(c.Request.Context(), c, a, []byte(body), s.ExtractSessionID(c, []byte(body)), "")
		case strings.HasPrefix(rc.Scenario, "messages"):
			result, _ = s.ForwardAsAnthropic(c.Request.Context(), c, a, []byte(body), s.ExtractSessionID(c, []byte(body)), "")
		default:
			result, _ = s.Forward(c.Request.Context(), c, a, []byte(body))
		}
	}()
	require.NotEmpty(t, upstream.requests, "production forward path must send an upstream request")
	if len(upstream.requests) == 0 {
		return c, "NO_UPSTREAM_REQUEST", nil, nil, result
	}
	req := upstream.requests[0]
	wire := upstream.bodies[0]
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(wire))
	dump, err := httputil.DumpRequestOut(clone, true)
	require.NoError(t, err)
	awaitCacheTrace(t, s, c, result)
	return c, string(dump), wire, req.Header, result
}

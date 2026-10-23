package service

// Adapted from reviewer R: exercise the production Forward /
// ForwardAsChatCompletions / passthrough paths and dumps the first upstream
// request, so baseline and P0a wire bytes can be compared end to end.

import (
	"bytes"
	"encoding/json"
	"fmt"
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

type traceForwardCase struct{ Proto, Kind, Key, Header, Variant string }

func (rc traceForwardCase) name() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", rc.Proto, rc.Kind, rc.Key, rc.Header, rc.Variant)
}

func traceForwardCases() []traceForwardCase {
	var out []traceForwardCase
	for _, proto := range []string{"responses", "chat"} {
		for _, kind := range []string{"apikey", "oauth", "apikey_pt", "oauth_pt"} {
			for _, key := range []string{"none", "", "a63114b0-6f55-4bf9-a6a2-fbf1cce72317", "trace-private-key"} {
				for _, header := range []string{"none", "session_id", "session-id", "x-session-id", "conversation_id"} {
					for _, variant := range []string{"plain", "codex", "rich"} {
						out = append(out, traceForwardCase{proto, kind, key, header, variant})
					}
				}
			}
		}
	}
	return out
}

func traceForwardBody(rc traceForwardCase) string {
	var b strings.Builder
	if rc.Proto == "chat" {
		b.WriteString(`{"model":"gpt-5.4","messages":[`)
		if rc.Variant == "rich" {
			b.WriteString(`{"role":"system","content":"trace-private-sys"},`)
		}
		b.WriteString(`{"role":"user","content":"trace-private-body"}]`)
		if rc.Variant == "rich" {
			b.WriteString(`,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{}}}}]`)
		}
	} else {
		b.WriteString(`{"model":"gpt-5.4"`)
		if rc.Variant == "rich" {
			b.WriteString(`,"instructions":"trace-private-sys","tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{}}}],"reasoning":{"effort":"high"}`)
		}
		b.WriteString(`,"input":[{"role":"user","content":"trace-private-body"}]`)
	}
	if rc.Variant == "private" {
		b.WriteString(`,"instructions":"trace-private-instructions","client_metadata":{"thread_id":"trace-private-metadata"}`)
	}
	b.WriteString(`,"stream":false`)
	if rc.Key != "none" {
		enc, _ := json.Marshal(rc.Key)
		b.WriteString(`,"prompt_cache_key":` + string(enc))
	}
	b.WriteString("}")
	return b.String()
}

func traceForwardRun(t *testing.T, rc traceForwardCase, configure func(*config.Config)) (*OpenAIGatewayService, *gin.Context, string, []byte, http.Header) {
	t.Helper()
	body := traceForwardBody(rc)
	path := "/v1/responses"
	if rc.Proto == "chat" {
		path = "/v1/chat/completions"
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if rc.Header != "none" {
		c.Request.Header.Set(rc.Header, "trace-private-header")
	}
	if rc.Variant == "codex" || rc.Variant == "private" {
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.120.0 (Linux; x86_64) xterm")
		c.Request.Header.Set("originator", "codex_cli_rs")
		c.Request.Header.Set("thread-id", "trace-private-thread")
	}
	group := int64(10)
	c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"synthetic stop"}}`))}}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	if configure != nil {
		configure(cfg)
	}
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	a := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": true}}
	if strings.HasPrefix(rc.Kind, "oauth") {
		a.Type = AccountTypeOAuth
		a.Credentials = map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}
	}
	if rc.Kind == "oauth_no_namespace" {
		delete(a.Credentials, "chatgpt_account_id")
	}
	if strings.HasSuffix(rc.Kind, "_pt") {
		a.Extra["openai_passthrough"] = true
	}
	s.GenerateSessionHash(c, []byte(body))
	func() {
		if rc.Proto == "chat" {
			_, _ = s.ForwardAsChatCompletions(c.Request.Context(), c, a, []byte(body), s.ExtractSessionID(c, []byte(body)), "")
		} else {
			_, _ = s.Forward(c.Request.Context(), c, a, []byte(body))
		}
	}()
	require.NotEmpty(t, upstream.requests, "production forward path must send an upstream request")
	if len(upstream.requests) == 0 {
		return s, c, "NO_UPSTREAM_REQUEST", nil, nil
	}
	req := upstream.requests[0]
	wire := upstream.bodies[0]
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(wire))
	dump, err := httputil.DumpRequestOut(clone, true)
	require.NoError(t, err)
	return s, c, string(dump), wire, req.Header
}

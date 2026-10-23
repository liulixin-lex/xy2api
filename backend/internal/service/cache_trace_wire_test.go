package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Snapshots were captured from e369fd3bbfd577de766689f5c6df3b76c43a35f1
// before adding observation hooks. All credentials and content are synthetic.
func cacheTraceWireFixture(t *testing.T, protocol, kind, key, header string, configure func(*OpenAIGatewayService)) (*OpenAIGatewayService, *gin.Context, *http.Request, []byte) {
	t.Helper()
	body := `{"model":"gpt-5.4","input":[{"role":"user","content":"trace-private-body"}],"stream":false`
	path := "/v1/responses"
	if protocol == "chat" {
		body = `{"model":"gpt-5.4","messages":[{"role":"user","content":"trace-private-body"}],"stream":false`
		path = "/v1/chat/completions"
	}
	if key != "none" {
		encoded, _ := json.Marshal(key)
		body += `,"prompt_cache_key":` + string(encoded)
	}
	body += "}"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if header != "none" {
		c.Request.Header.Set(header, "trace-private-header")
	}
	group := int64(10)
	c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"synthetic stop"}}`))}}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	if configure != nil {
		configure(s)
	}
	a := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": true}}
	if kind == "oauth" {
		a.Type = AccountTypeOAuth
		a.Credentials = map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}
	}
	s.GenerateSessionHash(c, []byte(body))
	var req *http.Request
	var err error
	if protocol == "chat" {
		_, err = s.ForwardAsChatCompletions(c.Request.Context(), c, a, []byte(body), s.ExtractSessionID(c, []byte(body)), "")
		require.Error(t, err)
		req = upstream.lastReq
	} else if kind == "passthrough" {
		req, err = s.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, a, []byte(body), "synthetic-token")
		require.NoError(t, err)
	} else {
		req, err = s.buildUpstreamRequest(c.Request.Context(), c, a, []byte(body), "synthetic-token", false, s.ExtractSessionID(c, []byte(body)), false)
		require.NoError(t, err)
	}
	require.NotNil(t, req)
	r, err := req.GetBody()
	require.NoError(t, err)
	wire, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	awaitCacheTrace(t, s, c, nil)
	return s, c, req, wire
}

func cacheTraceWireSnapshot(t *testing.T, req *http.Request, body []byte) string {
	t.Helper()
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	snapshot, err := httputil.DumpRequestOut(clone, true)
	require.NoError(t, err)
	return string(snapshot)
}

func TestCacheTraceWireGolden(t *testing.T) {
	golden := map[string]string{}
	const path = "testdata/cache_trace_wire_e369fd3bb.json"
	{
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &golden))
	}
	for _, protocol := range []string{"responses", "chat"} {
		for _, kind := range []string{"apikey", "oauth", "passthrough"} {
			for _, key := range []string{"none", "", "a63114b0-6f55-4bf9-a6a2-fbf1cce72317", "trace-private-key"} {
				for _, header := range []string{"none", "session_id", "session-id", "x-session-id", "x-session-affinity"} {
					name := fmt.Sprintf("%s/%s/%s/%s", protocol, kind, key, header)
					t.Run(name, func(t *testing.T) {
						_, _, req, body := cacheTraceWireFixture(t, protocol, kind, key, header, nil)
						actual := cacheTraceWireSnapshot(t, req, body)
						require.Equal(t, golden[name], actual)
					})
				}
			}
		}
	}
}

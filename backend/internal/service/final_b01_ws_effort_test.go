//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFinalB01WSLegacyBillingEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "gpt-5.4-xhigh"
	payload := []byte(`{"type":"response.create","model":"gpt-5.4-xhigh","input":"hi"}`)
	const completed = `{"type":"response.completed","response":{"id":"resp_b01","model":"gpt-5.4-xhigh","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	check := func(t *testing.T, result *OpenAIForwardResult, wire []byte, want string, multiplier float64) {
		t.Helper()
		require.NotNil(t, result)
		effort := optionalStringValue(result.ReasoningEffort)
		got := reasoningEffortBillingMultiplier(effort, map[string]float64{"xhigh": 2})
		t.Logf("wire=%s effort=%q multiplier=%g ws=%v", wire, effort, got, result.OpenAIWSMode)
		require.Equal(t, want, effort)
		require.Equal(t, multiplier, got)
		require.False(t, gjson.GetBytes(wire, "reasoning.effort").Exists())
	}
	t.Run("http_bridge", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + completed + "\n\n"))}}
		svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		account := &Account{ID: 5881, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "fixture-token", payload, len(payload), model, "", "", "", "", 1, func([]byte) error { return nil })
		require.NoError(t, err)
		check(t, result, upstream.lastBody, "xhigh", 2)
	})
	t.Run("native_ingress", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cfg := passthroughLifecycleConfig()
		cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
		cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
		cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 10
		cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 10
		upstream := &openAIWSCaptureConn{events: [][]byte{[]byte(completed)}}
		svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
		svc.openaiWSPool = newOpenAIWSConnPool(cfg)
		svc.openaiWSPool.setClientDialerForTest(&stagedPassthroughDialer{conn: upstream})
		defer svc.openaiWSPool.Close()
		account := passthroughLifecycleAccount()
		account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
		results := make(chan *OpenAIForwardResult, 1)
		turnErrors := make(chan error, 1)
		server, _ := startPassthroughHookRecordingServer(t, ctx, svc, account, &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) { results <- result; turnErrors <- err }})
		defer server.Close()
		client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		require.NoError(t, err)
		defer func() { _ = client.CloseNow() }()
		require.NoError(t, client.Write(ctx, coderws.MessageText, payload))
		_, _, err = client.Read(ctx)
		require.NoError(t, err)
		select {
		case result := <-results:
			require.NoError(t, <-turnErrors)
			upstream.mu.Lock()
			wire, marshalErr := json.Marshal(upstream.writes[0])
			upstream.mu.Unlock()
			require.NoError(t, marshalErr)
			check(t, result, wire, "xhigh", 2)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	})
	t.Run("http_e29", func(t *testing.T) {
		withResilienceSettings(t, nil)
		body := []byte(`{"model":"gpt-5.4-xhigh","input":"hi","stream":true}`)
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + completed + "\n\n"))}}
		svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://fixture.invalid"}}
		result, err := svc.Forward(context.Background(), c, account, body)
		require.NoError(t, err)
		check(t, result, upstream.lastBody, "", 1)
	})
}

//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCapabilityMarksWebSocketForwardersWithoutSampling(t *testing.T) {
	for _, ingress := range []bool{false, true} {
		t.Run(map[bool]string{false: "http_to_ws", true: "native_ingress"}[ingress], func(t *testing.T) {
			cfg := newOpenAIWSExecutionScopeTestConfig()
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
			account := capabilityTestAccount(1)
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			conn := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.failed","response":{"id":"fixture-response","status":"failed","error":{"code":"MODEL_UNSUPPORTED","message":"synthetic refusal"}}}`)}}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn})
			defer pool.Close()
			svc := &OpenAIGatewayService{cfg: cfg, accountRepo: repo, cache: &stubGatewayCache{}, httpUpstream: &httpUpstreamRecorder{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, openaiWSStateStore: NewOpenAIWSStateStore(nil)}
			state := resilience.NewRequestState()
			ctx := resilience.WithCapabilityRequest(resilience.WithRequestState(context.Background(), state), "responses", true)
			if !ingress {
				c, _ := compatStreamContext("/v1/responses", `{"model":"synthetic-model","input":"fixture","stream":true}`)
				c.Request = c.Request.WithContext(ctx)
				_, _ = svc.forwardOpenAIWSV2(ctx, c, account, map[string]any{"model": "synthetic-model", "input": "fixture", "stream": true}, "", "", "test", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, true, "synthetic-model", "synthetic-model", time.Now(), 1, "", nil)
			} else {
				done := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					client, err := coderws.Accept(w, r, nil)
					if err != nil {
						done <- err
						return
					}
					defer func() { _ = client.CloseNow() }()
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = r.WithContext(ctx)
					c.Set("api_key", &APIKey{ID: 7})
					done <- svc.ProxyResponsesWebSocketFromClient(ctx, c, client, account, "test", []byte(`{"type":"response.create","model":"synthetic-model","input":"fixture"}`), nil)
				}))
				defer server.Close()
				client := dialOpenAIWSExecutionScopeClient(t, server.URL, "fixture-thread")
				readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _, _ = client.Read(readCtx)
				cancel()
				_ = client.CloseNow()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("ingress did not finish")
				}
			}
			fresh, err := repo.GetByID(ctx, 1)
			require.NoError(t, err)
			require.NotNil(t, fresh.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()))
			require.Nil(t, state.CurrentAttempt(), "learning must not allocate an otherwise disabled quality capture")
		})
	}
}

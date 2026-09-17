package service

import (
	"context"
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

func TestGroupSystemPromptWebSocketPassthroughMultiTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(WithGroupSystemPrompt(context.Background(), GroupSystemPromptConfig{Prompt: "common", ModelPrompts: map[string]string{"second": "specific"}}, ""))
	defer cancel()
	upstream := newStagedPassthroughConn()
	hooks := &OpenAIWSIngressHooks{InitialRequestModel: "alias", MapRequestModel: func(_ int, _ string) (string, error) { return "gpt-5.1", nil }}
	server, _ := startPassthroughHookRecordingServer(t, ctx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount(), hooks)
	defer server.Close()
	client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"alias","instructions":"client","input":"hi"}`)
	defer client.CloseNow()
	for turn, want := range []string{"common\n\nclient", "specific\n\nclient", "common\n\nclient", "specific\n\nclient"} {
		if turn > 0 {
			if turn == 3 {
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"session.update","session":{"model":"second"}}`)))
				require.Equal(t, "session.update", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, 3*time.Second), "type").String())
			}
			payload := `{"type":"response.create","model":"second","instructions":"client","input":"again"}`
			if turn >= 2 {
				payload = `{"type":"response.create","instructions":"client","input":"again"}`
			}
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(payload)))
		}
		wire := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
		require.Equal(t, want, gjson.GetBytes(wire, "instructions").String())
		upstream.Send(`{"type":"response.completed","response":{"id":"resp_policy","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
		_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
		require.NoError(t, err)
	}
}

func TestGroupSystemPromptWebSocketHTTPFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sse := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_policy\",\"model\":\"mapped\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, httpUpstream: upstream}
	account := &Account{ID: 5881, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	payload := []byte(`{"type":"response.create","model":"mapped","instructions":"client","input":"hi"}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx := WithGroupSystemPrompt(context.Background(), GroupSystemPromptConfig{ModelPrompts: map[string]string{"alias": "admin"}}, "")
	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c, account, "test-token", payload, len(payload), "alias", "", "", "", "", 1, func([]byte) error { return nil })
	require.NoError(t, err)
	require.Equal(t, "admin\n\nclient", gjson.GetBytes(upstream.lastBody, "instructions").String())
}

func TestGroupSystemPromptFinalNativeBuilders(t *testing.T) {
	ctx := WithGroupSystemPrompt(context.Background(), GroupSystemPromptConfig{ModelPrompts: map[string]string{"alias": "admin"}}, "alias")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: kind}
		svc := &GatewayService{cfg: &config.Config{}}
		body := []byte(`{"model":"mapped","system":"client","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
		req, wire, err := svc.buildUpstreamRequest(ctx, c, account, body, "test", "apikey", "mapped", false, false)
		require.NoError(t, err)
		actual, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, wire, actual)
		require.Equal(t, "admin\n\nclient", gjson.GetBytes(actual, "system").String())
		count, _, err := svc.buildCountTokensRequest(ctx, c, account, body, "test", "apikey", "mapped", false)
		require.NoError(t, err)
		actual, err = io.ReadAll(count.Body)
		require.NoError(t, err)
		require.Equal(t, "admin\n\nclient", gjson.GetBytes(actual, "system").String())
		openai := &OpenAIGatewayService{cfg: &config.Config{}}
		account.Platform = PlatformOpenAI
		response, err := openai.buildUpstreamRequest(ctx, c, account, []byte(`{"model":"mapped","instructions":"client","input":"hello"}`), "test", false, "", false)
		require.NoError(t, err)
		actual, err = io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, "admin\n\nclient", gjson.GetBytes(actual, "instructions").String())
	}
}

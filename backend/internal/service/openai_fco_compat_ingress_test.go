package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIFCOCompatWSTurnSnapshotRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, _, ok := OpenAIFCOCompatWSTurnSnapshot(nil)
	require.False(t, ok)
	setOpenAIFCOCompatWSTurnSnapshot(nil, 1, OpenAIFCOCompatSnapshot{})

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	_, _, ok = OpenAIFCOCompatWSTurnSnapshot(c)
	require.False(t, ok)
	setOpenAIFCOCompatWSTurnSnapshot(c, 4, OpenAIFCOCompatSnapshot{IngressHasDuplicateMembers: true})
	snapshot, turn, ok := OpenAIFCOCompatWSTurnSnapshot(c)
	require.True(t, ok)
	require.Equal(t, 4, turn)
	require.True(t, snapshot.IngressHasDuplicateMembers)
}

func TestWithOpenAIFCOCompatIngressSnapshotUsesCurrentSettingsAndRawBody(t *testing.T) {
	saved := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(saved) })
	resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeProactive, "alpha_name"))

	raw := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"a","output":"b"}]}`)
	ctx := WithOpenAIFCOCompatIngressSnapshot(context.Background(), raw)
	snapshot, ok := OpenAIFCOCompatSnapshotFromContext(ctx)
	require.True(t, ok)
	require.Same(t, resilience.Current(), snapshot.Settings)
	require.Equal(t, []string{"alpha_name"}, snapshot.Rules.AdminNames)
	require.Equal(t, resilience.FCOCompatModeProactive, snapshot.GlobalMode())
	require.True(t, snapshot.IngressHasDuplicateMembers)
}

// Every WebSocket turn binds to the settings at the time its own frame was
// read and to that frame's bytes. The HTTP bridge send of the turn and the
// handler's per-turn audit (BeforeRequest) see the same snapshot.
func TestOpenAIWSHTTPBridgeTakesFCOCompatSnapshotPerTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	saved := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(saved) })
	resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeReactive, "alpha_name"))

	completed := func(id string) *http.Response {
		body := `data: {"type":"response.completed","response":{"id":"` + id + `","model":"gpt-5.1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{completed("resp_fco_1"), completed("resp_fco_2"), completed("resp_fco_3")}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	svc := &OpenAIGatewayService{
		cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID: 9301, Name: "fco-ws-bridge", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"}, Extra: map[string]any{"responses_websockets_v2_enabled": true},
		Concurrency: 1, Status: StatusActive, Schedulable: true,
	}

	type audited struct {
		turn, published int
		snapshot        OpenAIFCOCompatSnapshot
		ok              bool
	}
	var mu sync.Mutex
	var audits []audited
	var firstSnapshot OpenAIFCOCompatSnapshot
	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r.Clone(r.Context())
		// The handler stores the first frame's snapshot before its audit.
		ctx := WithOpenAIFCOCompatIngressSnapshot(r.Context(), firstMessage)
		mu.Lock()
		firstSnapshot, _ = OpenAIFCOCompatSnapshotFromContext(ctx)
		mu.Unlock()
		hooks := &OpenAIWSIngressHooks{BeforeRequest: func(turn int, _ []byte, _ string) error {
			snapshot, published, ok := OpenAIFCOCompatWSTurnSnapshot(ginCtx)
			mu.Lock()
			audits = append(audits, audited{turn: turn, published: published, snapshot: snapshot, ok: ok})
			mu.Unlock()
			return nil
		}}
		errCh <- svc.ProxyResponsesWebSocketFromClient(ctx, ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()
	turn := func(payload string) {
		t.Helper()
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
		cancelWrite()
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	}

	turn(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"first turn"}`)
	resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeReactive, "beta_name"))
	// The duplicate member must survive in the flag even though the frame is
	// rebuilt several times before the bridge sends it. The scan only runs for
	// a body carrying both function_call_output and an "agent" string.
	turn(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"function_call_output marker","metadata":{"source":"agent","source":"b"}}`)
	resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeProactive, "gamma_name"))
	turn(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"third turn"}`)
	resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeOff, "delta_name"))

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the websocket bridge to finish")
	}

	require.Len(t, upstream.requests, 3)
	sent := make([]OpenAIFCOCompatSnapshot, len(upstream.requests))
	for i, req := range upstream.requests {
		snapshot, ok := OpenAIFCOCompatSnapshotFromContext(req.Context())
		require.True(t, ok, "turn %d send carries a snapshot", i+1)
		sent[i] = snapshot
	}
	mu.Lock()
	defer mu.Unlock()
	require.Same(t, firstSnapshot.Settings, sent[0].Settings, "turn 1 is sent with the handler's snapshot")
	require.Equal(t, []string{"alpha_name"}, sent[0].Rules.AdminNames)
	require.Equal(t, []string{"beta_name"}, sent[1].Rules.AdminNames)
	require.Equal(t, []string{"gamma_name"}, sent[2].Rules.AdminNames)
	require.Equal(t, resilience.FCOCompatModeProactive, sent[2].GlobalMode())
	require.Equal(t, []bool{false, true, false}, []bool{
		sent[0].IngressHasDuplicateMembers, sent[1].IngressHasDuplicateMembers, sent[2].IngressHasDuplicateMembers,
	})

	require.Len(t, audits, 2, "BeforeRequest runs once for every later turn")
	for i, audit := range audits {
		require.Equal(t, i+2, audit.turn)
		require.True(t, audit.ok)
		require.Equal(t, audit.turn, audit.published, "the published snapshot belongs to the turn being admitted")
		require.Same(t, sent[audit.turn-1].Settings, audit.snapshot.Settings, "audit and send of a turn share one snapshot")
		require.Equal(t, sent[audit.turn-1].IngressHasDuplicateMembers, audit.snapshot.IngressHasDuplicateMembers)
	}
}

// A later turn that fails over is resent by a new forwarder attempt that
// treats the resent payload as its first frame and does not audit it again.
// The handler re-enters with the published snapshot of the failed turn, so the
// resend keeps the snapshot the turn was audited with.
func TestOpenAIWSHTTPBridgeCurrentTurnFailoverKeepsTurnFCOCompatSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	saved := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(saved) })
	resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeReactive, "alpha_name"))

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	sse := func(body string) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		sse("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_first\",\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"inspect\",\"arguments\":\"{}\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"),
		{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"60"}},
			Body: io.NopCloser(strings.NewReader(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`))},
		sse("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_second\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"),
	}}
	svc := &OpenAIGatewayService{
		cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID: 9311, Name: "limited", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Extra:       map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeHTTPBridge},
		Credentials: map[string]any{"chatgpt_account_id": "account-a", "chatgpt_user_id": "user-a"},
	}
	nextAccount := *account
	nextAccount.ID = 9312
	nextAccount.Credentials = map[string]any{"chatgpt_account_id": "account-b", "chatgpt_user_id": "user-b"}

	var failedTurn OpenAIFCOCompatSnapshot
	var failedTurnNo int
	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r.Clone(r.Context())
		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		ctx := WithOpenAIFCOCompatIngressSnapshot(r.Context(), firstMessage)
		proxyErr := svc.ProxyResponsesWebSocketFromClient(ctx, ginCtx, conn, account, "access-token-a", firstMessage, nil)
		retryPayload, retryCurrentTurn := OpenAIWSCurrentTurnRetryPayload(proxyErr)
		if !retryCurrentTurn || len(retryPayload) == 0 {
			serverErrCh <- errors.New("missing current-turn retry payload")
			return
		}
		snapshot, turn, ok := OpenAIFCOCompatWSTurnSnapshot(ginCtx)
		if !ok {
			serverErrCh <- errors.New("no published turn snapshot")
			return
		}
		failedTurn, failedTurnNo = snapshot, turn
		// A reload while the handler reselects must not reach the resend.
		resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeProactive, "gamma_name"))
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(WithOpenAIFCOCompatSnapshot(ctx, snapshot), ginCtx, conn, &nextAccount, "access-token-b", retryPayload, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancel()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()
	roundTrip := func(payload string) []byte {
		t.Helper()
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
		cancelWrite()
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		return event
	}
	roundTrip(`{"type":"response.create","model":"gpt-5.6-sol","input":[{"role":"user","content":"first"}]}`)
	resilience.Publish(*fcoModerationSettings(resilience.FCOCompatModeReactive, "beta_name"))
	event := roundTrip(`{"type":"response.create","model":"gpt-5.6-sol","previous_response_id":"resp_first","input":[{"type":"function_call_output","namespace":"agent","call_id":"call_1","output":"second","output":"second"}]}`)
	require.Equal(t, "resp_second", gjson.GetBytes(event, "response.id").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the websocket bridge to finish")
	}

	require.Equal(t, 2, failedTurnNo)
	require.Equal(t, []string{"beta_name"}, failedTurn.Rules.AdminNames)
	require.True(t, failedTurn.IngressHasDuplicateMembers)
	require.Len(t, upstream.requests, 3)
	for i, want := range []string{"alpha_name", "beta_name", "beta_name"} {
		snapshot, ok := OpenAIFCOCompatSnapshotFromContext(upstream.requests[i].Context())
		require.True(t, ok)
		require.Equal(t, []string{want}, snapshot.Rules.AdminNames, "send %d", i+1)
	}
	resent, _ := OpenAIFCOCompatSnapshotFromContext(upstream.requests[2].Context())
	require.Same(t, failedTurn.Settings, resent.Settings, "the resend keeps the failed turn's snapshot")
	require.True(t, resent.IngressHasDuplicateMembers, "the rebuilt retry payload cannot clear the frame's duplicate flag")
}

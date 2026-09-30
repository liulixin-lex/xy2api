package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func nativeWSFixtureService(t *testing.T, baseURL string) (*OpenAIGatewayService, *Account, *httpUpstreamRecorder) {
	t.Helper()
	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	upstream := &httpUpstreamRecorder{}
	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := nativeRelayAccount()
	account.Status = StatusActive
	account.Schedulable = true
	account.Concurrency = 2
	account.Credentials = map[string]any{"api_key": "test-fixture-only", "base_url": baseURL}
	account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	return svc, account, upstream
}

func TestNativeStreamWSRealSocketImmediateDelivery(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	var creates atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		creates.Add(1)
		_ = conn.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_ws_same\"}}"))
		select {
		case <-gate:
		case <-time.After(2 * time.Second):
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"response.vendor_progress\",\"sequence_number\":1,\"opaque\":\"retained\"}"))
		_ = conn.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_ws_same\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}"))
	}))
	defer upstream.Close()
	svc, account, httpTransport := nativeWSFixtureService(t, upstream.URL)
	results := make(chan error, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := nativeRelayContext(r.Context())
		c, _ := gin.CreateTestContext(w)
		c.Request = r.WithContext(ctx)
		_, err := svc.Forward(ctx, c, account, []byte("{\"model\":\"gpt-5.1\",\"stream\":true,\"input\":\"fixture\"}"))
		results <- err
	}))
	defer gateway.Close()
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	response, err := client.Get(gateway.URL)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	frame, err := nativeRelayReadEvent(reader)
	require.NoError(t, err)
	require.Contains(t, frame, "response.created")
	gate <- struct{}{}
	frame, err = nativeRelayReadEvent(reader)
	require.NoError(t, err)
	require.Contains(t, frame, "response.vendor_progress")
	frame, err = nativeRelayReadEvent(reader)
	require.NoError(t, err)
	require.Contains(t, frame, "response.completed")
	require.NoError(t, <-results)
	require.EqualValues(t, 1, creates.Load())
	require.Empty(t, httpTransport.requests)
	t.Log("WS created delivered before content release; unknown event retained; upstream create count=1; HTTP create count=0")
}

func TestNativeStreamWSSentWithoutEventDoesNotRegenerate(t *testing.T) {
	var creates atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		creates.Add(1)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, _ = conn.ReadMessage()
	}))
	defer upstream.Close()
	svc, account, httpTransport := nativeWSFixtureService(t, upstream.URL)
	ctx := nativeRelayContext(context.Background())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	_, err := svc.Forward(ctx, c, account, []byte("{\"model\":\"gpt-5.1\",\"stream\":true,\"input\":\"fixture\"}"))
	require.Error(t, err)
	var fallback *openAIWSFallbackError
	require.ErrorAs(t, err, &fallback)
	require.True(t, fallback.RequestSent)
	_, retryable := classifyOpenAIWSReconnectReason(err)
	require.False(t, retryable)
	require.EqualValues(t, 1, creates.Load())
	require.Empty(t, httpTransport.requests)
	t.Logf("send_certainty=sent_execution_unknown upstream_create_count=%d HTTP_create_count=0 error=%v", creates.Load(), err)
}

func TestNativeStreamWSPoolKeyIsolation(t *testing.T) {
	account := nativeRelayAccount()
	account.Credentials = map[string]any{"api_key": "fixture-A"}
	req := openAIWSAcquireRequest{NativeDelivery: true, Account: account, WSURL: "wss://fixture.test/v1/responses", ProxyURL: "http://proxy.test", Headers: http.Header{"Authorization": []string{"Bearer fixture-A"}, "Session-Id": []string{"session-A"}}}
	original := normalizeOpenAIWSAcquireCompatibility(req)
	require.Equal(t, original, normalizeOpenAIWSAcquireCompatibility(req))
	for _, change := range []string{"credential", "url", "proxy", "session"} {
		t.Run(change, func(t *testing.T) {
			changed := req
			copyAccount := *account
			changed.Account = &copyAccount
			changed.Headers = req.Headers.Clone()
			switch change {
			case "credential":
				copyAccount.Credentials = map[string]any{"api_key": "fixture-B"}
			case "url":
				changed.WSURL = "wss://other.test/v1/responses"
			case "proxy":
				changed.ProxyURL = "http://other-proxy.test"
			case "session":
				changed.Headers.Set("Session-Id", "session-B")
			}
			key := normalizeOpenAIWSAcquireCompatibility(changed)
			require.NotEqual(t, original, key)
			require.NotContains(t, fmt.Sprint(key), "fixture-A")
		})
	}
}

func TestNativeStreamNoEventFragmentExposure(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			ctx, cancel := context.WithCancel(nativeRelayContext(context.Background()))
			defer cancel()
			reader, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			writes := make(chan string, 4)
			out := &nativeRelayObservedWriter{header: make(http.Header), writes: writes}
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			result := make(chan error, 1)
			go func() {
				svc := nativeRelayService()
				resp := &http.Response{Header: make(http.Header), Body: reader}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				result <- err
			}()
			frame := nativeRelaySSE("response.created", "\"response\":{\"id\":\"resp_fragment\"}")
			_, err := io.WriteString(writer, strings.TrimSuffix(frame, "\n"))
			require.NoError(t, err)
			select {
			case got := <-writes:
				t.Fatalf("incomplete event exposed: %q", got)
			case <-time.After(25 * time.Millisecond):
			}
			_, err = io.WriteString(writer, "\n")
			require.NoError(t, err)
			select {
			case got := <-writes:
				require.Equal(t, frame, got)
			case <-time.After(time.Second):
				t.Fatal("complete event withheld")
			}
			cancel()
			err = <-result
			require.True(t, errors.Is(err, context.Canceled))
		})
	}
}

type nativeRelayObservedWriter struct {
	header http.Header
	writes chan string
}

func (w *nativeRelayObservedWriter) Header() http.Header { return w.header }
func (w *nativeRelayObservedWriter) WriteHeader(int)     {}
func (w *nativeRelayObservedWriter) Write(p []byte) (int, error) {
	w.writes <- string(p)
	return len(p), nil
}
func (w *nativeRelayObservedWriter) Flush() {}

func TestNativeStreamWSPoolBoundsReturnedIdleAndAge(t *testing.T) {
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 8
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 8
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	ap := pool.getOrCreateAccountPool(881)
	active := newOpenAIWSConn("active", 881, nil, nil)
	active.nativeDelivery = true
	require.True(t, active.tryAcquire())
	ap.conns[active.id] = active
	for i := 0; i < 4; i++ {
		conn := newOpenAIWSConn(fmt.Sprintf("idle-%d", i), 881, nil, nil)
		conn.nativeDelivery = true
		conn.lastUsedNano.Store(time.Now().Add(-time.Duration(i) * time.Second).UnixNano())
		ap.conns[conn.id] = conn
	}
	pool.trimNativeIdle(881)
	ap.mu.Lock()
	require.Len(t, ap.conns, 3)
	require.Same(t, active, ap.conns["active"])
	for id, conn := range ap.conns {
		if id != "active" {
			conn.lastUsedNano.Store(time.Now().Add(-91 * time.Second).UnixNano())
		}
	}
	evicted := pool.cleanupAccountLocked(ap, time.Now(), 8)
	require.Len(t, ap.conns, 1)
	ap.mu.Unlock()
	closeOpenAIWSConns(evicted)
	active.release()
}

// The actual downstream connection is a WebSocket, independent of the HTTP->WS
// transport fixture above. A client stop must close the upstream without drain.
func TestNativeStreamWSPassthroughImmediateCommitAndCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancel := context.WithCancel(NewControlledRequestContext(nativeRelayContext(context.Background()), "ws"))
	defer cancel()
	upstream := newStagedPassthroughConn()
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, passthroughLifecycleAccount())
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	require.Equal(t, "response.create", gjson.GetBytes(requirePassthroughUpstreamWrite(t, upstream, time.Second), "type").String())
	upstream.Send(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_native_ws"}}`)
	created, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.True(t, ControlledStreamSnapshot(controlCtx).AttemptCommitted)
	require.False(t, ControlledStreamSnapshot(controlCtx).SemanticSeen)
	upstream.Send(`{"type":"response.vendor_progress","sequence_number":1,"opaque":"kept"}`)
	unknown, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.Equal(t, "kept", gjson.GetBytes(unknown, "opaque").String())
	stoppedAt := time.Now()
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "user stopped"))
	select {
	case <-upstream.closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("native WS client disconnect retained upstream for legacy usage drain")
	}
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("native WS passthrough did not return after client stopped")
	}
	require.Equal(t, ControlledClientDetached, ControlledStreamSnapshot(controlCtx).CancelReason)
	t.Logf("real_ws_created_before_content=true committed_before_visible=true create_count=1 cancel_ms=%d", time.Since(stoppedAt).Milliseconds())
}

func TestNativeStreamWSPassthroughPolicyRefreshAtNextTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var version atomic.Int64
	var reads atomic.Int32
	version.Store(1)
	initial := WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 1})
	initial = context.WithValue(initial, nativeStreamPolicyReaderKey{}, nativeStreamPolicyReader{groupID: 42, read: func(context.Context, int64) (NativeStreamPolicy, error) {
		reads.Add(1)
		v := version.Load()
		return NativeStreamPolicy{Version: v, Delivery: v == 2}, nil
	}})
	controlCtx, cancel := context.WithCancel(initial)
	defer cancel()
	upstream := newStagedPassthroughConn()
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, passthroughLifecycleAccount())
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_old"}}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	version.Store(2)
	require.Zero(t, reads.Load())
	require.False(t, NativeStreamDeliveryEnabled(controlCtx))
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_old","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`)
	_, err = readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	writeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	err = client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`))
	stop()
	require.NoError(t, err)
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	require.EqualValues(t, 1, reads.Load())
	require.False(t, NativeStreamDeliveryEnabled(controlCtx))
	upstream.Send(`{"type":"response.created","response":{"id":"resp_new"}}`)
	_, err = readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "stop second turn"))
	select {
	case <-upstream.closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second WS turn did not use freshly enabled native cancellation policy")
	}
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("second WS turn did not end")
	}
	t.Log("active_turn_version=1 unchanged; next_turn_version=2 read_once; next_turn_native_cancel=true")
}

func TestNativeStreamWSPassthroughExplicitCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancel := context.WithCancel(NewControlledRequestContext(nativeRelayContext(context.Background()), "ws"))
	defer cancel()
	upstream := newStagedPassthroughConn()
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	server, serverErr := startPassthroughLifecycleServer(t, controlCtx, svc, passthroughLifecycleAccount())
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.created","response":{"id":"resp_stop"}}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	writeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	err = client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.cancel","response_id":"resp_stop"}`))
	stop()
	require.NoError(t, err)
	cancelFrame := requirePassthroughUpstreamWrite(t, upstream, time.Second)
	require.Equal(t, "response.cancel", gjson.GetBytes(cancelFrame, "type").String())
	select {
	case <-upstream.closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("explicit response.cancel did not immediately terminate original upstream")
	}
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("explicit cancellation left WS execution running")
	}
	require.Equal(t, ControlledUserStop, ControlledStreamSnapshot(controlCtx).CancelReason)
	require.Empty(t, upstream.writes)
	t.Log("explicit_cancel_forwarded_once=true terminal_reason=user_stop no_resume_no_new_create=true")
}

func TestNativeStreamWSCtxPoolSentCreateDoesNotRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	first := &openAIWSWriteFailAfterFirstTurnConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)}}
	second := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{first, second}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	defer pool.Close()
	svc := newPassthroughLifecycleService(cfg, newStagedPassthroughConn())
	svc.openaiWSPool = pool
	account := passthroughLifecycleAccount()
	account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	ctx, cancel := context.WithCancel(nativeRelayContext(context.Background()))
	defer cancel()
	server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	frame, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.Equal(t, "resp_ctx_first", gjson.GetBytes(frame, "response.id").String())
	writeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	err = client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_ctx_first","input":[]}`))
	stop()
	require.NoError(t, err)
	select {
	case err := <-serverErr:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ctx_pool retried a possibly sent native create")
	}
	require.Equal(t, 1, dialer.DialCount())
	require.Empty(t, second.writes)
	t.Log("native_ctx_pool_turn2_write_uncertain=true replacement_dials=0 new_create_replay=0")
}

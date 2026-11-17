//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const capabilityFinalRefusal = `{"type":"response.failed","response":{"id":"fixture","status":"failed","error":{"code":"MODEL_UNSUPPORTED"}}}`
const capabilityProvisionalRefusal = `{"type":"error","error":{"code":"unsupported_protocol","message":"unsupported transport"}}`
const capabilityFinalSuccess = `{"type":"response.completed","response":{"id":"fixture","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`

type capabilityReadBoundaryConn struct {
	*openAIWSCaptureConn
	onRead func([]byte)
	reads  int
}

func (c *capabilityReadBoundaryConn) ReadMessage(ctx context.Context) ([]byte, error) {
	b, err := c.openAIWSCaptureConn.ReadMessage(ctx)
	if err == nil {
		c.reads++
	}
	if err == nil && c.onRead != nil {
		c.onRead(b)
	}
	return b, err
}

type capabilityDeadlineWriter struct {
	header http.Header
	writes int
}

func (w *capabilityDeadlineWriter) Header() http.Header { return w.header }
func (w *capabilityDeadlineWriter) WriteHeader(int)     {}
func (w *capabilityDeadlineWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, context.DeadlineExceeded
}
func (*capabilityDeadlineWriter) Flush() {}

func capabilityFinalityService(t *testing.T, conn openAIWSClientConn) (*OpenAIGatewayService, *Account, *capabilityRepo) {
	t.Helper()
	cfg := newOpenAIWSExecutionScopeTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 0
	account := capabilityTestAccount(1)
	account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSSingleConnDialer{conn: conn})
	t.Cleanup(pool.Close)
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: repo, cache: &stubGatewayCache{}, httpUpstream: &httpUpstreamRecorder{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	return svc, account, repo
}

func TestCapabilityHTTPToWSExplicitFinalExits(t *testing.T) {
	for _, mode := range []string{"capability_failure", "cancel_drain_refusal", "cancel_drain_read_error", "local_write_deadline"} {
		t.Run(mode, func(t *testing.T) {
			state := resilience.NewRequestState()
			ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), state))
			defer cancel()
			events := [][]byte{[]byte(capabilityFinalRefusal)}
			if strings.HasPrefix(mode, "cancel") {
				events = [][]byte{[]byte(errclassWSCreated), []byte(capabilityFinalRefusal)}
				if mode == "cancel_drain_read_error" {
					events[1] = []byte(capabilityProvisionalRefusal)
				}
			}
			if mode == "local_write_deadline" {
				events = [][]byte{[]byte(`{"type":"error","error":{"code":"MODEL_UNSUPPORTED"}}`)}
			}
			conn := &capabilityReadBoundaryConn{openAIWSCaptureConn: &openAIWSCaptureConn{events: events}}
			if strings.HasPrefix(mode, "cancel") {
				conn.onRead = func(b []byte) {
					if strings.Contains(string(b), "response.created") {
						cancel()
					}
				}
			}
			svc, account, repo := capabilityFinalityService(t, conn)
			var writer http.ResponseWriter = httptest.NewRecorder()
			failing := &capabilityDeadlineWriter{header: http.Header{}}
			if mode == "local_write_deadline" {
				writer = failing
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			_, _ = svc.forwardOpenAIWSV2(ctx, c, account, map[string]any{"model": "fixture", "stream": true, "input": "fixture"}, "", "", "fixture", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, true, "fixture", "fixture", time.Now(), 1, "", nil)
			if mode == "local_write_deadline" {
				require.Positive(t, failing.writes, "must exercise actual production downstream write error exit")
			}
			require.Equal(t, mode == "capability_failure", account.CapabilityMarked("fixture", "responses", "stream", time.Now()) != nil)
			if mode != "capability_failure" {
				require.Zero(t, repo.writes)
			}
			require.Nil(t, state.CurrentAttempt(), "independent learning must not create a sampling attempt")
		})
	}
}

func TestCapabilityNativeWSExplicitFinalExits(t *testing.T) {
	for _, mode := range []string{"capability_failure", "cancel_drain_refusal", "cancel_drain_read_error", "success_then_write_deadline"} {
		t.Run(mode, func(t *testing.T) {
			events := [][]byte{[]byte(capabilityFinalRefusal)}
			if strings.HasPrefix(mode, "cancel") {
				events = [][]byte{[]byte(errclassWSCreated), []byte(capabilityFinalRefusal)}
				if mode == "cancel_drain_read_error" {
					events[1] = []byte(capabilityProvisionalRefusal)
				}
			}
			if mode == "success_then_write_deadline" {
				events = [][]byte{[]byte(capabilityProvisionalRefusal), []byte(capabilityFinalSuccess)}
			}
			conn := &capabilityReadBoundaryConn{openAIWSCaptureConn: &openAIWSCaptureConn{events: events}}
			svc, account, repo := capabilityFinalityService(t, conn)
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			expired, release := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer release()
			hooks := &OpenAIWSIngressHooks{ClientLifecycleContext: context.Background()}
			peers := make(chan *coderws.Conn, 1)
			done := make(chan error, 1)
			writeDeadlineReached := false
			disconnectObserved := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downstream, err := coderws.Accept(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer downstream.CloseNow()
				peer := <-peers
				conn.onRead = func(b []byte) {
					if strings.HasPrefix(mode, "cancel") && strings.Contains(string(b), "response.created") {
						_ = peer.CloseNow()
						readCtx, stop := context.WithTimeout(context.Background(), time.Second)
						_, _, readErr := downstream.Read(readCtx)
						stop()
						disconnectObserved = readErr != nil
					}
					if mode == "success_then_write_deadline" && strings.Contains(string(b), "response.completed") {
						hooks.ClientLifecycleContext = expired
						writeDeadlineReached = true
					}
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r.WithContext(ctx)
				c.Set("api_key", &APIKey{ID: 7})
				done <- svc.ProxyResponsesWebSocketFromClient(ctx, c, downstream, account, "fixture", []byte(`{"type":"response.create","model":"fixture","input":"fixture"}`), hooks)
			}))
			defer server.Close()
			dialCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			peer, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			peers <- peer
			defer peer.CloseNow()
			for {
				_, b, readErr := peer.Read(dialCtx)
				if readErr != nil {
					break
				}
				if strings.Contains(string(b), "response.failed") {
					_ = peer.CloseNow()
					break
				}
			}
			stop()
			select {
			case finalErr := <-done:
				if mode == "success_then_write_deadline" {
					require.True(t, writeDeadlineReached)
					require.ErrorIs(t, finalErr, context.DeadlineExceeded)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("production WS turn did not finish")
			}
			if strings.HasPrefix(mode, "cancel") {
				require.True(t, disconnectObserved, "real downstream close must be observed before drain")
				require.Equal(t, 2, conn.reads, "the production consumer must drain and observe the refusal after disconnect")
			}
			require.Equal(t, mode == "capability_failure", account.CapabilityMarked("fixture", "responses", "stream", time.Now()) != nil)
			if mode != "capability_failure" {
				require.Zero(t, repo.writes)
			}
			require.Nil(t, state.CurrentAttempt())
		})
	}
}

func TestCapabilityOldSnapshotRelearnsAfterClearOrEviction(t *testing.T) {
	for _, action := range []string{"clear", "evict"} {
		t.Run(action, func(t *testing.T) {
			account := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			mark := syntheticCapabilityMark()
			require.NoError(t, svc.LearnCapabilityMark(context.Background(), account, mark))
			stale, err := repo.GetByID(context.Background(), 1)
			require.NoError(t, err)
			require.NotNil(t, stale.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()))
			if action == "clear" {
				require.NoError(t, (&adminServiceImpl{accountRepo: repo}).ClearAccountCapabilityMarks(context.Background(), 1))
			} else {
				early := *account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now())
				early.ExpiresAt = time.Now().Add(time.Minute)
				account.Extra[capabilityMarkExtraKey(mark.Model, mark.Protocol, mark.Shape)] = early
				for i := 0; i < CapabilityMarkLimit; i++ {
					other := mark
					other.Model = string(rune('a'+i)) + "-other-fixture"
					require.NoError(t, svc.LearnCapabilityMark(context.Background(), account, other))
				}
			}
			require.Nil(t, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()))
			before := repo.writes
			require.NoError(t, svc.LearnCapabilityMark(context.Background(), stale, mark))
			require.Equal(t, before+1, repo.writes)
			require.NotNil(t, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()))
		})
	}
}

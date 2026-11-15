//go:build unit

package service

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Block the actual terminal socket write, after the relay observed its event.
type stickyWSWriteGate struct {
	armed            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
	err              error
}

func (g *stickyWSWriteGate) unblock() { g.once.Do(func() { close(g.release) }) }

type stickyWSGatedConn struct {
	net.Conn
	gate *stickyWSWriteGate
}

func (c *stickyWSGatedConn) Write(p []byte) (int, error) {
	if c.gate.armed.CompareAndSwap(true, false) {
		close(c.gate.entered)
		<-c.gate.release
		if c.gate.err != nil {
			return 0, c.gate.err
		}
	}
	return c.Conn.Write(p)
}

type stickyWSGatedWriter struct {
	http.ResponseWriter
	gate *stickyWSWriteGate
}

func (w *stickyWSGatedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	gated := &stickyWSGatedConn{Conn: conn, gate: w.gate}
	return gated, bufio.NewReadWriter(rw.Reader, bufio.NewWriter(gated)), nil
}

func TestStickyWSTerminalCommitsOnlyAfterClientWrite(t *testing.T) {
	for _, tc := range []struct {
		name         string
		event        string
		reason       string
		writeErr     error
		wantCommit   bool
		wantCanceled bool
	}{
		{name: "completed", event: "response.completed", wantCommit: true},
		{name: "max_output_tokens", event: "response.incomplete", reason: "max_output_tokens", wantCommit: true},
		{name: "content_filter", event: "response.incomplete", reason: "content_filter", wantCommit: true},
		{name: "failed_terminal", event: "response.failed"},
		{name: "unknown_incomplete", event: "response.incomplete", reason: "unknown"},
		{name: "internal_write_error", event: "response.completed", writeErr: errors.New("synthetic internal write failure")},
		{name: "write_deadline", event: "response.completed", writeErr: context.DeadlineExceeded},
		{name: "internal_cancel", event: "response.completed", writeErr: context.Canceled},
		{name: "client_disconnect", event: "response.completed", writeErr: syscall.EPIPE, wantCanceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			account := passthroughLifecycleAccount()
			account.GroupIDs = []int64{7}
			f := newSelectionRuntimeFixture(t, *account)
			f.svc.cfg = passthroughLifecycleConfig()
			f.svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 5
			upstream := newStagedPassthroughConn()
			t.Cleanup(func() { _ = upstream.Close() })
			f.svc.openaiWSResolver = NewOpenAIWSProtocolResolver(f.svc.cfg)
			f.svc.toolCorrector = NewCodexToolCorrector()
			f.svc.openaiWSPassthroughDialer = &stagedPassthroughDialer{conn: upstream}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			// WS does not migrate existing owners. Its initial binding still waits
			// for delivery, including when no recorder/request ledger is installed.
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "ws-session", "gpt-5.1", nil, OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, selected)
			t.Cleanup(selected.ReleaseFunc)
			pending := pendingOpenAIStickyChange(ctx)
			require.NotNil(t, pending)
			require.Equal(t, "create", pending.kind)
			require.False(t, state.GatewayRequestEnabled())
			require.Nil(t, f.svc.schedAttemptRecorder)
			gate := &stickyWSWriteGate{entered: make(chan struct{}), release: make(chan struct{}), err: tc.writeErr}
			turns := make(chan *OpenAIForwardResult, 2)
			exited := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, acceptErr := coderws.Accept(&stickyWSGatedWriter{ResponseWriter: w, gate: gate}, r, nil)
				if acceptErr != nil {
					exited <- acceptErr
					return
				}
				defer conn.CloseNow()
				_, first, readErr := conn.Read(r.Context())
				if readErr != nil {
					exited <- readErr
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r.Clone(ctx)
				hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, _ error) {
					// A nil result is the existing connection-exit notification.
					if result != nil {
						turns <- result
					}
				}}
				exited <- f.svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "sk-test", first, hooks)
			}))
			t.Cleanup(server.Close)
			t.Cleanup(gate.unblock)
			deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(deadline, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.CloseNow() })
			require.NoError(t, client.Write(deadline, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)))
			_ = requirePassthroughUpstreamWrite(t, upstream, 5*time.Second)
			gate.armed.Store(true)
			upstream.Send(`{"type":"` + tc.event + `","response":{"id":"resp_sticky","model":"gpt-5.1","incomplete_details":{"reason":"` + tc.reason + `"},"usage":{"input_tokens":1,"output_tokens":1}}}`)
			select {
			case <-gate.entered:
			case <-deadline.Done():
				t.Fatal("terminal socket write did not start")
			}
			require.Empty(t, f.cache.casKeys, "upstream terminal receipt is not successful client delivery")
			pending.mu.Lock()
			done := pending.done
			pending.mu.Unlock()
			require.False(t, done)
			gate.unblock()
			if tc.writeErr == nil {
				_, _, err = client.Read(deadline)
				require.NoError(t, err)
			}
			var result *OpenAIForwardResult
			select {
			case result = <-turns:
			case <-deadline.Done():
				t.Fatal("terminal write did not finalize its turn")
			}
			want := 0
			if tc.wantCommit {
				want = 1
			}
			require.Len(t, f.cache.casKeys, want, "the production turn hook must finalize the binding itself")
			pending.mu.Lock()
			done = pending.done
			pending.mu.Unlock()
			require.True(t, done)
			CompleteOpenAIStickyChange(ctx, account.ID, true, false)
			FinishOpenAIStickyForward(ctx, account.ID, result, tc.writeErr)
			require.Len(t, f.cache.casKeys, want, "a turn and repeated completion commit at most once")
			if tc.wantCommit {
				require.Equal(t, account.ID, f.cache.sessionBindings["openai:ws-session"])
			} else {
				require.Zero(t, f.cache.sessionBindings["openai:ws-session"])
			}
			require.Equal(t, tc.wantCanceled, result.ClientDisconnect)
			_ = client.CloseNow()
			select {
			case <-exited:
			case <-deadline.Done():
				t.Fatal("websocket relay did not exit")
			}
			require.Len(t, f.cache.casKeys, want, "connection cleanup must not submit another CAS")
			require.Empty(t, turns, "each text turn has one finalization callback")
		})
	}
}

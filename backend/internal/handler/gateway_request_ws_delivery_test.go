//go:build unit

package handler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Stop the real downstream socket write after the terminal has been observed.
// This checks the handler, adapter and relay together, including durable billing.
type requestWriteGate struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	err     error
	once    sync.Once
}

func (g *requestWriteGate) unblock() { g.once.Do(func() { close(g.release) }) }

type requestGatedConn struct {
	net.Conn
	gate *requestWriteGate
}

func (c *requestGatedConn) Write(p []byte) (int, error) {
	if c.gate.armed.CompareAndSwap(true, false) {
		close(c.gate.entered)
		<-c.gate.release
		if c.gate.err != nil {
			return 0, c.gate.err
		}
	}
	return c.Conn.Write(p)
}

type requestGatedWriter struct {
	http.ResponseWriter
	gate *requestWriteGate
}

func (w *requestGatedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	gated := &requestGatedConn{Conn: c, gate: w.gate}
	return gated, bufio.NewReadWriter(rw.Reader, bufio.NewWriter(gated)), nil
}

func TestRequestResultWSTerminalClosesAfterWriteAndBillsOnce(t *testing.T) {
	var successfulCost float64
	for _, tc := range []struct {
		name     string
		writeErr error
		binary   bool
		want     int16
	}{
		{name: "delivered", want: 1},
		{name: "write_failed", writeErr: errors.New("synthetic internal write failure"), want: 3},
		{name: "client_broken_pipe", writeErr: syscall.EPIPE, want: 4},
		{name: "write_deadline", writeErr: context.DeadlineExceeded, want: 3},
		{name: "binary_delivered", binary: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, nil)
			gate := &requestWriteGate{entered: make(chan struct{}), release: make(chan struct{}), err: tc.writeErr}
			defer gate.unblock()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if _, _, err = conn.Read(r.Context()); err != nil {
					return
				}
				gate.armed.Store(true)
				typ := coderws.MessageText
				if tc.binary {
					typ = coderws.MessageBinary
				}
				_ = conn.Write(r.Context(), typ, []byte(`{"type":"response.completed","response":`+captureResponseJSON+`}`))
				_, _, _ = conn.Read(r.Context())
			}))
			defer upstream.Close()
			rows := &requestRecordRepo{}
			done := make(chan struct{})
			_, rec, router := captureFixture(t, service.Account{Credentials: map[string]any{"api_key": "synthetic", "base_url": upstream.URL}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough}}, &compositeWSHTTPUpstream{}, captureOptions{usage: rows, requestDone: done})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				router.ServeHTTP(&requestGatedWriter{ResponseWriter: w, gate: gate}, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer conn.CloseNow()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)))
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("terminal client write never started")
			}
			rec.mu.Lock()
			finished := len(rec.samples)
			rec.mu.Unlock()
			require.Zero(t, finished, "attempt must remain open while client write is blocked")
			require.Empty(t, rows.bills, "billing follows terminal write")
			require.Empty(t, rows.unbilled, "request must remain open while client write is blocked")
			gate.unblock()
			if tc.writeErr == nil {
				_, _, err = conn.Read(ctx)
				require.NoError(t, err)
				_ = conn.Close(coderws.StatusNormalClosure, "")
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("terminal write exit did not release the lifecycle gate")
			}
			if tc.binary {
				require.Len(t, rows.bills, 1, "binary keeps one connection fallback bill")
				require.Nil(t, rows.bills[0].Result)
				require.Nil(t, rows.bills[0].GatewayRequestID)
				require.Zero(t, rows.bills[0].ActualCost)
				require.Empty(t, rows.unbilled)
			} else {
				require.Len(t, rows.bills, 1)
				require.Empty(t, rows.unbilled)
				require.Equal(t, tc.want, *rows.bills[0].Result)
				if tc.writeErr == nil {
					successfulCost = rows.bills[0].ActualCost
					require.Positive(t, successfulCost)
				} else {
					require.Equal(t, successfulCost, rows.bills[0].ActualCost, "write failure must not change the upstream usage charge")
				}
			}
			rec.mu.Lock()
			defer rec.mu.Unlock()
			require.Len(t, rec.samples, 1)
		})
	}
}

func TestRequestResultWSNextTurnWaitsForPreviousBilling(t *testing.T) {
	withHandlerResilience(t, nil)
	billingEntered, billingRelease := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(billingRelease) }) }
	defer release()
	var billOnce sync.Once
	rows := &requestRecordRepo{beforeCreate: func() { billOnce.Do(func() { close(billingEntered); <-billingRelease }) }}
	sent := make(chan int, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for turn := 1; turn <= 2; turn++ {
			if _, _, err = conn.Read(r.Context()); err != nil {
				return
			}
			sent <- turn
			payload := strings.Replace(captureResponseJSON, "resp_capture", fmt.Sprintf("resp_%d", turn), 1)
			if conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":`+payload+`}`)) != nil {
				return
			}
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()
	done := make(chan struct{})
	_, rec, router := captureFixture(t, service.Account{Credentials: map[string]any{"api_key": "synthetic", "base_url": upstream.URL}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough}}, &compositeWSHTTPUpstream{}, captureOptions{usage: rows, requestDone: done})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
	require.NoError(t, conn.Write(ctx, coderws.MessageText, payload))
	_, _, err = conn.Read(ctx)
	require.NoError(t, err)
	select {
	case <-billingEntered:
	case <-ctx.Done():
		t.Fatal("billing did not start")
	}
	require.Equal(t, 1, <-sent)
	require.NoError(t, conn.Write(ctx, coderws.MessageText, payload))
	select {
	case <-sent:
		t.Fatal("next request sent before previous turn closed")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case turn := <-sent:
		require.Equal(t, 2, turn)
	case <-ctx.Done():
		t.Fatal("next turn remained blocked")
	}
	_, _, err = conn.Read(ctx)
	require.NoError(t, err)
	_ = conn.Close(coderws.StatusNormalClosure, "")
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("handler did not exit")
	}
	require.Len(t, rows.bills, 2)
	require.Empty(t, rows.unbilled)
	require.NotEqual(t, *rows.bills[0].GatewayRequestID, *rows.bills[1].GatewayRequestID)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.starts, 2)
	require.Equal(t, rec.starts[0].RequestID, *rows.bills[0].GatewayRequestID)
	require.Equal(t, rec.starts[1].RequestID, *rows.bills[1].GatewayRequestID)
}

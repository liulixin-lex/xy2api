//go:build unit

package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// A pipe keeps the real WebSocket/HTTP stack inside synctest's bubble, so Wait
// observes the close callback without network timing or sleeps.
type requestResultPipeListener struct {
	conn   chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *requestResultPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conn:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *requestResultPipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (*requestResultPipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestRequestResultWebSocketCloseAfterTerminalDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		want        int16
	}{
		{"completed", "response.completed", resilience.RequestSuccess},
		{"failed", "response.failed", resilience.RequestFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st := resilience.NewRequestState()
				st.EnableGatewayRequest(time.Now())
				st.BeginGatewayTurn(time.Now())
				ctx := resilience.WithRequestState(context.Background(), st)
				st.InitDelivery(ctx, time.Now(), nil)
				upstream := newStagedPassthroughConn()
				// A nonterminal frame starts the client reader before the terminal.
				upstream.Send(`{"type":"response.created","response":{"id":"resp_close"}}`)
				upstream.Send(`{"type":"` + tc.event + `","response":{"id":"resp_close","usage":{"input_tokens":1,"output_tokens":1}}}`)
				svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
				terminalDelivered, settleTurn := make(chan struct{}), make(chan struct{})
				var meta *resilience.GatewayRequestMetadata
				hooks := &OpenAIWSIngressHooks{
					TurnStarted: func(_ int, _ time.Time) {
						// Called after the terminal write, before schedTurns.finish.
						close(terminalDelivered)
						<-settleTurn
					},
					AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
						st.CompleteGatewayForward(err == nil && result.CompletedForDelivery(), result.ClientDisconnect)
						meta, _ = st.ClaimGatewayRequest(true)
					},
				}
				listener := &requestResultPipeListener{conn: make(chan net.Conn, 1), closed: make(chan struct{})}
				serverDone := make(chan error, 1)
				server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						serverDone <- err
						return
					}
					defer conn.CloseNow()
					_, first, err := conn.Read(ctx)
					if err != nil {
						serverDone <- err
						return
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = r.WithContext(ctx)
					serverDone <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, passthroughLifecycleAccount(), "synthetic", first, hooks)
				})}
				defer server.Close()
				go func() { _ = server.Serve(listener) }()
				transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
					client, server := net.Pipe()
					listener.conn <- server
					return client, nil
				}}
				defer transport.CloseIdleConnections()
				client, _, err := coderws.Dial(ctx, "ws://fixture/v1/responses", &coderws.DialOptions{HTTPClient: &http.Client{Transport: transport}})
				require.NoError(t, err)
				defer client.CloseNow()
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)))
				for range 2 {
					_, _, err = client.Read(ctx)
					require.NoError(t, err)
				}
				<-terminalDelivered
				closeDone := make(chan error, 1)
				go func() { closeDone <- client.Close(coderws.StatusNormalClosure, "") }()
				// Drive the actual read_client/BeforeRelayCancel path while the
				// delivered turn is deliberately held before attempt settlement.
				synctest.Wait()
				close(settleTurn)
				require.NoError(t, <-closeDone)
				require.NoError(t, <-serverDone)
				require.NotNil(t, meta)
				t.Logf("terminal=%s result=%d", tc.event, meta.Result)
				require.Equal(t, tc.want, meta.Result)
			})
		})
	}
}

//go:build unit

package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrclassControlWriteTerminalBarrier(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		st := resilience.NewRequestState()
		st.EnableGatewayRequest(time.Now())
		account := passthroughLifecycleAccount()
		st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
		ctx := resilience.WithRequestState(context.Background(), st)
		st.InitDelivery(ctx, time.Now(), nil)
		upstream := newStagedPassthroughConn()
		upstream.Send(errclassWSDelta)
		upstream.Send(errclassWSCompleted)
		svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
		controlFailed := make(chan struct{})
		svc.openaiWSPassthroughDialer = &stagedPassthroughDialer{conn: &errclassControlWriteFailureConn{stagedPassthroughConn: upstream, failed: controlFailed}}
		recorder := &errclassRecorder{}
		svc.SetSchedAttemptRecorder(recorder)
		terminalDelivered, settle := make(chan struct{}), make(chan struct{})
		var meta *resilience.GatewayRequestMetadata
		hooks := &OpenAIWSIngressHooks{
			TurnStarted: func(int, time.Time) { close(terminalDelivered); <-settle },
			AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
				st.CompleteGatewayForward(err == nil && result.CompletedForDelivery(), result.ClientDisconnect)
				meta, _ = st.ClaimGatewayRequest(true)
			},
		}
		listener := &requestResultPipeListener{conn: make(chan net.Conn, 1), closed: make(chan struct{})}
		done := make(chan error, 1)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := coderws.Accept(w, r, nil)
			if err != nil {
				done <- err
				return
			}
			defer conn.CloseNow()
			_, first, err := conn.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = r.WithContext(ctx)
			done <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "synthetic", first, hooks)
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
		require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"synthetic-model","input":"synthetic"}`)))
		for range 2 {
			_, _, err = client.Read(ctx)
			require.NoError(t, err)
		}
		<-terminalDelivered
		require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"session.update","session":{}}`)))
		<-controlFailed
		closed := make(chan struct{})
		go func() { _, _, _ = client.Read(ctx); close(closed) }()
		synctest.Wait()
		assert.Empty(t, recorder.finishes, "control failure must wait for terminal settlement")
		close(settle)
		<-done
		<-closed
		require.Len(t, recorder.finishes, 1)
		require.Equal(t, schedquality.OutcomeOK, recorder.finishes[0].Outcome)
		require.Equal(t, schedquality.ClassNone, recorder.finishes[0].Class)
		require.NotNil(t, meta)
		require.Equal(t, resilience.RequestSuccess, meta.Result)
	})
}

func TestErrclassProcessingFailureNegativeEvidence(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"Bad request"}}`,
		`{"error":{"type":"invalid_request_error","code":"invalid_parameter","param":"input","message":"Invalid parameter input: An error occurred while processing your request"}}`,
	} {
		got := classifySchedAttempt(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault}, &resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: 400, Body: []byte(body)}, false, false)
		require.NotEqual(t, schedquality.ClassUpstream, got.Class)
		require.False(t, got.Class.Counted())
	}
}

type errclassPanicHTTPUpstream struct {
	HTTPUpstream
	value any
}

func (u *errclassPanicHTTPUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	panic(u.value)
}

func TestErrclassTransportPanic(t *testing.T) {
	withResilienceSettings(t, nil)
	body := []byte(`{"model":"synthetic","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, _ := compatStreamContext("/v1/chat/completions", string(body))
	account := rawChatCompletionsTestAccount()
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic"})
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), st))
	value := &struct{ message string }{"synthetic panic"}
	svc := compatStreamService(&errclassPanicHTTPUpstream{value: value}, 0, 0, 0)
	recorder := &errclassRecorder{}
	svc.SetSchedAttemptRecorder(recorder)
	require.PanicsWithValue(t, value, func() { _, _ = captureForward(svc, c, account, body, "chat") })
	FinishSchedAttemptAtHandlerExit(c.Request.Context())
	require.Len(t, recorder.finishes, 1)
	require.Equal(t, schedquality.ClassGateway, recorder.finishes[0].Class)
	require.False(t, recorder.finishes[0].Class.Counted())
}

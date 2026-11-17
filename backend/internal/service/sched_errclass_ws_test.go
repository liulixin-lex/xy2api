//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const errclassWSCompleted = `{"type":"response.completed","response":{"id":"resp_synthetic","status":"completed","model":"synthetic-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`
const errclassWSCreated = `{"type":"response.created","response":{"id":"resp_synthetic","status":"in_progress"}}`
const errclassWSDelta = `{"type":"response.output_text.delta","delta":"synthetic"}`

type errclassRealWSDialer struct {
	url  string
	fail bool
}

func (d *errclassRealWSDialer) Dial(ctx context.Context, _ string, _ http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	if d.fail {
		return nil, 0, nil, errors.New("synthetic unavailable before send")
	}
	conn, resp, err := coderws.Dial(ctx, d.url, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	return &coderOpenAIWSClientConn{conn: conn}, resp.StatusCode, resp.Header, nil
}

// Both sides of this fixture use the real websocket framing and socket stack.
// The upstream receives an application request before any simulated failure.
func errclassWSFixture(t *testing.T, mode string) (*OpenAIGatewayService, *Account, *resilience.RequestState, *errclassRecorder, *atomic.Int32, func()) {
	t.Helper()
	var sends atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			return
		}
		n := sends.Add(1)
		switch mode {
		case "disconnect":
			return
		case "retry":
			if n == 1 {
				return
			}
		case "output_disconnect":
			_ = conn.Write(ctx, coderws.MessageText, []byte(errclassWSDelta))
			return
		case "deadline":
			select {
			case <-release:
			case <-ctx.Done():
			}
			return
		case "client_disconnect":
			// HTTP buffers response.created, so use effective output to reach
			// the real downstream write before the client disconnects.
			if conn.Write(ctx, coderws.MessageText, []byte(errclassWSDelta)) != nil {
				return
			}
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			// ctx_pool drains the upstream after a failed client write. Exercise
			// subsequent writes after the peer closes, then supply the terminal
			// so the existing drain policy can finish normally.
			for i := 0; i < 8; i++ {
				if conn.Write(ctx, coderws.MessageText, []byte(errclassWSDelta)) != nil {
					return
				}
				select {
				case <-time.After(5 * time.Millisecond):
				case <-ctx.Done():
					return
				}
			}
		}
		_ = conn.Write(ctx, coderws.MessageText, []byte(errclassWSCompleted))
	}))
	t.Cleanup(func() { unblock(); upstream.Close() })
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 0
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	dialer := &errclassRealWSDialer{url: "ws" + strings.TrimPrefix(upstream.URL, "http"), fail: mode == "unsent"}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	t.Cleanup(pool.Close)
	rec := &errclassRecorder{}
	svc := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}, httpUpstream: &httpUpstreamRecorder{}, toolCorrector: NewCodexToolCorrector(), openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPool: pool, openaiWSPassthroughDialer: dialer, schedAttemptRecorder: rec}
	account := passthroughLifecycleAccount()
	account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	st := resilience.NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
	return svc, account, st, rec, &sends, unblock
}

func TestErrclassRealHTTPToWSFailures(t *testing.T) {
	for _, mode := range []string{"disconnect", "output_disconnect", "completed_close", "deadline", "client_disconnect"} {
		t.Run(mode, func(t *testing.T) {
			withResilienceSettings(t, nil)
			svc, account, st, rec, sends, release := errclassWSFixture(t, mode)
			ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), st))
			defer cancel()
			var writer http.ResponseWriter = httptest.NewRecorder()
			if mode == "client_disconnect" {
				writer = &cancelOnFirstWriteResponseWriter{cancel: func() { cancel(); release() }}
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			InstallDeliveryTracking(c, time.Now())
			result, err := svc.forwardOpenAIWSV2(ctx, c, account, map[string]any{"model": "synthetic-model", "input": "synthetic", "stream": true}, "", "", "synthetic-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, true, "synthetic-model", "synthetic-model", time.Now(), 1, "", nil)
			require.EqualValues(t, 1, sends.Load())
			require.Len(t, rec.finishes, 1)
			want := schedquality.ClassUpstream
			switch mode {
			case "output_disconnect":
				want = schedquality.ClassInterrupted
			case "completed_close":
				want = schedquality.ClassNone
				require.NoError(t, err)
				require.True(t, result.CompletedForDelivery())
			case "deadline":
				want = schedquality.ClassUnknown
			case "client_disconnect":
				want = schedquality.ClassClientCancel
			}
			require.Equal(t, want, rec.finishes[0].Class)
		})
	}
}

func TestErrclassHTTPToWSRequestSendCounting(t *testing.T) {
	for _, mode := range []string{"retry", "completed_close", "unsent"} {
		for _, record := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/record=%v", mode, record), func(t *testing.T) {
				withResilienceSettings(t, nil)
				svc, account, st, rec, sends, _ := errclassWSFixture(t, mode)
				if !record {
					svc.SetSchedAttemptRecorder(nil)
				}
				ctx := resilience.WithRequestState(context.Background(), st)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
				InstallDeliveryTracking(c, time.Now())
				call := func(attempt int) (*OpenAIForwardResult, error) {
					return svc.forwardOpenAIWSV2(ctx, c, account, map[string]any{"model": "synthetic-model", "input": "synthetic", "stream": true}, "", "", "synthetic-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, true, "synthetic-model", "synthetic-model", time.Now(), attempt, "read_event", nil)
				}
				result, err := call(1)
				if mode == "retry" {
					require.Error(t, err)
					result, err = call(2)
				}
				if mode == "unsent" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				st.CompleteGatewayForward(err == nil && result.CompletedForDelivery(), false)
				meta, _ := st.ClaimGatewayRequest(true)
				switch mode {
				case "retry":
					require.EqualValues(t, 2, sends.Load())
					require.Equal(t, resilience.RequestRescued, meta.Result)
				case "completed_close":
					require.EqualValues(t, 1, sends.Load())
					require.Equal(t, resilience.RequestSuccess, meta.Result)
				case "unsent":
					require.Zero(t, sends.Load())
					require.Equal(t, resilience.RequestFailed, meta.Result)
					require.Zero(t, meta.AccountID)
					require.Empty(t, rec.finishes)
				}
				if mode != "unsent" {
					require.Equal(t, account.ID, meta.AccountID)
				}
				if record && mode == "retry" {
					require.Len(t, rec.finishes, 2)
					require.Equal(t, schedquality.ClassUpstream, rec.finishes[0].Class)
					require.Equal(t, schedquality.ClassNone, rec.finishes[1].Class)
				}
			})
		}
	}
}

func TestErrclassRealWSIngressFailures(t *testing.T) {
	for _, route := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, mode := range []string{"disconnect", "output_disconnect", "completed_close", "client_disconnect", "deadline"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				withResilienceSettings(t, nil)
				svc, account, st, rec, sends, release := errclassWSFixture(t, mode)
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = route
				ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), st), 10*time.Second)
				defer cancel()
				st.InitDelivery(ctx, time.Now(), nil)
				server, done := startPassthroughLifecycleServer(t, ctx, svc, account)
				defer server.Close()
				client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
				require.NoError(t, err)
				defer client.CloseNow()
				require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"synthetic-model","input":"synthetic"}`)))
				for {
					_, message, readErr := client.Read(ctx)
					if readErr != nil {
						break
					}
					if mode == "client_disconnect" {
						_ = client.CloseNow()
						release()
						break
					}
					if strings.Contains(string(message), "response.completed") {
						_ = client.CloseNow()
						break
					}
				}
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("WS proxy did not finish")
				}
				require.Positive(t, sends.Load())
				require.NotEmpty(t, rec.finishes)
				want := schedquality.ClassUpstream
				switch mode {
				case "output_disconnect":
					want = schedquality.ClassInterrupted
				case "completed_close":
					want = schedquality.ClassNone
				case "client_disconnect":
					want = schedquality.ClassClientCancel
				case "deadline":
					want = schedquality.ClassUnknown
					if route == OpenAIWSIngressModePassthrough {
						// Passthrough uses the idle timeout as its first-output
						// fallback even when the configured first-output limit is 0.
						want = schedquality.ClassFirstOutputTimeout
					}
				}
				for _, sample := range rec.finishes {
					require.Equal(t, want, sample.Class)
				}
			})
		}
	}
}

func TestErrclassTransientProcessingFailure(t *testing.T) {
	body := []byte(`{"error":{"type":"server_error","code":"server_error","message":"An error occurred while processing your request"}}`)
	for _, event := range []bool{false, true} {
		failure := resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: 400, Body: body}
		if event {
			failure.Source = resilience.SourceStreamEvent
			failure.StatusCode = 200
			failure.Body = []byte(`{"type":"response.failed","response":` + string(body) + `}`)
		}
		got := classifySchedAttempt(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault}, &failure, false, false)
		require.Equal(t, schedquality.ClassUpstream, got.Class)
		require.True(t, got.Class.Counted())
	}
}

// Control frames bypass the relay's normal write_upstream stage. An upstream
// write error must still be attributed to the active attempt, not the client.
type errclassControlWriteFailureConn struct {
	*stagedPassthroughConn
}

func (c *errclassControlWriteFailureConn) WriteFrame(ctx context.Context, typ coderws.MessageType, payload []byte) error {
	if strings.Contains(string(payload), "session.update") {
		return io.ErrUnexpectedEOF
	}
	return c.stagedPassthroughConn.WriteFrame(ctx, typ, payload)
}

func TestErrclassPassthroughControlFrameUpstreamWriteFailure(t *testing.T) {
	withResilienceSettings(t, nil)
	upstream := newStagedPassthroughConn()
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	svc.openaiWSPassthroughDialer = &stagedPassthroughDialer{conn: &errclassControlWriteFailureConn{upstream}}
	rec := &errclassRecorder{}
	svc.SetSchedAttemptRecorder(rec)
	account := passthroughLifecycleAccount()
	st := resilience.NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
	ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), st), 5*time.Second)
	defer cancel()
	st.InitDelivery(ctx, time.Now(), nil)
	server, done := startPassthroughLifecycleServer(t, ctx, svc, account)
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"synthetic-model","input":"synthetic"}`)))
	select {
	case <-upstream.writes:
	case <-ctx.Done():
		t.Fatal("initial request was not sent")
	}
	upstream.Send(errclassWSCreated)
	_, _, err = client.Read(ctx)
	require.NoError(t, err)
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"session.update","session":{}}`)))
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("proxy did not finish after upstream write failure")
	}
	require.Len(t, rec.finishes, 1)
	require.Equal(t, schedquality.ClassUpstream, rec.finishes[0].Class)
	require.True(t, rec.finishes[0].Class.Counted())
}

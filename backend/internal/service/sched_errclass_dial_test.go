//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type errclassDialScript struct {
	conn       openAIWSClientConn
	status     int
	body       string
	err        error
	delay      time.Duration
	panicValue any
	calls      atomic.Int32
	started    time.Time
}

type errclassReplyWSConn struct{ *stagedPassthroughConn }

func (c *errclassReplyWSConn) WriteJSON(context.Context, any) error {
	c.Send(errclassWSCompleted)
	return nil
}
func (c *errclassReplyWSConn) WriteFrame(ctx context.Context, typ coderws.MessageType, body []byte) error {
	if err := c.stagedPassthroughConn.WriteFrame(ctx, typ, body); err != nil {
		return err
	}
	c.Send(errclassWSCompleted)
	return nil
}

func (d *errclassDialScript) Dial(ctx context.Context, _ string, _ http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	d.calls.Add(1)
	d.started = time.Now()
	if d.panicValue != nil {
		panic(d.panicValue)
	}
	if d.delay > 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return nil, 0, nil, ctx.Err()
		}
	}
	if d.err != nil {
		return nil, d.status, http.Header{}, &openAIWSHandshakeError{Body: []byte(d.body), Err: d.err}
	}
	return d.conn, http.StatusSwitchingProtocols, http.Header{}, nil
}

func errclassDialService(t *testing.T, dialer openAIWSClientDialer) (*OpenAIGatewayService, *Account, *resilience.RequestState, *errclassRecorder) {
	t.Helper()
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 0
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	t.Cleanup(pool.Close)
	rec := &errclassRecorder{}
	svc := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}, httpUpstream: &httpUpstreamRecorder{}, toolCorrector: NewCodexToolCorrector(), openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPool: pool, openaiWSPassthroughDialer: dialer, schedAttemptRecorder: rec}
	account := passthroughLifecycleAccount()
	account.Extra["responses_websockets_v2_enabled"] = true
	st := resilience.NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
	return svc, account, st, rec
}

func errclassCallHTTPWS(ctx context.Context, svc *OpenAIGatewayService, account *Account) (*OpenAIForwardResult, error) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	InstallDeliveryTracking(c, time.Now())
	return svc.forwardOpenAIWSV2(ctx, c, account, map[string]any{"model": "synthetic-model", "input": "synthetic", "stream": true}, "", "", "synthetic", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, true, "synthetic-model", "synthetic-model", time.Now(), 1, "", nil)
}

// Recovery sits above the production forwarding stack, just as in the handler.
func errclassCallIngress(t *testing.T, ctx context.Context, svc *OpenAIGatewayService, account *Account, turns int) any {
	t.Helper()
	type result struct{ panicValue any }
	done := make(chan result, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { done <- result{panicValue: recover()} }()
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, first, err := conn.Read(ctx)
		if err != nil {
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.WithContext(ctx)
		_ = svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "synthetic", first, nil)
	}))
	defer server.Close()
	client, _, err := coderws.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	for range turns {
		if client.Write(context.Background(), coderws.MessageText, []byte(`{"type":"response.create","model":"synthetic-model","input":"synthetic"}`)) != nil {
			break
		}
		_, _, err = client.Read(context.Background())
		if err != nil {
			break
		}
	}
	_ = client.CloseNow()
	select {
	case result := <-done:
		return result.panicValue
	case <-time.After(10 * time.Second):
		t.Fatal("ingress did not return")
		return nil
	}
}

func TestErrclassWSDialOwnership(t *testing.T) {
	for _, route := range []string{"http_ws", OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, mode := range []string{"handshake503", "success", "reuse", "canceled"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				withResilienceSettings(t, nil)
				upstream := newStagedPassthroughConn()
				dialer := &errclassDialScript{conn: &errclassReplyWSConn{upstream}}
				turns := 1
				switch mode {
				case "handshake503":
					dialer.status = 503
					dialer.err = errors.New("synthetic handshake failure")
				case "canceled":
					dialer.err = context.Canceled
				case "reuse":
					turns = 2
				}
				svc, account, st, rec := errclassDialService(t, dialer)
				ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), st), 8*time.Second)
				defer cancel()
				st.InitDelivery(ctx, time.Now(), nil)
				if route == "http_ws" {
					for range turns {
						_, _ = errclassCallHTTPWS(ctx, svc, account)
					}
				} else {
					account.Extra["openai_apikey_responses_websockets_v2_mode"] = route
					require.Nil(t, errclassCallIngress(t, ctx, svc, account, turns))
				}
				require.EqualValues(t, 1, dialer.calls.Load())
				switch mode {
				case "handshake503":
					require.Len(t, rec.finishes, 1)
					require.Equal(t, q.ClassUpstream, rec.finishes[0].Class)
					require.True(t, rec.finishes[0].Class.Counted())
					require.Equal(t, 503, rec.finishes[0].HTTPStatus)
					require.Equal(t, q.OriginUpstream, rec.finishes[0].StatusOrigin)
					meta, _ := st.ClaimGatewayRequest(true)
					require.Zero(t, meta.AccountID, "dial failure must not advance business sends")
				case "canceled":
					require.Empty(t, rec.starts)
					require.Empty(t, rec.finishes)
				default:
					require.Len(t, rec.finishes, turns)
					for _, f := range rec.finishes {
						require.Equal(t, q.ClassNone, f.Class)
					}
				}
			})
		}
	}
}

func TestErrclassWSDialLocalWaitHasNoSample(t *testing.T) {
	for _, route := range []string{"http_ws", OpenAIWSIngressModeCtxPool} {
		t.Run(route, func(t *testing.T) {
			withResilienceSettings(t, nil)
			dialer := &errclassDialScript{conn: newStagedPassthroughConn()}
			svc, account, st, rec := errclassDialService(t, dialer)
			lease, err := svc.openaiWSPool.Acquire(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "ws://fixture/responses"})
			require.NoError(t, err)
			defer lease.Release()
			ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), st), 50*time.Millisecond)
			defer cancel()
			if route == "http_ws" {
				_, err = errclassCallHTTPWS(ctx, svc, account)
				require.Error(t, err)
			} else {
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = route
				require.Nil(t, errclassCallIngress(t, ctx, svc, account, 1))
			}
			require.EqualValues(t, 1, dialer.calls.Load(), "only the unrelated lease dialed")
			require.Empty(t, rec.starts)
			require.Empty(t, rec.finishes)
		})
	}
}

func TestErrclassSlowHandshakeStart(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		upstream := newStagedPassthroughConn()
		upstream.Send(errclassWSCompleted)
		dialer := &errclassDialScript{conn: upstream, delay: 2 * time.Second}
		svc, account, st, _ := errclassDialService(t, dialer)
		defer svc.openaiWSPool.Close()
		recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
		var rows []q.AttemptSample
		recorder.SetObserver(func(row q.AttemptSample) {
			if !row.FinishedAt.IsZero() {
				rows = append(rows, row)
			}
		})
		svc.SetSchedAttemptRecorder(recorder)
		started := time.Now()
		result, err := errclassCallHTTPWS(resilience.WithRequestState(context.Background(), st), svc, account)
		require.NoError(t, err)
		require.True(t, result.CompletedForDelivery())
		require.Len(t, rows, 1)
		require.Equal(t, started, dialer.started)
		require.Equal(t, dialer.started, rows[0].StartedAt)
		require.GreaterOrEqual(t, rows[0].FinishedAt.Sub(rows[0].StartedAt), 2*time.Second)
	})
}

func TestErrclassWSDialDoesNotFallbackHTTP(t *testing.T) {
	for _, status := range []int{426, 0} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			withResilienceSettings(t, nil)
			dialer := &errclassDialScript{status: status, err: errors.New("synthetic dial failure")}
			svc, account, st, rec := errclassDialService(t, dialer)
			ctx := resilience.WithRequestState(context.Background(), st)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			_, err := svc.Forward(ctx, c, account, []byte(`{"model":"synthetic-model","input":"synthetic","stream":true}`))
			require.Error(t, err)
			require.Positive(t, dialer.calls.Load())
			require.Len(t, rec.finishes, int(dialer.calls.Load()), "every physical retry owns its own sample")
			require.Empty(t, svc.httpUpstream.(*httpUpstreamRecorder).requests)
		})
	}
}

func TestErrclassWSHandshakeUsesHTTPClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		class  q.ErrorClass
	}{
		{401, `{"error":{"message":"Invalid API key"}}`, q.ClassAccountAccess},
		{403, `{"error":{"message":"Forbidden"}}`, q.ClassUnknown},
		{429, `{"error":{"code":"account_rate_limit_exceeded","message":"Limit reached"}}`, q.ClassAccountCapacity},
		{426, `{"error":{"message":"Upgrade required"}}`, q.ClassUnknown},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			withResilienceSettings(t, nil)
			dialer := &errclassDialScript{status: tc.status, body: tc.body, err: errors.New("synthetic handshake rejection")}
			svc, account, st, rec := errclassDialService(t, dialer)
			_, err := errclassCallHTTPWS(resilience.WithRequestState(context.Background(), st), svc, account)
			require.Error(t, err)
			require.Len(t, rec.finishes, 1)
			require.Equal(t, tc.class, rec.finishes[0].Class)
			require.Equal(t, tc.status, rec.finishes[0].HTTPStatus)
			require.Equal(t, q.OriginUpstream, rec.finishes[0].StatusOrigin)
		})
	}
}

type errclassPanicWSConn struct {
	*stagedPassthroughConn
	value any
}

func (c *errclassPanicWSConn) WriteJSON(context.Context, any) error { panic(c.value) }
func (c *errclassPanicWSConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	panic(c.value)
}

func TestErrclassWSPanicOwnership(t *testing.T) {
	for _, route := range []string{"http_ws", OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, stage := range []string{"dial", "send"} {
			t.Run(route+"/"+stage, func(t *testing.T) {
				withResilienceSettings(t, nil)
				value := &struct{ message string }{"synthetic WS panic"}
				dialer := &errclassDialScript{conn: &errclassPanicWSConn{stagedPassthroughConn: newStagedPassthroughConn(), value: value}}
				if stage == "dial" {
					dialer.panicValue = value
				}
				svc, account, st, rec := errclassDialService(t, dialer)
				ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), st), 8*time.Second)
				defer cancel()
				if route == "http_ws" {
					require.PanicsWithValue(t, value, func() { _, _ = errclassCallHTTPWS(ctx, svc, account) })
				} else {
					account.Extra["openai_apikey_responses_websockets_v2_mode"] = route
					require.Same(t, value, errclassCallIngress(t, ctx, svc, account, 1))
				}
				FinishSchedAttemptAtHandlerExit(ctx)
				require.Len(t, rec.finishes, 1)
				require.Equal(t, q.ClassGateway, rec.finishes[0].Class)
				require.False(t, rec.finishes[0].Class.Counted())
			})
		}
	}
}

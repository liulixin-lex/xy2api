//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type capabilityReadErrorDialer struct {
	inner errclassRealWSDialer
	reads atomic.Int32
	errs  chan error
}

func (d *capabilityReadErrorDialer) Dial(ctx context.Context, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	c, status, responseHeaders, err := d.inner.Dial(ctx, url, headers, proxy)
	if err != nil {
		return c, status, responseHeaders, err
	}
	return &capabilityReadErrorConn{openAIWSClientConn: c, owner: d}, status, responseHeaders, nil
}

type capabilityReadErrorConn struct {
	openAIWSClientConn
	owner *capabilityReadErrorDialer
}

func (c *capabilityReadErrorConn) RequiresReaderLoop() bool { return true }
func (c *capabilityReadErrorConn) ReadMessage(ctx context.Context) ([]byte, error) {
	b, err := c.openAIWSClientConn.ReadMessage(ctx)
	if err == nil {
		c.owner.reads.Add(1)
	} else {
		select {
		case c.owner.errs <- err:
		default:
		}
	}
	return b, err
}

func TestCapabilityWSReadFailureFinality(t *testing.T) {
	for _, route := range []string{"http_ws", OpenAIWSIngressModeCtxPool} {
		for _, sampled := range []bool{false, true} {
			for _, mode := range []string{"local_limit", "disconnect", "remote_1009"} {
				name := route + "/unsampled/" + mode
				if sampled {
					name = route + "/sampled/" + mode
				}
				t.Run(name, func(t *testing.T) {
					withResilienceSettings(t, nil)
					refusal := capabilityProvisionalRefusal
					if route == "http_ws" {
						// HTTP->WS exits immediately on type:error. A nonterminal
						// envelope with a root error exercises its pending evidence
						// followed by a real read error without changing that policy.
						refusal = `{"type":"response.created","response":{"id":"fixture","status":"in_progress"},"error":{"code":"unsupported_protocol","message":"unsupported transport"}}`
					}
					require.True(t, ClassifyOpenAIResponsesTerminal("", []byte(refusal)).QualityFailure())
					require.True(t, ClassifyUpstreamCapabilityEventFailure([]byte(refusal)).Markable())
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
						if conn.Write(ctx, coderws.MessageText, []byte(refusal)) != nil {
							return
						}
						switch mode {
						case "local_limit":
							_ = conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.output_text.delta","delta":"`+strings.Repeat("x", int(openAIWSMessageReadLimitBytes))+`"}`))
						case "remote_1009":
							_ = conn.Close(coderws.StatusMessageTooBig, "remote limit")
						}
					}))
					defer upstream.Close()
					dialer := &capabilityReadErrorDialer{inner: errclassRealWSDialer{url: "ws" + strings.TrimPrefix(upstream.URL, "http")}, errs: make(chan error, 1)}
					svc, account, state, rec := errclassDialService(t, dialer)
					account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
					repo := &capabilityRepo{accounts: map[int64]*Account{account.ID: account}}
					svc.accountRepo = repo
					if !sampled {
						svc.schedAttemptRecorder = nil
						state = resilience.NewRequestState()
					}
					ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), state), 8*time.Second)
					defer cancel()
					var finalErr error
					if route == "http_ws" {
						_, finalErr = errclassCallHTTPWS(ctx, svc, account)
					} else {
						done := make(chan error, 1)
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							peer, err := coderws.Accept(w, r, nil)
							if err != nil {
								done <- err
								return
							}
							defer peer.CloseNow()
							c, _ := gin.CreateTestContext(httptest.NewRecorder())
							c.Request = r.WithContext(ctx)
							done <- svc.ProxyResponsesWebSocketFromClient(ctx, c, peer, account, "synthetic", []byte(`{"type":"response.create","model":"synthetic-model","input":"synthetic"}`), nil)
						}))
						defer server.Close()
						peer, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
						require.NoError(t, err)
						defer peer.CloseNow()
						// Stay connected after the provisional error. Closing here
						// would exercise cancellation instead of the read-error exit.
						for {
							if _, _, err := peer.Read(ctx); err != nil {
								break
							}
						}
						select {
						case finalErr = <-done:
						case <-ctx.Done():
							t.Fatal("production WS turn did not finish")
						}
					}
					require.Error(t, finalErr)
					require.NoError(t, ctx.Err(), "the actual read failure must precede any fixture deadline")
					require.EqualValues(t, 1, dialer.reads.Load(), "refusal was read before the failing frame")
					var readErr error
					select {
					case readErr = <-dialer.errs:
					case <-ctx.Done():
						t.Fatal("upstream reader did not report its terminal error")
					}
					require.Equal(t, mode == "local_limit", errors.Is(readErr, coderws.ErrMessageTooBig))
					if mode == "remote_1009" {
						require.Equal(t, coderws.StatusMessageTooBig, coderws.CloseStatus(readErr))
					}
					require.True(t, state.SemanticOutputAt().IsZero())
					require.Equal(t, mode != "local_limit", account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()) != nil)
					if mode == "local_limit" {
						require.Zero(t, repo.writes)
					}
					if sampled {
						require.Len(t, rec.finishes, 1)
					} else {
						require.Empty(t, rec.starts, "independent learning must not allocate a quality capture")
					}
					require.Nil(t, state.CurrentAttempt())
				})
			}
		}
	}
}

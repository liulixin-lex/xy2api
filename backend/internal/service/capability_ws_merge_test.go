//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type capabilityPanicWriter struct {
	header http.Header
	value  any
	writes int
}

func (w *capabilityPanicWriter) Header() http.Header { return w.header }
func (*capabilityPanicWriter) WriteHeader(int)       {}
func (w *capabilityPanicWriter) Write([]byte) (int, error) {
	w.writes++
	panic(w.value)
}
func (*capabilityPanicWriter) Flush() {}

type capabilityPanicReadConn struct {
	*openAIWSCaptureConn
	value any
	reads int
}

func (c *capabilityPanicReadConn) ReadMessage(ctx context.Context) ([]byte, error) {
	c.reads++
	if c.reads == 2 {
		panic(c.value)
	}
	return c.openAIWSCaptureConn.ReadMessage(ctx)
}

func TestCapabilityWSPanicAfterRefusalDoesNotLearn(t *testing.T) {
	for _, route := range []string{"http_ws", OpenAIWSIngressModeCtxPool} {
		t.Run(route, func(t *testing.T) {
			withResilienceSettings(t, nil)
			value := &struct{ message string }{"panic after provisional capability evidence"}
			conn := &capabilityPanicReadConn{
				openAIWSCaptureConn: &openAIWSCaptureConn{events: [][]byte{[]byte(capabilityProvisionalRefusal)}},
				value:               value,
			}
			svc, account, state, rec := errclassDialService(t, &errclassDialScript{conn: conn})
			repo := &capabilityRepo{accounts: map[int64]*Account{account.ID: account}}
			svc.accountRepo = repo
			ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), state), 8*time.Second)
			defer cancel()
			if route == "http_ws" {
				conn.events = [][]byte{[]byte(capabilityFinalRefusal)}
				// The real HTTP emitter panics after both learners have observed
				// the refusal event, before a normal final result can be returned.
				writer := &capabilityPanicWriter{header: http.Header{}, value: value}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				require.PanicsWithValue(t, value, func() {
					_, _ = svc.forwardOpenAIWSV2(ctx, c, account, map[string]any{"model": "synthetic-model", "input": "synthetic", "stream": true}, "", "", "synthetic", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, true, "synthetic-model", "synthetic-model", time.Now(), 1, "", nil)
				})
				require.Positive(t, writer.writes)
				require.Equal(t, 1, conn.reads)
			} else {
				// ctx_pool continues after the recoverable error; its next real
				// upstream read panics while provisional evidence is pending.
				account.Extra["openai_apikey_responses_websockets_v2_mode"] = route
				require.Same(t, value, errclassCallIngress(t, ctx, svc, account, 1))
				require.Equal(t, 2, conn.reads)
			}
			FinishSchedAttemptAtHandlerExit(ctx)
			require.Len(t, rec.starts, 1)
			require.Len(t, rec.finishes, 1, "panic owns exactly one sample, even after handler cleanup")
			require.Equal(t, q.ClassGateway, rec.finishes[0].Class)
			require.Equal(t, "GATEWAY_PANIC", rec.finishes[0].Detail)
			require.Zero(t, repo.writes)
			require.Nil(t, account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()))
			require.Nil(t, state.CurrentAttempt())
		})
	}
}

func TestCapabilityWSPhysicalDialLearningBoundary(t *testing.T) {
	for _, route := range []string{"http_ws", OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		for _, tc := range []struct {
			name   string
			status int
			body   string
			mark   bool
		}{
			{"network_without_response", 0, "", false},
			{"model_refusal_handshake", 404, `{"error":{"code":"model_not_found"}}`, true},
			{"request_feature_handshake", 400, `{"error":{"message":"This upstream supports text and https input_image content only"}}`, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				withResilienceSettings(t, nil)
				dialer := &errclassDialScript{status: tc.status, body: tc.body, err: errors.New("synthetic physical dial failure")}
				svc, account, state, rec := errclassDialService(t, dialer)
				repo := &capabilityRepo{accounts: map[int64]*Account{account.ID: account}}
				svc.accountRepo = repo
				ctx, cancel := context.WithTimeout(resilience.WithRequestState(context.Background(), state), 8*time.Second)
				defer cancel()
				if route == "http_ws" {
					_, err := errclassCallHTTPWS(ctx, svc, account)
					require.Error(t, err)
				} else {
					account.Extra["openai_apikey_responses_websockets_v2_mode"] = route
					require.Nil(t, errclassCallIngress(t, ctx, svc, account, 1))
				}
				require.Positive(t, dialer.calls.Load(), "must reach the actual physical dial")
				require.Len(t, rec.finishes, int(dialer.calls.Load()))
				meta, _ := state.ClaimGatewayRequest(true)
				require.Zero(t, meta.AccountID, "a failed handshake never sent a business request")
				require.Equal(t, tc.mark, account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()) != nil)
				if tc.mark {
					require.Equal(t, 1, repo.writes)
					require.Equal(t, q.ClassCapability, rec.finishes[0].Class)
				} else {
					require.Zero(t, repo.writes)
				}
			})
		}
	}
}

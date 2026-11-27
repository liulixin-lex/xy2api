package service

import (
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
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	pluginv1 "github.com/liulixin-lex/xy2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type dispatchServiceTransport func(*http.Request) (*http.Response, error)

func (f dispatchServiceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type dispatchSharedStub struct{ base http.RoundTripper }

func (s dispatchSharedStub) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return resilience.ObserveDispatchTransport(s.base, "direct").RoundTrip(r)
}
func (s dispatchSharedStub) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(r, p, id, n)
}
func dispatchState(t *testing.T) (context.Context, *resilience.RequestState) {
	t.Helper()
	st := resilience.NewRequestState()
	return resilience.WithRequestState(t.Context(), st), st
}

func TestDispatchCountDirectClients(t *testing.T) {
	for _, path := range []string{"codex_ticket", "codex_proxy", "codex_models", "agent_identity"} {
		t.Run(path, func(t *testing.T) {
			ctx, st := dispatchState(t)
			calls := 0
			sentinel := errors.New("dial failed")
			base := dispatchServiceTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.EqualValues(t, calls, *st.Timing().DispatchCount)
				return nil, sentinel
			})
			client := &http.Client{Transport: base, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			req, err := http.NewRequestWithContext(ctx, "GET", "https://upstream.example/test", nil)
			require.NoError(t, err)
			for i := 0; i < 2; i++ {
				_, err = doObservedHTTP(client, req, path)
				require.ErrorIs(t, err, sentinel)
			}
			require.Equal(t, 2, calls)
			require.EqualValues(t, 2, *st.Timing().DispatchCount)
			require.Equal(t, time.Minute, client.Timeout)
			_, ok := client.Transport.(dispatchServiceTransport)
			require.True(t, ok, "cached client must not be modified")
		})
	}
}

func TestDispatchCountCodexTicketBranches(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, observed := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "shared", true: "chained"}[direct], map[bool]string{false: "background", true: "request"}[observed]}, "/"), func(t *testing.T) {
				ctx, st := dispatchState(t)
				if !observed {
					ctx = t.Context()
				}
				var calls atomic.Int64
				account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "fixture", "chatgpt_account_id": "fixture-account"}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				proxyURL := ""
				if direct {
					proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						require.Equal(t, http.MethodConnect, r.Method)
						w.WriteHeader(http.StatusBadGateway)
					}))
					defer proxy.Close()
					svc.cfg.Gateway.OpenAICodexTicket.HarvestDialProxyURL = proxy.URL
					proxyURL = "http://proxy.example:8080"
				} else {
					svc.httpUpstream = dispatchSharedStub{base: dispatchServiceTransport(func(*http.Request) (*http.Response, error) {
						calls.Add(1)
						return codexModelResponse("fixture-model"), nil
					})}
				}
				_, status, err := svc.fireCodexAccountTicketProbe(ctx, account, "fixture", "fixture-model", proxyURL, "", time.Second)
				if direct {
					require.Error(t, err)
					require.Zero(t, status)
				} else {
					require.NoError(t, err)
					require.Equal(t, 200, status)
				}
				require.EqualValues(t, 1, calls.Load())
				want := int64(0)
				if observed {
					want = 1
				}
				require.Equal(t, want, *st.Timing().DispatchCount)
			})
		}
	}
}

func TestDispatchCountCodexProxyBranches(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, observed := range []bool{false, true} {
			ctx, st := dispatchState(t)
			if !observed {
				ctx = t.Context()
			}
			var calls atomic.Int64
			body := "ip=192.0.2.1\nloc=US\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = io.WriteString(w, body) }))
			defer server.Close()
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			svc.cfg.Security.ProxyProbe.URLs = []config.ProbeURLConfig{{URL: server.URL}}
			if direct {
				svc.cfg.Gateway.OpenAICodexTicket.HarvestDialProxyURL = server.URL
			} else {
				svc.httpUpstream = dispatchSharedStub{base: dispatchServiceTransport(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
			}
			result := svc.probeCodexProxyExit(ctx, "")
			require.True(t, result.Success)
			require.Equal(t, "192.0.2.1", result.IPAddress)
			require.EqualValues(t, 1, calls.Load())
			want := int64(0)
			if observed {
				want = 1
			}
			require.Equal(t, want, *st.Timing().DispatchCount)
		}
	}
}

type dispatchPluginAPI struct {
	pluginv1.TransportPluginClient
	stream *dispatchPluginStream
	err    error
}

func (a dispatchPluginAPI) Forward(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[pluginv1.ForwardRequest, pluginv1.ForwardResponse], error) {
	return a.stream, a.err
}

type dispatchPluginStream struct {
	grpc.ClientStream
	starts      atomic.Int64
	received    int
	failSend    bool
	frameError  *pluginv1.ForwardResponseError
	redirectURL string
}

func (s *dispatchPluginStream) Send(r *pluginv1.ForwardRequest) error {
	if r.GetStart() != nil {
		s.starts.Add(1)
		if s.failSend {
			return errors.New("send failed")
		}
	}
	return nil
}
func (s *dispatchPluginStream) CloseSend() error { return nil }
func (s *dispatchPluginStream) Recv() (*pluginv1.ForwardResponse, error) {
	s.received++
	if s.received == 1 {
		if s.frameError != nil {
			return &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Error{Error: s.frameError}}, nil
		}
		if s.redirectURL != "" {
			req, err := http.NewRequest(http.MethodPost, s.redirectURL+"/start", strings.NewReader("fixture"))
			if err != nil {
				return nil, err
			}
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err != nil {
				return nil, err
			}
			_, readErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		return &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: 201, ContentLength: 0}}}, nil
	}
	return &pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{}}}, nil
}

func TestDispatchCountPluginRouting(t *testing.T) {
	for _, mode := range []string{"handled", "unhandled", "unavailable", "draining", "stream_failed", "send_failed", "invalid_request", "prepare_failed", "upstream_failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, st := dispatchState(t)
			stream := &dispatchPluginStream{failSend: mode == "send_failed"}
			if mode == "prepare_failed" || mode == "upstream_failed" {
				stream.frameError = &pluginv1.ForwardResponseError{Code: "FIXTURE_ERROR", Message: "synthetic failure", RequestSent: mode == "upstream_failed"}
			}
			api := dispatchPluginAPI{stream: stream}
			if mode == "stream_failed" {
				api.err = errors.New("stream failed")
			}
			runtime := &pluginRuntime{client: &hcplugin.Client{}, api: api}
			runtime.draining.Store(mode == "draining")
			manager := &PluginManager{}
			route := &pluginRoute{rolloutPercent: 100, runtime: runtime}
			if mode == "unavailable" {
				route.runtime = nil
			}
			if mode != "unhandled" {
				manager.route.Store(route)
			}
			calls := 0
			svc := &OpenAIGatewayService{pluginManager: manager, httpUpstream: dispatchSharedStub{base: dispatchServiceTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("shared"))}, nil
			})}}
			req, err := http.NewRequestWithContext(ctx, "POST", "https://upstream.example/test", nil)
			require.NoError(t, err)
			if mode == "invalid_request" {
				req.URL = nil
			}
			resp, err := svc.doOpenAIUpstream(req, "", &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
			switch mode {
			case "handled", "unhandled":
				require.NoError(t, err)
				require.Equal(t, 201, resp.StatusCode)
				_, err = io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
			default:
				require.Error(t, err)
				require.Nil(t, resp)
			}
			require.Equal(t, stream.starts.Load() == 0, st.DispatchCountExact())
			require.Equal(t, stream.starts.Load() != 0, st.Timing().DispatchCountInexact)
			require.Equal(t, st.DispatchCountExact(), st.Timing().DispatchCountExact())
			if mode == "unhandled" {
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls)
			}
			if mode == "handled" || mode == "unhandled" || mode == "send_failed" || mode == "upstream_failed" {
				require.EqualValues(t, 1, *st.Timing().DispatchCount)
			} else {
				require.Zero(t, *st.Timing().DispatchCount)
			}
		})
	}
}

func TestDispatchCountWebSocketApplicationFrames(t *testing.T) {
	ctx, st := dispatchState(t)
	fake := &openAIWSFakeConn{}
	lease := &openAIWSConnLease{conn: newOpenAIWSConn("fixture", 7, fake, nil)}
	for i := 1; i <= 3; i++ {
		require.NoError(t, lease.WriteJSONWithContextTimeout(ctx, map[string]any{"type": "response.create", "generate": i != 1}, time.Second))
		require.EqualValues(t, len(fake.payload), *st.Timing().DispatchCount)
	}
	require.Error(t, lease.WriteJSONWithContextTimeout(ctx, make(chan int), time.Second))
	require.EqualValues(t, 3, *st.Timing().DispatchCount, "JSON preparation must not count")
	require.NoError(t, fake.Ping(ctx))
	require.EqualValues(t, 3, *st.Timing().DispatchCount)
	_, empty := dispatchState(t)
	require.Error(t, (&openAIWSConnLease{}).WriteJSONWithContextTimeout(resilience.WithRequestState(ctx, empty), map[string]any{}, time.Second))
	require.Zero(t, *empty.Timing().DispatchCount)
	passthrough := newStagedPassthroughConn()
	route := &openAIWSTierRoute{conn: passthrough}
	turn := &schedWSTurnCapture{}
	svc := &OpenAIGatewayService{}
	for i := 0; i < 2; i++ {
		payload := []byte("{\"type\":\"response.create\"}")
		turn.begin(svc, ctx, &Account{ID: 7}, payload)
		before := *st.Timing().DispatchCount
		require.NoError(t, route.WriteFrame(ctx, coderws.MessageText, payload))
		require.EqualValues(t, before+1, *st.Timing().DispatchCount)
		require.Equal(t, payload, <-passthrough.writes)
	}
	sessionUpdate := []byte("{\"type\":\"session.update\"}")
	require.NoError(t, route.WriteFrame(ctx, coderws.MessageText, sessionUpdate))
	require.EqualValues(t, 6, *st.Timing().DispatchCount, "session.update is an application request frame")
	require.Equal(t, sessionUpdate, <-passthrough.writes)
	binaryFrame := []byte("{\"type\":\"binary.fixture\"}")
	require.NoError(t, route.WriteFrame(ctx, coderws.MessageBinary, binaryFrame))
	require.EqualValues(t, 7, *st.Timing().DispatchCount, "binary application frames count too")
	require.Equal(t, binaryFrame, <-passthrough.writes)
	require.NoError(t, route.Close())
	require.Error(t, route.WriteFrame(ctx, coderws.MessageText, []byte("{\"type\":\"response.create\"}")))
	require.EqualValues(t, 7, *st.Timing().DispatchCount)
}

func TestDispatchCountProviderForwarding(t *testing.T) {
	for _, provider := range []string{"Claude", "GeminiChat", "GeminiNative", "OpenAI"} {
		t.Run(provider, func(t *testing.T) {
			ctx, st := dispatchState(t)
			cfg := &config.Config{}
			cfg.Gateway.MaxLineSize = defaultMaxLineSize
			calls := 0
			upstream := dispatchSharedStub{base: dispatchServiceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				require.EqualValues(t, calls, *st.Timing().DispatchCount)
				body, contentType := "", "application/json"
				switch provider {
				case "Claude":
					body = "{\"id\":\"fixture\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}],\"model\":\"claude-sonnet-4\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}"
				case "GeminiChat":
					body = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":3}}}\n\ndata: [DONE]\n\n"
					contentType = "text/event-stream"
				case "GeminiNative":
					body = "{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":3}}"
				case "OpenAI":
					body = "{\"id\":\"fixture\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			var input, output int
			switch provider {
			case "Claude":
				body := []byte("{\"model\":\"claude-sonnet-4\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\"max_tokens\":20}")
				c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(string(body))).WithContext(ctx)
				svc := &GatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: upstream, rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}}
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, err)
				result, err := svc.Forward(ctx, c, newAnthropicAPIKeyAccountForTest(), parsed)
				require.NoError(t, err)
				input, output = result.Usage.InputTokens, result.Usage.OutputTokens
			case "GeminiChat", "GeminiNative":
				svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream, cfg: cfg}
				account := &Account{ID: 7, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "fixture", "project_id": "fixture-project"}}
				body := []byte("{\"model\":\"gemini-2.5-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}")
				c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))).WithContext(ctx)
				var result *ForwardResult
				var err error
				if provider == "GeminiChat" {
					result, err = svc.ForwardAsChatCompletions(ctx, c, account, body)
				} else {
					account.Type = AccountTypeAPIKey
					account.Credentials = map[string]any{"api_key": "fixture"}
					body = []byte("{\"contents\":[{\"role\":\"user\",\"parts\":[{\"text\":\"hello\"}]}]}")
					result, err = svc.ForwardNative(ctx, c, account, "gemini-2.5-flash", "generateContent", false, body)
				}
				require.NoError(t, err)
				input, output = result.Usage.InputTokens, result.Usage.OutputTokens
			case "OpenAI":
				svc := newOpenAIRecordUsageServiceForTest(nil, nil, nil, nil)
				svc.httpUpstream = upstream
				body := []byte("{\"model\":\"gpt-5.1\",\"input\":\"hello\"}")
				c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
				account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://upstream.example"}}
				result, err := svc.Forward(ctx, c, account, body)
				require.NoError(t, err)
				input, output = result.Usage.InputTokens, result.Usage.OutputTokens
			}
			require.Equal(t, 1, calls)
			require.EqualValues(t, calls, *st.Timing().DispatchCount)
			require.Equal(t, 7, input)
			require.Equal(t, 3, output)
			require.Equal(t, 200, recorder.Code)
			require.Contains(t, recorder.Body.String(), "hello")
		})
	}
}

type dispatchRealtimeConn struct {
	openAIWSFakeConn
	writes chan struct{}
}

func (c *dispatchRealtimeConn) WriteJSON(context.Context, any) error {
	c.writes <- struct{}{}
	return nil
}
func (c *dispatchRealtimeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDispatchCountGrokRealtimeFrames(t *testing.T) {
	ctx, st := dispatchState(t)
	conn := &dispatchRealtimeConn{writes: make(chan struct{}, 2)}
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = socket.CloseNow() }()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		_, _ = (&OpenAIGatewayService{}).ProxyGrokRealtimeConn(ctx, c, socket, &GrokRealtimeUpstream{conn: conn})
		close(finished)
	}))
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	for i := 1; i <= 2; i++ {
		require.NoError(t, client.Write(ctx, coderws.MessageText, []byte("{\"type\":\"response.create\"}")))
		select {
		case <-conn.writes:
		case <-time.After(time.Second):
			t.Fatal("frame not sent")
		}
		require.EqualValues(t, i, *st.Timing().DispatchCount)
	}
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte("invalid JSON")))
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("relay did not reject invalid JSON")
	}
	require.EqualValues(t, 2, *st.Timing().DispatchCount)
}

func TestDispatchCountPluginRedirectIsInexact(t *testing.T) {
	for _, prepared := range []bool{true, false} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			var physical atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				physical.Add(1)
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/finish", http.StatusTemporaryRedirect)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			ctx, state := dispatchState(t)
			stream := &dispatchPluginStream{redirectURL: server.URL}
			if !prepared {
				stream.frameError = &pluginv1.ForwardResponseError{Code: "LOCAL_PREPARE", Message: "preparation failed", RequestSent: false}
			}
			runtime := &pluginRuntime{api: dispatchPluginAPI{stream: stream}}
			require.True(t, runtime.beginRequest())
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
			require.NoError(t, err)
			response, err := runtime.roundTrip(ctx, req, "", &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
			if prepared {
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.EqualValues(t, 2, physical.Load())
				require.EqualValues(t, 1, *state.Timing().DispatchCount)
			} else {
				runtime.finishRequest()
				require.Error(t, err)
				require.Zero(t, physical.Load())
				require.Zero(t, *state.Timing().DispatchCount)
			}
			require.False(t, state.DispatchCountExact())
			require.True(t, state.Timing().DispatchCountInexact)
			require.False(t, state.Timing().DispatchCountExact())
			require.Contains(t, string(state.Timing().DispatchPhases), "\"count_inexact\":true")
			t.Logf("physical_sends=%d dispatch_count=%d count_inexact=%t", physical.Load(), *state.Timing().DispatchCount, state.Timing().DispatchCountInexact)
		})
	}
}

func TestDispatchCountPerWebSocketTurn(t *testing.T) {
	for _, mode := range []string{"pooled", "passthrough"} {
		t.Run(mode, func(t *testing.T) {
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			state.InitDelivery(ctx, time.Now(), nil)
			state.EnableGatewayRequest(time.Now())
			fake := &openAIWSFakeConn{}
			lease := &openAIWSConnLease{conn: newOpenAIWSConn("fixture", 7, fake, nil)}
			raw := newStagedPassthroughConn()
			route := &openAIWSTierRoute{conn: raw}
			defer func() { require.NoError(t, route.Close()) }()
			send := func() {
				if mode == "pooled" {
					require.NoError(t, lease.WriteJSONContext(ctx, map[string]any{"type": "response.create"}))
				} else {
					require.NoError(t, route.WriteFrame(ctx, coderws.MessageText, []byte("{\"type\":\"response.create\"}")))
					<-raw.writes
				}
			}
			state.BeginGatewayTurn(time.Now())
			firstID := state.GatewayRequestID()
			send()
			state.MarkDispatchCountInexact()
			state.NoteWireStatus(200)
			state.NoteKeepaliveCommit(time.Now())
			first := state.Timing()
			require.EqualValues(t, 1, *first.DispatchCount)
			state.BeginGatewayTurn(time.Now())
			require.NotEqual(t, firstID, state.GatewayRequestID())
			next := state.Timing()
			require.Zero(t, *next.DispatchCount)
			require.True(t, next.DispatchCountExact())
			require.Nil(t, next.PreDispatchMs)
			require.Nil(t, next.WireStatus)
			require.Nil(t, next.KeepaliveCommitMs)
			send()
			require.EqualValues(t, 1, *state.Timing().DispatchCount)
			state.BeginGatewayTurn(time.Now())
			state.FinishRejectedDispatch(time.Now())
			require.Zero(t, *state.Timing().DispatchCount, "third turn rejected before send")
			require.True(t, state.DispatchCountExact())
			require.NotContains(t, string(state.Timing().DispatchPhases), "count_inexact")
			require.EqualValues(t, 1, *first.DispatchCount, "previous snapshot stays frozen")
			require.False(t, first.DispatchCountExact())
			t.Log("turn1=1 turn2=1 turn3_rejected=0")
		})
	}
}

func TestDispatchCountWebSocketSingleEncodingAndBackground(t *testing.T) {
	ctx, state := dispatchState(t)
	fake := &openAIWSFakeConn{}
	lease := &openAIWSConnLease{conn: newOpenAIWSConn("fixture", 7, fake, nil)}
	calls := 0
	value := dispatchMarshalOnce{calls: &calls}
	require.NoError(t, lease.WriteJSONContext(ctx, value))
	require.Equal(t, 1, calls)
	require.Equal(t, []byte("{\"type\":\"response.create\"}"), fake.payload[0])
	require.EqualValues(t, 1, *state.Timing().DispatchCount)
	require.NoError(t, writeObservedWSFrame(context.Background(), fake, coderws.MessageText, []byte("{}")))
	require.Len(t, fake.payload, 2)
	require.EqualValues(t, 1, *state.Timing().DispatchCount)
}

type dispatchMarshalOnce struct{ calls *int }

func (v dispatchMarshalOnce) MarshalJSON() ([]byte, error) {
	*v.calls++
	return []byte("{\"type\":\"response.create\"}"), nil
}

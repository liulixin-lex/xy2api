package handler

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeRecoveryHTTPTransport struct {
	service.HTTPUpstream
	client *http.Client
}

func (u *nativeRecoveryHTTPTransport) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(r)
}

type nativeRecoveryFixture struct {
	handler         *OpenAIGatewayHandler
	server          *httptest.Server
	upstream        *httptest.Server
	scope           responseturn.Scope
	createCount     atomic.Int64
	wsCreateCount   atomic.Int64
	cancelCount     atomic.Int64
	pollCount       atomic.Int64
	createCancelled atomic.Bool
	accessRevoked   atomic.Bool
	emit            chan struct{}
	finish          chan struct{}
	emitOnce        sync.Once
	finishOnce      sync.Once
	slots           *concurrencyCacheMock
	usage           chan *service.UsageLog
}

func (f *nativeRecoveryFixture) emitNext()   { f.emitOnce.Do(func() { close(f.emit) }) }
func (f *nativeRecoveryFixture) finishNext() { f.finishOnce.Do(func() { close(f.finish) }) }

type nativeRecoveryAPIKeyRepo struct {
	service.APIKeyRepository
	fixture *nativeRecoveryFixture
}

func (r *nativeRecoveryAPIKeyRepo) GetByID(_ context.Context, id int64) (*service.APIKey, error) {
	f := r.fixture
	if id != f.scope.APIKeyID {
		return nil, service.ErrAPIKeyNotFound
	}
	status := service.StatusActive
	if f.accessRevoked.Load() {
		status = service.StatusAPIKeyDisabled
	}
	group := f.scope.GroupID
	return &service.APIKey{ID: id, UserID: f.scope.UserID, Status: status, GroupID: &group, User: &service.User{ID: f.scope.UserID, Status: service.StatusActive}, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1}}, nil
}

type nativeRecoveryOptions struct {
	billingRepo           service.UsageBillingRepository
	userRepo              service.UserRepository
	standard              bool
	terminalStatus        string
	unsupportedBackground bool
	cancelGate            <-chan struct{}
	pollError             bool
	executionTimeout      time.Duration
	configureControl      func(*service.OpenAIGatewayService, service.AccountRepository, *service.ConcurrencyService) *service.ControlledSchedulingService
	webSocket             bool
}

func newNativeRecoveryFixture(t *testing.T, modes ...string) *nativeRecoveryFixture {
	mode := ""
	if len(modes) > 0 {
		mode = modes[0]
	}
	return newNativeRecoveryFixtureOptions(t, mode, nativeRecoveryOptions{})
}
func newNativeRecoveryFixtureOptions(t *testing.T, mode string, options nativeRecoveryOptions) *nativeRecoveryFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &nativeRecoveryFixture{scope: responseturn.Scope{UserID: 71, APIKeyID: 72, GroupID: 73, Interface: "responses"}, emit: make(chan struct{}), finish: make(chan struct{}), usage: make(chan *service.UsageLog, 8)}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if options.webSocket && websocket.IsWebSocketUpgrade(r) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			f.createCount.Add(1)
			f.wsCreateCount.Add(1)
			background := gjson.GetBytes(body, "background").Bool()
			_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_fixture","status":"in_progress","background":%v}}`, background)))
			status := options.terminalStatus
			if status == "" {
				status = "completed"
			}
			if status == "bare_then_failed" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","sequence_number":1,"code":"server_error","message":"fixture failure"}`))
				status = "failed"
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":%q,"sequence_number":2,"response":{"id":"resp_fixture","status":%q,"model":"gpt-5.6-sol","background":%v,"output":[],"usage":{"input_tokens":4,"output_tokens":1}}}`, "response."+status, status, background)))
			return
		}
		if mode == "background" && r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp_fixture" {
			f.pollCount.Add(1)
			if options.pollError && f.cancelCount.Load() == 0 {
				http.Error(w, "fixture polling failure", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			cancelled := f.cancelCount.Load() > 0
			if options.cancelGate != nil {
				select {
				case <-options.cancelGate:
				default:
					cancelled = false
				}
			}
			if cancelled {
				_, _ = io.WriteString(w, `{"id":"resp_fixture","object":"response","status":"cancelled","background":true,"model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":4,"output_tokens":1}}`)
				return
			}
			select {
			case <-f.finish:
				status := options.terminalStatus
				if status == "" {
					status = "completed"
				}
				_, _ = fmt.Fprintf(w, `{"id":"resp_fixture","object":"response","status":%q,"background":true,"model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":4,"output_tokens":1}}`, status)
			default:
				_, _ = io.WriteString(w, `{"id":"resp_fixture","object":"response","status":"in_progress","background":true,"model":"gpt-5.6-sol","output":[]}`)
			}
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("real HTTP fixture does not support Flush")
			return
		}
		if r.URL.Path == "/v1/responses/resp_fixture/cancel" {
			f.cancelCount.Add(1)
			if options.cancelGate != nil {
				select {
				case <-options.cancelGate:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp_fixture","status":"cancelled"}`)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		count := f.createCount.Add(1)
		if mode == "json_error" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"id":"resp_fixture","error":{"type":"invalid_request_error","message":"fixture invalid request","code":"fixture_bad_request"}}`)
			return
		}
		if mode == "background" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Id", "fixture-background-usage-id")
			_, _ = io.WriteString(w, `{"id":"resp_fixture","object":"response","status":"queued","background":true,"model":"gpt-5.6-sol","output":[]}`)
			return
		}
		if mode == "retry" && count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"error":{"message":"fixture precommit failure"}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		background := gjson.GetBytes(body, "background").Bool()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "fixture-usage-id")
		_, _ = fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_fixture\",\"status\":\"in_progress\",\"background\":%v}}\n\n", background)
		flusher.Flush()
		if mode == "after_commit" {
			return
		}
		select {
		case <-f.emit:
		case <-r.Context().Done():
			f.createCancelled.Store(true)
			return
		}
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"item_fixture\",\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.future_event\ndata: {\"type\":\"response.future_event\",\"sequence_number\":2,\"value\":\"preserved\"}\n\n")
		flusher.Flush()
		select {
		case <-f.finish:
		case <-r.Context().Done():
			f.createCancelled.Store(true)
			return
		}
		if options.terminalStatus != "" {
			status := options.terminalStatus
			if status == "bare_then_failed" {
				_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"sequence_number\":3,\"code\":\"server_error\",\"message\":\"fixture failure\"}\n\n")
				status = "failed"
			}
			_, _ = fmt.Fprintf(w, "event: response.%s\ndata: {\"type\":%q,\"sequence_number\":4,\"response\":{\"id\":\"resp_fixture\",\"status\":%q,\"model\":\"gpt-5.6-sol\",\"background\":%v,\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n", status, "response."+status, status, background)
		} else {
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"background\":true,\"output\":[{\"id\":\"item_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n")
		}
		flusher.Flush()
	}))
	cfg := &config.Config{RunMode: config.RunModeSimple}
	if options.standard {
		cfg.RunMode = config.RunModeStandard
	}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.MaxAccountSwitches = 1
	if options.webSocket {
		cfg.Gateway.OpenAIWS.Enabled = true
		cfg.Gateway.OpenAIWS.APIKeyEnabled = true
		cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
		cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2
		cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
	}
	account := service.Account{ID: 74, Name: "native-fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{73},
		Credentials: map[string]any{"api_key": "fixture-secret", "base_url": f.upstream.URL}, Extra: map[string]any{"openai_passthrough": true, "openai_responses_supported": true}}
	if options.unsupportedBackground {
		account.Extra["openai_responses_supported"] = false
	}
	if options.webSocket {
		delete(account.Extra, "openai_passthrough")
		account.Extra["responses_websockets_v2_enabled"] = true
	}
	var repo service.AccountRepository = &openAIWSUsageHandlerAccountRepoStub{account: account}
	if mode == "retry" {
		second := account
		second.ID = 75
		second.Name = "second-native-fixture"
		repo = &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account, second}}
	}
	billing := service.NewBillingCacheService(nil, options.userRepo, nil, nil, nil, nil, cfg, nil)
	usage := &openAIWSUsageHandlerUsageLogRepoStub{created: f.usage}
	f.slots = &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
	concurrency := service.NewConcurrencyService(f.slots)
	gateway := service.NewOpenAIGatewayService(repo, usage, options.billingRepo, options.userRepo, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billing, &nativeRecoveryHTTPTransport{client: f.upstream.Client()}, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	var control *service.ControlledSchedulingService
	if options.configureControl != nil {
		control = options.configureControl(gateway, repo, concurrency)
	}
	f.handler = NewOpenAIGatewayHandler(gateway, concurrency, billing, service.NewAPIKeyService(&nativeRecoveryAPIKeyRepo{fixture: f}, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(nil))
	router.Use(func(c *gin.Context) {
		if options.executionTimeout > 0 {
			ctx, stop := context.WithTimeout(c.Request.Context(), options.executionTimeout)
			defer stop()
			c.Request = c.Request.WithContext(ctx)
		}
		user := f.scope.UserID
		if c.GetHeader("X-Fixture-User") == "other" {
			user = 99
		}
		group := f.scope.GroupID
		key := &service.APIKey{ID: f.scope.APIKeyID, UserID: user, Status: service.StatusActive, GroupID: &group, User: &service.User{ID: user, Status: service.StatusActive}, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1}}
		c.Set(string(middleware2.ContextKeyAPIKey), key)
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user, Concurrency: 1})
		c.Request = c.Request.WithContext(service.WithNativeStreamPolicy(c.Request.Context(), service.NativeStreamPolicy{Version: 1, Delivery: true, Recovery: true}))
		c.Next()
	})
	router.Use(service.ControlledSchedulingMiddleware(func(context.Context) (scheduling.ModeSnapshot, error) {
		if control != nil {
			return scheduling.ModeSnapshot{Mode: scheduling.ModeControlled}, nil
		}
		return scheduling.ModeSnapshot{Mode: scheduling.ModeSub2API}, nil
	}))
	router.POST("/v1/responses", f.handler.Responses)
	router.GET("/v1/responses/*subpath", f.handler.NativeResponseRetrieve)
	router.POST("/v1/responses/*subpath", func(c *gin.Context) {
		if !f.handler.ResponsesControlPost(c) {
			c.Status(404)
		}
	})
	f.server = httptest.NewServer(router)
	t.Cleanup(func() {
		f.emitNext()
		f.finishNext()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 7*time.Second)
		_ = f.handler.ShutdownNativeResponses(shutdownCtx)
		stop()
		f.server.Close()
		f.upstream.Close()
		if control != nil {
			control.Close()
		}
		gateway.StopOpenAICodexTicketHarvester()
		gateway.CloseOpenAIWSPool()
		billing.Stop()
	})
	return f
}
func (f *nativeRecoveryFixture) create(t *testing.T, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "fixture-turn")
	response, err := f.server.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	return response
}
func readNativeFrame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := r.ReadString('\n')
		require.NoError(t, err)
		_, _ = frame.WriteString(line)
		if line == "\n" || line == "\r\n" {
			return frame.String()
		}
	}
}
func (f *nativeRecoveryFixture) turn(t *testing.T) *responseturn.Turn {
	t.Helper()
	turn, err := f.handler.NativeResponseManager().Lookup(f.scope, "resp_fixture")
	require.NoError(t, err)
	return turn
}

func TestNativeResponseHTTPDetachReplayUsesOneGenerationAndUsage(t *testing.T) {
	f := newNativeRecoveryFixture(t)
	body := `{"model":"gpt-5.6-sol","input":"fixture","stream":true,"background":true}`
	response := f.create(t, body)
	first := readNativeFrame(t, bufio.NewReader(response.Body))
	require.Contains(t, first, "response.created")
	turn := f.turn(t)
	require.True(t, turn.Snapshot().Recoverable)
	require.NoError(t, response.Body.Close())
	require.Eventually(t, func() bool { return turn.Snapshot().State == responseturn.StateDetached }, time.Second, time.Millisecond)
	require.Equal(t, int32(0), atomic.LoadInt32(&f.slots.releaseAccountCalled), "offline generation must retain account slot")
	require.False(t, f.createCancelled.Load())
	duplicateReq, _ := http.NewRequest(http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(body))
	duplicateReq.Header.Set("Idempotency-Key", "fixture-turn")
	duplicate, err := f.server.Client().Do(duplicateReq)
	require.NoError(t, err)
	require.Equal(t, 409, duplicate.StatusCode)
	_ = duplicate.Body.Close()
	require.Equal(t, int64(1), f.createCount.Load())
	f.emitNext()
	require.Eventually(t, func() bool { return turn.Snapshot().JournalEvents == 3 }, time.Second, time.Millisecond)
	resumed, err := f.server.Client().Get(f.server.URL + "/v1/responses/resp_fixture?stream=true&starting_after=0")
	require.NoError(t, err)
	require.Equal(t, 200, resumed.StatusCode)
	reader := bufio.NewReader(resumed.Body)
	require.Contains(t, readNativeFrame(t, reader), `"sequence_number":1`)
	require.Contains(t, readNativeFrame(t, reader), "response.future_event")
	f.finishNext()
	require.Contains(t, readNativeFrame(t, reader), "response.completed")
	remaining, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Empty(t, remaining)
	_ = resumed.Body.Close()
	require.Eventually(t, func() bool { return atomic.LoadInt32(&f.slots.releaseAccountCalled) == 1 }, time.Second, time.Millisecond)
	select {
	case <-f.usage:
	case <-time.After(time.Second):
		t.Fatal("original usage was not recorded")
	}
	select {
	case <-f.usage:
		t.Fatal("replay settled usage twice")
	default:
	}
	require.Equal(t, int64(1), f.createCount.Load())
	require.Equal(t, int64(0), f.cancelCount.Load())
	result, err := f.server.Client().Get(f.server.URL + "/v1/responses/resp_fixture")
	require.NoError(t, err)
	data, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	_ = result.Body.Close()
	require.Equal(t, "completed", gjson.GetBytes(data, "status").String())
	require.Contains(t, string(data), "hello")
}

func TestNativeResponseHTTPCancelAuthorizationAndNoRevival(t *testing.T) {
	f := newNativeRecoveryFixture(t)
	response := f.create(t, `{"model":"gpt-5.6-sol","input":"fixture","stream":true,"background":true}`)
	_ = readNativeFrame(t, bufio.NewReader(response.Body))
	turn := f.turn(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/v1/responses/resp_fixture"
		if method == http.MethodPost {
			path += "/cancel"
		}
		request, _ := http.NewRequest(method, f.server.URL+path, nil)
		request.Header.Set("X-Fixture-User", "other")
		result, err := f.server.Client().Do(request)
		require.NoError(t, err)
		require.Equal(t, 404, result.StatusCode)
		_ = result.Body.Close()
	}
	require.Equal(t, int64(0), f.cancelCount.Load())
	cancelled, err := f.server.Client().Post(f.server.URL+"/v1/responses/resp_fixture/cancel", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, 200, cancelled.StatusCode)
	_ = cancelled.Body.Close()
	_ = response.Body.Close()
	require.Eventually(t, func() bool { return turn.Snapshot().CancelConfirmed }, time.Second, time.Millisecond)
	require.Equal(t, responseturn.ReasonUserStop, turn.Snapshot().Reason)
	require.Equal(t, int64(1), f.cancelCount.Load())
	require.True(t, turn.Snapshot().CancelRequested)
	again, err := f.server.Client().Post(f.server.URL+"/v1/responses/resp_fixture/cancel", "application/json", nil)
	require.NoError(t, err)
	_ = again.Body.Close()
	require.Equal(t, int64(1), f.cancelCount.Load())
	require.Equal(t, int64(1), f.createCount.Load())
	require.Equal(t, responseturn.StateCancelled, turn.Snapshot().State)
}

func TestNativeResponseHTTPOrdinaryDisconnectCancels(t *testing.T) {
	f := newNativeRecoveryFixture(t)
	response := f.create(t, `{"model":"gpt-5.6-sol","input":"fixture","stream":true}`)
	_ = readNativeFrame(t, bufio.NewReader(response.Body))
	turn := f.turn(t)
	require.False(t, turn.Snapshot().Recoverable)
	_ = response.Body.Close()
	require.Eventually(t, func() bool { return f.createCancelled.Load() }, time.Second, time.Millisecond)
	require.Equal(t, responseturn.StateCancelled, turn.Snapshot().State)
	require.Equal(t, responseturn.ReasonClientDetached, turn.Snapshot().Reason)
	require.Equal(t, int64(0), f.cancelCount.Load())
}

func TestNativeResponseHTTPPrecommitRetryKeepsLedgerBoundary(t *testing.T) {
	f := newNativeRecoveryFixture(t, "retry")
	response := f.create(t, `{"model":"gpt-5.6-sol","input":"fixture","stream":true,"background":true}`)
	first := readNativeFrame(t, bufio.NewReader(response.Body))
	require.Contains(t, first, "response.created")
	require.Equal(t, int64(2), f.createCount.Load(), "precommit failure must retain allowed retry")
	require.True(t, f.turn(t).Snapshot().Recoverable)
	f.emitNext()
	f.finishNext()
	remaining, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Contains(t, string(remaining), "response.completed")
	require.Equal(t, int64(2), f.createCount.Load())
}
func TestNativeResponseHTTPAfterCommitFailureNeverMixesAccounts(t *testing.T) {
	f := newNativeRecoveryFixture(t, "after_commit")
	response := f.create(t, `{"model":"gpt-5.6-sol","input":"fixture","stream":true,"background":true}`)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Contains(t, string(body), "response.created")
	require.Equal(t, int64(1), f.createCount.Load())
	require.NotEqual(t, responseturn.StateCompleted, f.turn(t).Snapshot().State)
}

func TestNativeResponseHTTPNonstreamBackgroundIdempotencyPollsBeforeSettlement(t *testing.T) {
	for _, stream := range []string{"", `,"stream":false`} {
		t.Run(stream, func(t *testing.T) {
			f := newNativeRecoveryFixture(t, "background")
			body := `{"model":"gpt-5.6-sol","input":"fixture","background":true` + stream + "}"
			request, err := http.NewRequest(http.MethodPost, f.server.URL+"/v1/responses", strings.NewReader(body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "nonstream-background")
			response, err := f.server.Client().Do(request)
			require.NoError(t, err)
			data, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			_ = response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "queued", gjson.GetBytes(data, "status").String())
			require.Equal(t, int64(1), f.createCount.Load())
			f.finishNext()
			require.Eventually(t, func() bool { return f.turn(t).Snapshot().State == responseturn.StateCompleted }, 2*time.Second, time.Millisecond)
		})
	}
}

func TestNativeResponseControlPostOnlyHandlesExactCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{}
	for _, prefix := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
		for _, suffix := range []string{"/resp_fixture/cancel", "/resp_fixture/input_items", "/input_items", "/compact", "/input_tokens", "/resp_fixture/cancel/extra", "/cancel"} {
			t.Run(prefix+suffix, func(t *testing.T) {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, prefix+suffix, nil)
				c.Params = gin.Params{{Key: "subpath", Value: suffix}}
				want := suffix == "/resp_fixture/cancel"
				require.Equal(t, want, h.ResponsesControlPost(c))
				if want {
					require.Equal(t, http.StatusUnauthorized, w.Code, "cancel must enter its authenticated dedicated handler")
				} else {
					require.False(t, c.Writer.Written(), "other native subpaths must retain their original dispatcher")
				}
			})
		}
	}
}

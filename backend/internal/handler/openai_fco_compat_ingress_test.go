package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/securityaudit"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// fcoAuditCapture is a legacy audit engine that records the fco_compat
// snapshot each audit call receives.
type fcoAuditCapture struct {
	mu      sync.Mutex
	calls   []fcoAuditCall
	onCheck func()
}

type fcoAuditCall struct {
	stage    string
	body     string
	snapshot service.OpenAIFCOCompatSnapshot
	ok       bool
}

func (e *fcoAuditCapture) Check(ctx context.Context, req securityaudit.Request) (*securityaudit.LegacyDecision, error) {
	snapshot, ok := service.OpenAIFCOCompatSnapshotFromContext(ctx)
	e.mu.Lock()
	e.calls = append(e.calls, fcoAuditCall{stage: req.Stage, body: string(req.Body), snapshot: snapshot, ok: ok})
	onCheck := e.onCheck
	e.mu.Unlock()
	if onCheck != nil {
		onCheck()
	}
	return &securityaudit.LegacyDecision{Allowed: true, Action: service.ContentModerationActionAllow}, nil
}

func (e *fcoAuditCapture) snapshotCalls() []fcoAuditCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]fcoAuditCall(nil), e.calls...)
}

// fcoIngressUpstream records the snapshot each outbound request carries.
type fcoIngressUpstream struct {
	service.HTTPUpstream
	sse bool
	// failFirst answers that many sends with a retryable 502.
	failFirst int
	mu        sync.Mutex
	sent      []fcoAuditCall
	accounts  []int64
}

func (u *fcoIngressUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	service.RecordOpsUpstreamSendBoundary(req.Context(), accountID)
	body, _ := io.ReadAll(req.Body)
	snapshot, ok := service.OpenAIFCOCompatSnapshotFromContext(req.Context())
	u.mu.Lock()
	u.sent = append(u.sent, fcoAuditCall{body: string(body), snapshot: snapshot, ok: ok})
	u.accounts = append(u.accounts, accountID)
	fail := len(u.sent) <= u.failFirst
	u.mu.Unlock()
	if fail {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporary upstream failure"}}`))}, nil
	}
	const response = `{"id":"resp_fco_ingress","object":"response","status":"completed","model":"gpt-5.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
	if u.sse || gjson.GetBytes(body, "stream").Bool() {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(
			`data: {"type":"response.completed","response":` + response + "}\n\n",
		))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
}

func (u *fcoIngressUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func (u *fcoIngressUpstream) snapshotSent() []fcoAuditCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]fcoAuditCall(nil), u.sent...)
}

func newFCOIngressHTTPHandler(t *testing.T, upstream service.HTTPUpstream, audit *fcoAuditCapture) *OpenAIGatewayHandler {
	t.Helper()
	account := service.Account{
		ID: 7301, Name: "fco-ingress", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://relay.example.test"}, Extra: map[string]any{},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	gateway := service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account}, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.securityAuditCoordinator = securityaudit.NewCoordinator(audit, nil)
	return h
}

// Retries and account switches happen inside the handler's selection loop;
// moderation runs once before it, and every send reuses the ingress snapshot.
// The fco replay itself lives in the outbound branch and is covered when the
// branches are integrated.
func TestOpenAIFCOCompatModerationRunsOnceAcrossRetriesAndFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Enabled = true
		s.FCOCompat.Mode = resilience.FCOCompatModeReactive
	})
	accounts := []service.Account{
		{
			ID: 7321, Name: "fco-pool", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{
				"api_key": "sk-pool", "base_url": "https://relay.example.test", "pool_mode": true,
				"pool_mode_retry_count": float64(1), "pool_mode_retry_status_codes": []any{float64(http.StatusBadGateway)},
			},
			Extra: map[string]any{},
		},
		{
			ID: 7322, Name: "fco-fallback", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Priority: 2,
			Credentials: map[string]any{"api_key": "sk-fallback", "base_url": "https://relay.example.test"},
			Extra:       map[string]any{},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.MaxAccountSwitches = 1
	upstream := &fcoIngressUpstream{failFirst: 2}
	audit := &fcoAuditCapture{}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewOpenAIGatewayService(&openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.securityAuditCoordinator = securityaudit.NewCoordinator(audit, nil)

	c, rec := newFCOIngressHTTPContext("/v1/responses", []byte(`{"model":"gpt-5.1","stream":false,"input":[`+
		`{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page"},{"type":"message","role":"user","content":"hi"}]}`))
	h.Responses(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	upstream.mu.Lock()
	gotAccounts := append([]int64(nil), upstream.accounts...)
	upstream.mu.Unlock()
	require.Equal(t, []int64{7321, 7321, 7322}, gotAccounts, "a same-account retry, then an account switch")
	calls := audit.snapshotCalls()
	require.Len(t, calls, 1, "moderation runs once per request")
	for i, sent := range upstream.snapshotSent() {
		require.True(t, sent.ok, "send %d", i+1)
		require.Same(t, calls[0].snapshot.Settings, sent.snapshot.Settings, "send %d reuses the ingress snapshot", i+1)
	}
}

func newFCOIngressHTTPContext(path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	groupID := int64(7302)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 7303, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}, User: &service.User{ID: 7304},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7304, Concurrency: 0})
	return c, rec
}

// The HTTP entry points take the snapshot from the client's bytes before the
// audit; the audit and the service send then share it, even when settings are
// reloaded in between, and the duplicate member flag survives later rebuilds.
func TestOpenAIFCOCompatIngressSnapshotReachesForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const fco = `{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"page"}`
	cases := []struct {
		name, path string
		body       string
		duplicate  bool
	}{
		{"responses", "/v1/responses", `{"model":"gpt-5.1","stream":false,"input":[` + fco + `,{"type":"message","role":"user","content":"hi"}]}`, false},
		{"responses duplicate member", "/v1/responses", `{"model":"gpt-5.1","stream":false,"input":[` + fco + `,{"type":"message","role":"user","content":"hi","content":"hi"}]}`, true},
		{"chat native input", "/v1/chat/completions", `{"model":"gpt-5.1","stream":false,"input":[` + fco + `,{"type":"message","role":"user","content":"hi"}]}`, false},
		{"chat native input duplicate member", "/v1/chat/completions", `{"model":"gpt-5.1","stream":false,"input":[` + fco + `,{"type":"message","role":"user","content":"hi","content":"hi"}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, func(s *resilience.Settings) {
				s.Enabled = true
				s.FCOCompat.Mode = resilience.FCOCompatModeReactive
				s.FCOCompat.AdminNames = []string{"alpha_name"}
			})
			// The Chat conversion reads the upstream Responses reply as a stream.
			upstream := &fcoIngressUpstream{sse: strings.Contains(tc.path, "chat/completions")}
			audit := &fcoAuditCapture{onCheck: func() {
				next := resilience.Defaults()
				next.Enabled = true
				next.FCOCompat.Mode = resilience.FCOCompatModeOff
				next.FCOCompat.AdminNames = []string{"beta_name"}
				resilience.Publish(next)
			}}
			h := newFCOIngressHTTPHandler(t, upstream, audit)
			c, rec := newFCOIngressHTTPContext(tc.path, []byte(tc.body))
			if strings.Contains(tc.path, "chat/completions") {
				h.ChatCompletions(c)
			} else {
				h.Responses(c)
			}
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			calls := audit.snapshotCalls()
			require.Len(t, calls, 1, "the request is audited once at ingress")
			require.True(t, calls[0].ok)
			require.Equal(t, []string{"alpha_name"}, calls[0].snapshot.Rules.AdminNames)
			require.Equal(t, resilience.FCOCompatModeReactive, calls[0].snapshot.GlobalMode())
			require.Equal(t, tc.duplicate, calls[0].snapshot.IngressHasDuplicateMembers)

			sent := upstream.snapshotSent()
			require.Len(t, sent, 1)
			require.True(t, sent[0].ok, "the service send carries the ingress snapshot")
			require.Same(t, calls[0].snapshot.Settings, sent[0].snapshot.Settings, "the reload during the audit does not reach the send")
			require.Equal(t, calls[0].snapshot.Rules, sent[0].snapshot.Rules)
			require.Equal(t, tc.duplicate, sent[0].snapshot.IngressHasDuplicateMembers)
		})
	}
}

// Each Responses WebSocket turn is audited exactly once, with the snapshot
// taken from that turn's own frame and the settings of the moment it arrived.
// Over the HTTP bridge the turn is also sent with that same snapshot.
func TestOpenAIWSAuditsEachTurnOnceWithItsOwnFCOCompatSnapshot(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			publish := func(fcoMode, adminName string) {
				next := resilience.Defaults()
				next.Enabled = true
				next.FCOCompat.Mode = fcoMode
				next.FCOCompat.AdminNames = []string{adminName}
				resilience.Publish(next)
			}
			withHandlerResilience(t, func(s *resilience.Settings) {
				s.Enabled = true
				s.FCOCompat.Mode = resilience.FCOCompatModeReactive
				s.FCOCompat.AdminNames = []string{"alpha_name"}
			})

			upstreamWS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				for turn := 1; ; turn++ {
					readCtx, cancelRead := context.WithTimeout(r.Context(), 5*time.Second)
					_, _, readErr := conn.Read(readCtx)
					cancelRead()
					if readErr != nil {
						return
					}
					event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_fco_ws_%d","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`, turn)
					writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
					writeErr := conn.Write(writeCtx, coderws.MessageText, []byte(event))
					cancelWrite()
					if writeErr != nil {
						return
					}
				}
			}))
			t.Cleanup(upstreamWS.Close)
			httpUpstream := &fcoIngressUpstream{sse: true}
			audit := &fcoAuditCapture{}
			clientConn, handlerDone := newFCOIngressWSHandler(t, mode, upstreamWS.URL, httpUpstream, audit)

			turn := func(payload string) {
				t.Helper()
				writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
				require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
				cancelWrite()
				readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
				_, event, err := clientConn.Read(readCtx)
				cancelRead()
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String(), string(event))
			}
			turn(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"turn one"}]}`)
			publish(resilience.FCOCompatModeReactive, "beta_name")
			turn(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"turn two","output":"turn two"}]}`)
			publish(resilience.FCOCompatModeProactive, "gamma_name")
			turn(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":[{"type":"function_call_output","namespace":"agent","name":"gamma_name","output":"turn three"}]}`)
			publish(resilience.FCOCompatModeOff, "delta_name")
			require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
			select {
			case <-handlerDone:
			case <-time.After(5 * time.Second):
				t.Fatal("websocket handler did not exit")
			}

			calls := audit.snapshotCalls()
			require.Len(t, calls, 3, "one audit per turn")
			require.Equal(t, []string{"first_turn", "subsequent_turn", "subsequent_turn"}, []string{calls[0].stage, calls[1].stage, calls[2].stage})
			wantAdmin := []string{"alpha_name", "beta_name", "gamma_name"}
			wantMode := []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive}
			wantDuplicate := []bool{false, true, false}
			for i, call := range calls {
				require.True(t, call.ok, "turn %d audit carries a snapshot", i+1)
				require.Equal(t, []string{wantAdmin[i]}, call.snapshot.Rules.AdminNames, "turn %d", i+1)
				require.Equal(t, wantMode[i], call.snapshot.GlobalMode(), "turn %d", i+1)
				require.Equal(t, wantDuplicate[i], call.snapshot.IngressHasDuplicateMembers, "turn %d", i+1)
			}
			if mode != service.OpenAIWSIngressModeHTTPBridge {
				return
			}
			sent := httpUpstream.snapshotSent()
			require.Len(t, sent, 3)
			for i := range sent {
				require.True(t, sent[i].ok, "turn %d send carries a snapshot", i+1)
				require.Same(t, calls[i].snapshot.Settings, sent[i].snapshot.Settings, "turn %d audit and send share one snapshot", i+1)
				require.Equal(t, calls[i].snapshot.IngressHasDuplicateMembers, sent[i].snapshot.IngressHasDuplicateMembers, "turn %d", i+1)
			}
		})
	}
}

func newFCOIngressWSHandler(t *testing.T, mode, upstreamURL string, httpUpstream service.HTTPUpstream, audit *fcoAuditCapture) (*coderws.Conn, <-chan struct{}) {
	t.Helper()
	groupID := int64(7311)
	account := service.Account{
		ID: 7312, Name: "fco-ingress-ws", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": upstreamURL},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    mode,
		},
	}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 3

	settingRepo := &contentModerationHandlerSettingRepo{values: map[string]string{}}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	gatewaySvc := service.NewOpenAIGatewayService(
		&openAIWSUsageHandlerAccountRepoStub{account: account}, &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)},
		nil, nil, nil, nil, testutil.NewRedisGatewayCache(t), cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCacheSvc, httpUpstream, &service.DeferredService{},
		nil, nil, nil, nil, nil, service.NewSettingService(settingRepo, nil), nil,
	)
	concurrencyCache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := &OpenAIGatewayHandler{
		gatewayService:           gatewaySvc,
		billingCacheService:      billingCacheSvc,
		apiKeyService:            &service.APIKeyService{},
		securityAuditCoordinator: securityaudit.NewCoordinator(audit, nil),
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(concurrencyCache), SSEPingFormatNone, time.Second),
	}
	apiKey := &service.APIKey{ID: 7313, UserID: 7314, Name: "fco-ws-key", Key: "sk-handler-fco-test", GroupID: &groupID,
		User: &service.User{ID: 7314, Status: service.StatusActive}}
	handlerDone := make(chan struct{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", func(c *gin.Context) {
		h.ResponsesWebSocket(c)
		close(handlerDone)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.CloseNow() })
	return clientConn, handlerDone
}

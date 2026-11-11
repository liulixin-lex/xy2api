//go:build unit

package handler

import (
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
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type captureRecorder struct {
	mu       sync.Mutex
	starts   []service.SchedAttemptStart
	samples  []schedquality.AttemptSample
	finished chan schedquality.AttemptSample
}
type captureAttempt struct {
	owner  *captureRecorder
	in     service.SchedAttemptStart
	sample schedquality.AttemptSample
	done   bool
}

func (r *captureRecorder) Begin(in service.SchedAttemptStart) service.SchedAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, in)
	return &captureAttempt{owner: r, in: in, sample: schedquality.AttemptSample{AccountID: in.AccountID, Model: in.Model, Stream: in.Stream, Warm: in.StickyWarm, RequestID: in.RequestID}}
}
func (*captureRecorder) SetObserver(func(schedquality.AttemptSample)) {}
func (*captureRecorder) Flush(context.Context) error                  { return nil }
func (a *captureAttempt) MarkFirstOutput(time.Time)                   { n := 1; a.sample.TTFOMs = &n }
func (a *captureAttempt) finish(o schedquality.Outcome, d string, u *service.SchedAttemptUsage) {
	if a.done {
		panic("attempt finished twice")
	}
	a.done = true
	a.sample.Outcome = o
	a.sample.Detail = d
	if u != nil {
		a.sample.PromptTokens = &u.PromptTokens
		a.sample.CacheReadTokens = &u.CacheReadTokens
	}
	if a.in.Test != nil {
		a.in.Test.Sink(a.sample)
		return
	}
	a.owner.mu.Lock()
	a.owner.samples = append(a.owner.samples, a.sample)
	a.owner.mu.Unlock()
	if a.owner.finished != nil {
		a.owner.finished <- a.sample
	}
}
func (a *captureAttempt) FinishOK(u *service.SchedAttemptUsage) {
	a.finish(schedquality.OutcomeOK, "", u)
}
func (a *captureAttempt) FinishFault(d string)        { a.finish(schedquality.OutcomeAccountFault, d, nil) }
func (a *captureAttempt) FinishOther(d string)        { a.finish(schedquality.OutcomeOther, d, nil) }
func (a *captureAttempt) FinishTimeout(time.Duration) { a.finish(schedquality.OutcomeTimeout, "", nil) }

type captureUpstream struct {
	service.HTTPUpstream
	calls  int
	bodies [][]byte
	reply  func(int, *http.Request, []byte) (*http.Response, error)
}

func (u *captureUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.calls++
	u.bodies = append(u.bodies, body)
	return u.reply(u.calls, req, body)
}
func captureResponse(status int, body string, sse bool) *http.Response {
	typ := "application/json"
	if sse {
		typ = "text/event-stream"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {typ}}, Body: io.NopCloser(strings.NewReader(body))}
}

const captureResponseJSON = `{"id":"resp_capture","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":100000,"output_tokens":2,"input_tokens_details":{"cached_tokens":90000}}}`

func captureSSE() string {
	return "data: " + `{"type":"response.output_text.delta","delta":"answer"}` + "\n\n" + captureTerminalSSE()
}
func captureTerminalSSE() string {
	return "data: " + `{"type":"response.completed","response":` + captureResponseJSON + "}\n\n"
}

type captureOptions struct {
	requestDone chan struct{}
	ops         *service.OpsService
	repo        service.AccountRepository
	cache       service.GatewayCache
	slots       *service.ConcurrencyService
	configure   func(*config.Config)
}

func captureFixture(t *testing.T, account service.Account, up service.HTTPUpstream, options ...captureOptions) (*OpenAIGatewayHandler, *captureRecorder, *gin.Engine) {
	var opt captureOptions
	if len(options) > 0 {
		opt = options[0]
	}
	t.Helper()
	gin.SetMode(gin.TestMode)
	if account.ID == 0 {
		account.ID = 9901
	}
	account.Platform = service.PlatformOpenAI
	account.Type = service.AccountTypeAPIKey
	account.Status = service.StatusActive
	account.Schedulable = true
	account.Concurrency = 2
	if account.Credentials == nil {
		account.Credentials = map[string]any{"api_key": "test", "base_url": "https://capture.example"}
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	var repo service.AccountRepository = &openAIWSUsageHandlerAccountRepoStub{account: account}
	if opt.repo != nil {
		repo = opt.repo
	}
	if opt.configure != nil {
		opt.configure(cfg)
	}
	usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 32)}
	gw := service.NewOpenAIGatewayService(repo, usage, nil, nil, nil, nil, opt.cache, cfg, nil, opt.slots, service.NewBillingService(cfg, nil), nil, billing, up, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	recorder := &captureRecorder{finished: make(chan schedquality.AttemptSample, 32)}
	gw.SetSchedAttemptRecorder(recorder)
	cache := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
	h := &OpenAIGatewayHandler{gatewayService: gw, cfg: cfg, billingCacheService: billing, apiKeyService: &service.APIKeyService{}, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 0)}
	groupID := int64(4201)
	key := &service.APIKey{ID: 1801, GroupID: &groupID, User: &service.User{ID: 1701, Status: service.StatusActive}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1701, Concurrency: 2})
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
		service.InstallDeliveryTracking(c, time.Now())
		c.Next()
		if opt.requestDone != nil {
			close(opt.requestDone)
		}
	})
	if opt.ops != nil {
		router.Use(OpsErrorLoggerMiddleware(opt.ops))
	}
	router.POST("/v1/images/generations", h.Images)
	router.POST("/v1/embeddings", h.Embeddings)
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/responses/compact", h.Responses)
	router.POST("/v1/chat/completions", h.ChatCompletions)
	router.POST("/v1/messages", h.Messages)
	router.GET("/v1/responses", h.ResponsesWebSocket)
	return h, recorder, router
}
func captureRequest(router http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(r, req)
	return r
}
func TestSchedCaptureA8Success(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, entry := range []string{"responses", "chat/completions", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", entry, stream), func(t *testing.T) {
				up := &captureUpstream{reply: func(_ int, _ *http.Request, _ []byte) (*http.Response, error) {
					return captureResponse(200, captureSSE(), true), nil
				}}
				_, rec, r := captureFixture(t, service.Account{}, up)
				body := fmt.Sprintf(`{"model":"gpt-5.1","stream":%t,"input":"hi","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, stream)
				resp := captureRequest(r, "/v1/"+entry, body)
				require.Equal(t, 200, resp.Code, resp.Body.String())
				require.Equal(t, 1, up.calls)
				require.Len(t, rec.samples, 1)
				sample := rec.samples[0]
				require.Equal(t, schedquality.OutcomeOK, sample.Outcome, sample.Detail)
				require.Equal(t, 100000, *sample.PromptTokens)
				require.Equal(t, 90000, *sample.CacheReadTokens)
				if stream {
					require.NotNil(t, sample.TTFOMs)
				}
				require.Equal(t, stream, sample.Stream)
			})
		}
	}
}
func TestSchedCaptureA7ConstructionFailure(t *testing.T) {
	withHandlerResilience(t, nil)
	up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) { t.Fatal("must not send"); return nil, nil }}
	_, rec, r := captureFixture(t, service.Account{Credentials: map[string]any{"api_key": "key", "base_url": "://invalid"}}, up)
	captureRequest(r, "/v1/responses", `{"model":"gpt-5.1","input":"hi"}`)
	require.Empty(t, rec.starts)
	require.Empty(t, rec.samples)
	require.Zero(t, up.calls)
}

func TestSchedCaptureA6RepairAndFallback(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			up := &captureUpstream{reply: func(n int, req *http.Request, _ []byte) (*http.Response, error) {
				if n == 1 {
					if fallback {
						return captureResponse(404, `{"error":{"message":"endpoint not found"}}`, false), nil
					}
					return captureResponse(400, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: top_p"}}`, false), nil
				}
				if strings.Contains(req.URL.Path, "chat/completions") {
					return captureResponse(200, `{"id":"chatcmpl_ok","choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":2}}`, false), nil
				}
				return captureResponse(200, captureSSE(), true), nil
			}}
			_, rec, r := captureFixture(t, service.Account{}, up)
			resp := captureRequest(r, "/v1/chat/completions", `{"model":"gpt-5.1","stream":false,"top_p":0.7,"messages":[{"role":"user","content":"hi"}]}`)
			require.Equal(t, 200, resp.Code, resp.Body.String())
			require.Equal(t, 2, up.calls)
			require.Len(t, rec.samples, 2)
			want := schedquality.OutcomeOther
			if fallback {
				want = schedquality.OutcomeAccountFault
			}
			require.Equal(t, want, rec.samples[0].Outcome)
			require.Equal(t, schedquality.OutcomeOK, rec.samples[1].Outcome)
		})
	}
}
func TestSchedCaptureA9A13ClassificationSwitches(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			withHandlerResilience(t, func(s *resilience.Settings) { s.Enabled = enabled })
			for _, rate := range []bool{false, true} {
				t.Run(fmt.Sprint(rate), func(t *testing.T) {
					up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
						if rate {
							return captureResponse(200, "data: "+`{"type":"response.failed","response":{"id":"resp_rate","status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}}`+"\n\n", true), nil
						}
						return captureResponse(502, `{"error":{"message":"Bad gateway"}}`, false), nil
					}}
					_, rec, r := captureFixture(t, service.Account{}, up)
					captureRequest(r, "/v1/responses", `{"model":"gpt-5.1","stream":true,"input":"hi"}`)
					require.NotEmpty(t, rec.samples)
					require.Len(t, rec.samples, up.calls)
					for _, sample := range rec.samples {
						want := schedquality.OutcomeAccountFault
						if rate {
							want = schedquality.OutcomeOther
						}
						require.Equal(t, want, sample.Outcome, sample.Detail)
					}
				})
			}
		})
	}
}
func TestSchedCaptureA14CompactEvidenceModel(t *testing.T) {
	withHandlerResilience(t, nil)
	up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
		return captureResponse(200, captureResponseJSON, false), nil
	}}
	account := service.Account{Credentials: map[string]any{"api_key": "key", "base_url": "https://capture.example", "model_mapping": map[string]any{"public": "gpt-5.1"}, "compact_model_mapping": map[string]any{"public": "gpt-5.2"}}, Extra: map[string]any{"openai_compact_supported": true}}
	_, rec, r := captureFixture(t, account, up)
	resp := captureRequest(r, "/v1/responses/compact", `{"model":"public","input":"hi"}`)
	require.Equal(t, 200, resp.Code, resp.Body.String())
	require.Len(t, rec.samples, 1)
	require.Equal(t, "gpt-5.1", rec.samples[0].Model)
	require.Contains(t, string(up.bodies[0]), "gpt-5.2")
}

func TestSchedCaptureA10WebSocketTwoTurns(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		for _, rate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", mode, rate), func(t *testing.T) {
				withHandlerResilience(t, nil)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer conn.CloseNow()
					for turn := 0; turn < 2; turn++ {
						_, _, err = conn.Read(r.Context())
						if err != nil {
							return
						}
						if turn == 1 {
							if rate {
								_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.failed","response":{"id":"resp_bad","error":{"code":"rate_limit_exceeded","message":"rate limit exceeded"}}}`))
							} else {
								// Opening metadata prevents the pool's automatic replay without marking semantic output.
								_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_bad","status":"in_progress"}}`))
							}
							return
						}
						_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":`+captureResponseJSON+`}`))
					}
				}))
				defer upstream.Close()
				requestDone := make(chan struct{})
				_, rec, router := captureFixture(t, service.Account{Credentials: map[string]any{"api_key": "key", "base_url": upstream.URL}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": mode}}, &compositeWSHTTPUpstream{}, captureOptions{requestDone: requestDone})
				server := httptest.NewServer(router)
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
				require.NoError(t, err)
				defer conn.CloseNow()
				for turn := 0; turn < 2; turn++ {
					require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)))
					_, _, err = conn.Read(ctx)
					if turn == 0 {
						require.NoError(t, err)
					}
				}
				var samples []schedquality.AttemptSample
				for len(samples) < 2 {
					select {
					case row := <-rec.finished:
						samples = append(samples, row)
					case <-ctx.Done():
						t.Fatal("missing per-turn samples")
					}
				}
				require.Equal(t, schedquality.OutcomeOK, samples[0].Outcome)
				require.NotNil(t, samples[0].TTFOMs)
				want := schedquality.OutcomeAccountFault
				if rate {
					want = schedquality.OutcomeOther
				}
				require.Equal(t, want, samples[1].Outcome, samples[1].Detail)
				_ = conn.CloseNow()
				select {
				case <-requestDone:
				case <-ctx.Done():
					t.Fatal("websocket handler did not exit")
				}
				require.Len(t, rec.starts, 2)
				require.Len(t, rec.samples, 2)
			})
		}
	}
}

func TestSchedCaptureA11ExcludedHandlers(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, entry := range []string{"images/generations", "embeddings"} {
		t.Run(entry, func(t *testing.T) {
			up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
				return captureResponse(200, `{"created":1,"data":[{"b64_json":"aGVsbG8=","embedding":[0.1,0.2],"index":0}],"usage":{"prompt_tokens":2,"total_tokens":2}}`, false), nil
			}}
			_, rec, router := captureFixture(t, service.Account{}, up)
			model := "text-embedding-3-small"
			if entry == "images/generations" {
				model = "gpt-image-1"
			}
			response := captureRequest(router, "/v1/"+entry, fmt.Sprintf(`{"model":%q,"prompt":"draw a cat","input":"hello"}`, model))
			require.Equal(t, 200, response.Code, response.Body.String())
			require.Equal(t, 1, up.calls)
			require.Empty(t, rec.starts)
			require.Empty(t, rec.samples)
		})
	}
}

type capturePinnedCache struct {
	service.GatewayCache
	owner  int64
	writes int
}

func (c *capturePinnedCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return c.owner, nil
}
func (c *capturePinnedCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	c.writes++
	return nil
}
func (*capturePinnedCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (*capturePinnedCache) DeleteSessionAccountID(context.Context, int64, string) error { return nil }
func TestSchedCaptureA15BorrowedRepairStaysCold(t *testing.T) {
	withHandlerResilience(t, nil)
	accounts := []service.Account{}
	for i := int64(1); i <= 2; i++ {
		accounts = append(accounts, service.Account{ID: i, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: int(i), GroupIDs: []int64{4201}, Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}})
	}
	slots := service.NewConcurrencyService(&concurrencyCacheMock{acquireAccountSlotFn: func(_ context.Context, id int64, _ int, _ string) (bool, error) { return id == 2, nil }})
	cache := &capturePinnedCache{owner: 1}
	up := &captureUpstream{reply: func(n int, _ *http.Request, _ []byte) (*http.Response, error) {
		if n == 1 {
			return captureResponse(400, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: top_p"}}`, false), nil
		}
		return captureResponse(200, captureSSE(), true), nil
	}}
	_, rec, router := captureFixture(t, service.Account{}, up, captureOptions{repo: openAIImagesFailoverAccountRepo{accounts: accounts}, cache: cache, slots: slots})
	response := captureRequest(router, "/v1/chat/completions", `{"model":"gpt-5.1","top_p":0.7,"prompt_cache_key":"borrowed-session","messages":[{"role":"user","content":"hello"}]}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, 2, up.calls)
	require.Len(t, rec.samples, 2)
	for _, row := range rec.samples {
		require.Equal(t, int64(2), row.AccountID)
		require.False(t, row.Warm)
	}
	require.Equal(t, schedquality.OutcomeOther, rec.samples[0].Outcome)
	require.Equal(t, schedquality.OutcomeOK, rec.samples[1].Outcome)
	require.Zero(t, cache.writes, "borrow must preserve the sticky owner")
}

func TestSchedCaptureA9OpsMonitoringSwitch(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
				return captureResponse(502, `{"error":{"message":"bad gateway"}}`, false), nil
			}}
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			ops.SetMonitoringEnabled(enabled)
			require.Equal(t, enabled, ops.IsMonitoringEnabled(context.Background()))
			_, rec, router := captureFixture(t, service.Account{}, up, captureOptions{ops: ops})
			captureRequest(router, "/v1/responses", `{"model":"gpt-5.1","input":"hi"}`)
			require.NotEmpty(t, rec.samples)
			require.Len(t, rec.samples, up.calls)
			for _, row := range rec.samples {
				require.Equal(t, schedquality.OutcomeAccountFault, row.Outcome)
			}
		})
	}
}

func TestSchedCaptureA8PassthroughAndRaw(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, mode := range []string{"passthrough", "raw"} {
		for _, entry := range []string{"responses", "chat/completions", "messages"} {
			if mode == "passthrough" && entry != "responses" {
				continue
			}
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%t", mode, entry, stream), func(t *testing.T) {
					up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
						if mode == "passthrough" {
							require.Contains(t, req.URL.Path, "responses")
							return captureResponse(200, captureSSE(), true), nil
						}
						require.Contains(t, req.URL.Path, "chat/completions")
						if !stream {
							return captureResponse(200, `{"id":"chat_capture","choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100000,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":90000}}}`, false), nil
						}
						return captureResponse(200, "data: "+`{"id":"chat_capture","choices":[{"index":0,"delta":{"role":"assistant","content":"answer"},"finish_reason":null}]}`+"\n\n"+"data: "+`{"id":"chat_capture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100000,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":90000}}}`+"\n\ndata: [DONE]\n\n", true), nil
					}}
					extra := map[string]any{"openai_passthrough": true}
					if mode == "raw" {
						extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
					}
					_, rec, router := captureFixture(t, service.Account{Extra: extra}, up)
					response := captureRequest(router, "/v1/"+entry, fmt.Sprintf(`{"model":"gpt-5.1","stream":%t,"input":"hi","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, stream))
					require.Equal(t, 200, response.Code, response.Body.String())
					require.Equal(t, 1, up.calls)
					require.Len(t, rec.samples, 1)
					require.Equal(t, schedquality.OutcomeOK, rec.samples[0].Outcome, rec.samples[0].Detail)
					require.NotNil(t, rec.samples[0].PromptTokens)
					require.Equal(t, 100000, *rec.samples[0].PromptTokens)
					require.Equal(t, 90000, *rec.samples[0].CacheReadTokens)
					require.Equal(t, stream, rec.samples[0].Stream)
					if stream {
						require.NotNil(t, rec.samples[0].TTFOMs)
					}
				})
			}
		}
	}
}

func TestSchedCaptureA8WebSocketHTTPBridge(t *testing.T) {
	withHandlerResilience(t, nil)
	up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
		return captureResponse(200, captureTerminalSSE(), true), nil
	}}
	_, rec, router := captureFixture(t, service.Account{Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModeCtxPool}}, up, captureOptions{configure: func(cfg *config.Config) {
		cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
		cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	}})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	for turn := 0; turn < 2; turn++ {
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)))
		_, _, err = conn.Read(ctx)
		require.NoError(t, err)
		select {
		case row := <-rec.finished:
			require.Equal(t, schedquality.OutcomeOK, row.Outcome, row.Detail)
			require.NotNil(t, row.TTFOMs)
			require.Equal(t, 100000, *row.PromptTokens)
			require.Equal(t, turn > 0, row.Warm)
		case <-ctx.Done():
			t.Fatal("missing bridge sample")
		}
	}
	require.Equal(t, 2, up.calls)
}

func TestSchedCaptureRawChatTerminalWithoutDone(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, entry := range []string{"responses", "chat/completions", "messages"} {
		for _, terminal := range []string{"finish_reason", "usage"} {
			t.Run(entry+"/"+terminal, func(t *testing.T) {
				payload := "data: " + `{"id":"chat_capture","choices":[{"index":0,"delta":{"role":"assistant","content":"answer"},"finish_reason":null}]}` + "\n\n"
				if terminal == "finish_reason" {
					payload += "data: " + `{"id":"chat_capture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
				} else {
					payload += "data: " + `{"id":"chat_capture","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2}}` + "\n\n"
				}
				up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
					return captureResponse(http.StatusOK, payload, true), nil
				}}
				_, rec, router := captureFixture(t, service.Account{Extra: map[string]any{"openai_responses_mode": "force_chat_completions"}}, up)
				resp := captureRequest(router, "/v1/"+entry, `{"model":"gpt-5.1","stream":true,"input":"hi","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
				require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
				require.Equal(t, 1, up.calls)
				require.Len(t, rec.starts, 1)
				require.Len(t, rec.samples, 1)
				require.Equal(t, schedquality.OutcomeOK, rec.samples[0].Outcome, rec.samples[0].Detail)
			})
		}
	}
}

func TestSchedCaptureStreamStallDetail(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Stall.TextStallSeconds = 1 })
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
				reader, writer := io.Pipe()
				t.Cleanup(func() { _ = writer.Close() })
				go func() {
					_, _ = io.WriteString(writer, "data: "+`{"type":"response.output_text.delta","delta":"answer"}`+"\n\n")
				}()
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
			}}
			_, rec, router := captureFixture(t, service.Account{Extra: map[string]any{"openai_passthrough": passthrough}}, up, captureOptions{configure: func(cfg *config.Config) { cfg.Gateway.StreamDataIntervalTimeout = 1 }})
			resp := captureRequest(router, "/v1/responses", `{"model":"gpt-5.1","stream":true,"input":"hi"}`)
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			require.Equal(t, 1, up.calls)
			require.Len(t, rec.samples, 1)
			require.Equal(t, schedquality.OutcomeAccountFault, rec.samples[0].Outcome)
			require.Equal(t, string(resilience.CategoryStall), rec.samples[0].Detail)
		})
	}
}

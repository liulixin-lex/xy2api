//go:build unit

package handler

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type ownerFactsCache struct {
	capturePinnedCache
	service.OpenAIStickyVersionedCache
}

func (c *ownerFactsCache) GetSessionBinding(context.Context, int64, string) (service.StickySessionBinding, error) {
	return service.StickySessionBinding{AccountID: c.owner, Generation: "g1"}, nil
}

type ownerFactsStore struct{ service.ResilienceStore }

func (ownerFactsStore) SessionAvoidWrittenAt(context.Context, int64, string) (map[int64]time.Time, error) {
	return nil, nil
}
func (ownerFactsStore) SessionFailures(context.Context, int64, string) (map[int64]int64, error) {
	return nil, nil
}
func (ownerFactsStore) SetSessionCacheRatio(context.Context, string, float64, time.Duration) error {
	return nil
}
func (ownerFactsStore) ClearSessionFailure(context.Context, int64, string, int64) error {
	return nil
}
func (ownerFactsStore) SessionCacheRatio(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}

func (ownerFactsStore) IncrSessionFailure(context.Context, int64, string, int64, time.Duration) (int64, error) {
	return 1, nil
}
func (ownerFactsStore) AddSessionAvoid(context.Context, int64, string, int64, time.Duration, ...time.Time) error {
	return nil
}
func (ownerFactsStore) SessionAvoid(context.Context, int64, string) (map[int64]struct{}, error) {
	return nil, nil
}
func (ownerFactsStore) MarkGroupStable(_ context.Context, _ int64, now time.Time) (time.Time, error) {
	return now, nil
}
func (ownerFactsStore) ClearGroupStable(context.Context, int64) error { return nil }
func (ownerFactsStore) IncrUndelivered(context.Context, int64, string) (int64, error) {
	return 1, nil
}
func (ownerFactsStore) MarkFallbackKey(context.Context, int64, int64, time.Duration) error {
	return nil
}
func (ownerFactsStore) FallbackKey(context.Context, int64, int64) (bool, error) {
	return false, nil
}
func (ownerFactsStore) ClearFallbackKey(context.Context, int64, int64) error { return nil }

func TestOwnerCutoffFromHTTPIngress(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Budget.FirstOutputTimeoutSeconds.Default = 1
		s.Budget.ModelOverrides = nil
		s.Budget.PreOutputBudgetSeconds = 12
	})
	for _, tc := range []struct {
		name, path, fields string
		transfer           bool
	}{
		{"responses", "/v1/responses", `"input":"hello"`, true},
		{"messages", "/v1/messages", `"max_tokens":32,"messages":[{"role":"user","content":"hello"}]`, true},
		{"chat", "/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}]`, true},
		{"chat_image", "/v1/chat/completions", `"input":"hello","tools":[{"type":"image_generation"}]`, false},
		{"chat_continuation", "/v1/chat/completions", `"input":"hello","previous_response_id":"resp_continuation"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := service.Account{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{4201}, Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
			repo := &openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
			var state *resilience.RequestState
			var wirePrevious string
			var wireImage bool
			up := &captureUpstream{reply: func(_ int, req *http.Request, body []byte) (*http.Response, error) {
				state = resilience.StateFrom(req.Context())
				wireImage = service.IsExplicitImageGenerationIntent(tc.path, "gpt-5.1", body)
				wirePrevious = gjson.GetBytes(body, "previous_response_id").String()
				<-req.Context().Done()
				return nil, req.Context().Err()
			}}
			h, _, router := captureFixture(t, account, up, captureOptions{repo: repo, cache: &ownerFactsCache{capturePinnedCache: capturePinnedCache{owner: account.ID}}})
			previous := service.Resilience()
			rt := service.ProvideResilienceService(nil, ownerFactsStore{}, nil, nil, repo, nil, nil, &service.GatewayService{}, h.gatewayService)
			t.Cleanup(func() { rt.Stop(); service.SetResilienceInstance(previous) })
			probe := gin.New()
			probe.Use(router.Handlers...)
			probe.Use(func(c *gin.Context) {
				ctx := c.Request.Context()
				resilience.StateFrom(ctx).SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
				c.Next()
			})
			probe.POST("/v1/responses", h.Responses)
			probe.POST("/v1/messages", h.Messages)
			probe.POST("/v1/chat/completions", h.ChatCompletions)
			captureRequest(probe, tc.path, `{"model":"gpt-5.1","stream":true,"prompt_cache_key":"fixture",`+tc.fields+"}")
			require.Equal(t, 1, up.calls)
			if tc.name == "chat_continuation" {
				require.Equal(t, "resp_continuation", wirePrevious)
			}
			fact, ok := state.OwnerCutoff()
			if tc.name == "chat_image" {
				require.True(t, wireImage)
				selected, _ := state.SchedSelection()
				require.False(t, selected.HTTPStreamText)
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.Equal(t, tc.transfer, fact.CanTransfer)
			require.Equal(t, account.ID, fact.AccountID)
		})
	}
}

type factsRedirectTransport struct {
	service.HTTPUpstream
	state *resilience.RequestState
}

func (u *factsRedirectTransport) Do(req *http.Request, proxy string, id int64, c int) (*http.Response, error) {
	u.state = resilience.StateFrom(req.Context())
	return u.HTTPUpstream.Do(req, proxy, id, c)
}
func TestOwnerCutoffAfterHTTPRedirect(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Budget.FirstOutputTimeoutSeconds.Default = 1
		s.Budget.ModelOverrides = nil
		s.Budget.PreOutputBudgetSeconds = 12
	})
	for _, phase := range []string{"tls_handshake", "http_request"} {
		t.Run(phase, func(t *testing.T) {
			release := make(chan struct{})
			var clientHello atomic.Bool
			var requests atomic.Int32
			second := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				<-release
			}))
			if phase == "tls_handshake" {
				second.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
					clientHello.Store(true)
					<-release
					return nil, nil
				}}
				second.StartTLS()
			} else {
				second.Start()
			}
			defer second.Close()
			defer close(release)

			var redirects atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirects.Add(1)
				http.Redirect(w, r, second.URL+"/next", http.StatusTemporaryRedirect)
			}))
			defer first.Close()
			account := service.Account{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{4201}, Credentials: map[string]any{"api_key": "test", "base_url": first.URL}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
			repo := &openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
			up := &factsRedirectTransport{HTTPUpstream: repository.NewHTTPUpstream(&config.Config{})}
			h, _, router := captureFixture(t, account, up, captureOptions{cache: &ownerFactsCache{capturePinnedCache: capturePinnedCache{owner: account.ID}}, repo: repo})
			previous := service.Resilience()
			rt := service.ProvideResilienceService(nil, ownerFactsStore{}, nil, nil, repo, nil, nil, &service.GatewayService{}, h.gatewayService)
			t.Cleanup(func() { rt.Stop(); service.SetResilienceInstance(previous) })
			probe := gin.New()
			probe.Use(router.Handlers...)
			probe.Use(func(c *gin.Context) {
				ctx := c.Request.Context()
				resilience.StateFrom(ctx).SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
				c.Next()
			})
			probe.POST("/v1/responses", h.Responses)
			response := captureRequest(probe, "/v1/responses", `{"model":"gpt-5.1","input":"hi","stream":true,"prompt_cache_key":"review"}`)
			require.Equal(t, http.StatusBadGateway, response.Code)
			require.EqualValues(t, 1, redirects.Load())
			if phase == "tls_handshake" {
				require.True(t, clientHello.Load(), "second hop reached TLS")
				require.Zero(t, requests.Load(), "second hop has sent no HTTP request")
			} else {
				require.EqualValues(t, 1, requests.Load(), "second hop received the HTTP request")
			}
			require.NotNil(t, up.state)
			_, published := up.state.OwnerCutoff()
			require.False(t, published, "redirect wait cannot establish an original-owner cutoff")
		})
	}

}

func TestOwnerCutoffHTTPSource(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, route := range []string{"responses", "chat/completions", "messages"} {
		for _, mode := range []string{"buffered", "stream", "image"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				var sel resilience.SchedSelection
				up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
					sel, _ = resilience.StateFrom(req.Context()).SchedSelection()
					resp := captureResponse(200, captureTerminalSSE(), true)
					resp.Request = req
					return resp, nil
				}}
				_, _, router := captureFixture(t, service.Account{Extra: map[string]any{"openai_responses_mode": "force_responses"}}, up)
				fields := `"max_tokens":100,"messages":[{"role":"user","content":"hello"}]`
				if route == "responses" || route == "chat/completions" && mode == "image" {
					fields = `"input":"hello"`
				}
				switch mode {
				case "image":
					fields += `,"stream":true,"tools":[{"type":"image_generation","name":"draw","input_schema":{"type":"object"}}]`
				case "stream":
					fields += `,"stream":true`
				}
				response := captureRequest(router, "/v1/"+route, `{"model":"gpt-5.1",`+fields+"}")
				require.Equal(t, 1, up.calls, response.Body.String())
				require.Equal(t, mode == "stream", sel.HTTPStreamText)
			})
		}
	}
}

type factsReselectionSlots struct {
	queueReselectionCache
	binding *capturePinnedCache
}

func (s *factsReselectionSlots) IncrementAccountWaitCount(ctx context.Context, id int64, limit int) (bool, error) {
	ok, err := s.queueReselectionCache.IncrementAccountWaitCount(ctx, id, limit)
	// A concurrent request moves the binding while the first choice is queued.
	// The next selection hits that binding but cannot become original-owner evidence.
	s.binding.owner = 2
	s.freeB = true
	return ok, err
}

func TestOwnerCutoffQueueReselection(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, route := range []string{"responses", "chat/completions", "messages"} {
		t.Run(route, func(t *testing.T) {
			a := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{4201}, Credentials: map[string]any{"api_key": "test", "base_url": "https://capture.example"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
			b := a
			b.ID = 2
			repo := &openAIImagesFailoverAccountRepo{accounts: []service.Account{a, b}}
			binding := &capturePinnedCache{owner: 1}
			slots := &factsReselectionSlots{queueReselectionCache: queueReselectionCache{attempts: map[int64]int{}, queueFull: true}, binding: binding}
			concurrency := service.NewConcurrencyService(slots)
			var selected resilience.SchedSelection
			var profileWarm bool
			up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
				st := resilience.StateFrom(req.Context())
				selected, _ = st.SchedSelection()
				profileWarm = st.AttemptProfile().Warm
				resp := captureResponse(200, captureTerminalSSE(), true)
				resp.Request = req
				return resp, nil
			}}
			h, _, router := captureFixture(t, a, up, captureOptions{repo: repo, cache: binding, slots: concurrency})
			h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatNone, 0)
			body := `{"model":"gpt-5.1","stream":true,"input":"hi","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"fixture"}`
			response := captureRequest(router, "/v1/"+route, body)
			require.Equal(t, 1, slots.waitAdds, response.Body.String())
			require.Equal(t, 1, up.calls)
			require.EqualValues(t, 2, selected.AccountID)
			require.True(t, profileWarm, "new selection really was sticky")
			require.False(t, selected.StickyWarm)
		})
	}
}

func TestOwnerCutoffWebSocketHTTPBridgeSource(t *testing.T) {
	withHandlerResilience(t, nil)
	selections := make(chan resilience.SchedSelection, 2)
	up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
		sel, _ := resilience.StateFrom(req.Context()).SchedSelection()
		selections <- sel
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
	for range 2 {
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)))
		_, _, err = conn.Read(ctx)
		require.NoError(t, err)
		select {
		case sel := <-selections:
			require.NotZero(t, sel.AccountID)
			require.False(t, sel.HTTPStreamText)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case <-rec.finished:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

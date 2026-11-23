//go:build unit

package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type collabWiringUpstream struct {
	respond func(*http.Request, int64) (*http.Response, error)
	sends   int
}

func (u *collabWiringUpstream) Do(req *http.Request, _ string, account int64, _ int) (*http.Response, error) {
	u.sends++
	service.RecordOpsUpstreamSendBoundary(req.Context(), account)
	return u.respond(req, account)
}
func (u *collabWiringUpstream) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

type collabWiringFixture struct {
	handler  *OpenAIGatewayHandler
	gateway  *service.OpenAIGatewayService
	usage    *openAIWSUsageHandlerUsageLogRepoStub
	upstream *collabWiringUpstream
	repo     *openAIWSFailoverHandlerAccountRepoStub
}

func newCollabWiringFixture(t *testing.T, settings resilience.Settings, respond func(*http.Request, int64) (*http.Response, error)) *collabWiringFixture {
	t.Helper()
	old := resilience.Current().Clone()
	settings.Keepalive.Enabled = false
	resilience.Publish(settings)
	t.Cleanup(func() { resilience.Publish(old) })
	accounts := []service.Account{{ID: 71, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://fixture.invalid"}, Extra: map[string]any{"openai_collaboration_plaintext": true}}}
	repo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts, groupLookup: true}
	memory := &replayMemory{bindings: map[string]int64{}, avoids: map[string]map[int64]time.Time{}, avoidUntil: map[string]map[int64]time.Time{}, failures: map[string]map[int64]int64{}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = "synthetic-wiring-key"
	cfg.Default.RateMultiplier = 1
	upstream := &collabWiringUpstream{respond: respond}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	rateLimit := service.NewRateLimitService(repo, nil, cfg, nil, nil)
	usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 16), unbilled: make(chan *service.GatewayUnbilledRequest, 16)}
	gateway := service.NewOpenAIGatewayService(repo, usage, nil, nil, nil, nil, memory, cfg, nil, nil, service.NewBillingService(cfg, nil), rateLimit, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	gateway.StopOpenAICodexTicketHarvester()
	t.Cleanup(gateway.CloseCollabRecords)
	previousRuntime := service.Resilience()
	runtime := service.ProvideResilienceService(nil, memory, nil, nil, repo, nil, rateLimit, &service.GatewayService{}, gateway)
	t.Cleanup(func() { runtime.Stop(); service.SetResilienceInstance(previousRuntime) })
	slots := &replaySlots{active: map[int64]int{}}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(slots), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	return &collabWiringFixture{handler: h, gateway: gateway, usage: usage, upstream: upstream, repo: repo}
}

func (f *collabWiringFixture) request(body []byte) (*httptest.ResponseRecorder, *resilience.RequestState) {
	router := gin.New()
	router.Use(middleware.ClientRequestID())
	var state *resilience.RequestState
	router.POST("/v1/responses", func(c *gin.Context) {
		group := int64(1)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 1, GroupID: &group, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI}, User: &service.User{ID: 1}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		f.handler.Responses(c)
		state = resilience.StateFrom(c.Request.Context())
	})
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.170.0")
	req.Header.Set("session_id", "sample-thread")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec, state
}

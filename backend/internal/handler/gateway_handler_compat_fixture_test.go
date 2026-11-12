//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type handlerCompatStore struct{ service.GatewayCache }

func (*handlerCompatStore) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, service.ErrStickySessionNotFound
}
func (*handlerCompatStore) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (*handlerCompatStore) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (*handlerCompatStore) DeleteSessionAccountID(context.Context, int64, string) error { return nil }

type handlerCompatUsage struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *handlerCompatUsage) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	copy := *log
	r.logs = append(r.logs, &copy)
	return true, nil
}

type handlerCompatAccounts struct {
	service.AccountRepository
	penalties int
	account   *service.Account
}

func (r *handlerCompatAccounts) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	if r.account == nil || !r.account.Schedulable {
		return nil, nil
	}
	return []service.Account{*r.account}, nil
}

func (r *handlerCompatAccounts) SetError(context.Context, int64, string) error {
	r.penalties++
	return nil
}
func (r *handlerCompatAccounts) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.penalties++
	return nil
}
func (r *handlerCompatAccounts) SetRateLimited(context.Context, int64, time.Time) error {
	r.penalties++
	return nil
}
func (r *handlerCompatAccounts) SetOverloaded(context.Context, int64, time.Time) error {
	r.penalties++
	return nil
}

type handlerCompatHTTP struct{ client *http.Client }

func (u *handlerCompatHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(req)
}
func (u *handlerCompatHTTP) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

type handlerCompatWire struct {
	body   []byte
	header http.Header
}
type handlerCompatFixture struct {
	handler  *GatewayHandler
	group    *service.Group
	key      *service.APIKey
	account  *service.Account
	snapshot *fakeSchedulerCache
	store    *handlerCompatStore
	usage    *handlerCompatUsage
	accounts *handlerCompatAccounts
	wire     chan handlerCompatWire
	response func(uint64) string
}

func newHandlerCompatFixture(t *testing.T, channels ...*service.ChannelService) *handlerCompatFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &handlerCompatFixture{wire: make(chan handlerCompatWire, 4)}
	var calls atomic.Uint64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.wire <- handlerCompatWire{body, r.Header.Clone()}
		if f.response != nil {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, f.response(calls.Add(1)))
			return
		}
		http.Error(w, "unexpected upstream request", http.StatusBadGateway)
	}))
	t.Cleanup(upstream.Close)
	f.group = &service.Group{ID: 8100, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	f.account = &service.Account{ID: 8101, Name: "compat-fixture", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "test-key", "base_url": upstream.URL}, Extra: map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileOff}, AccountGroups: []service.AccountGroup{{AccountID: 8101, GroupID: f.group.ID}}}
	f.key = &service.APIKey{ID: 8102, UserID: 8103, GroupID: &f.group.ID, Group: f.group, Status: service.StatusActive, User: &service.User{ID: 8103, Concurrency: 10, Balance: 100}}
	f.store = &handlerCompatStore{}
	f.usage = &handlerCompatUsage{}
	f.accounts = &handlerCompatAccounts{account: f.account}
	f.snapshot = &fakeSchedulerCache{accounts: []*service.Account{f.account}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	var channel *service.ChannelService
	if len(channels) > 0 {
		channel = channels[0]
	}
	gateway := service.NewGatewayService(f.accounts, &fakeGroupRepo{group: f.group}, f.usage, nil, nil, nil, nil, f.store, cfg, service.NewSchedulerSnapshotService(f.snapshot, nil, nil, nil, nil), nil, service.NewBillingService(cfg, nil), service.NewRateLimitService(f.accounts, nil, cfg, nil, nil), billing, nil, &handlerCompatHTTP{client: upstream.Client()}, service.NewDeferredService(f.accounts, nil, time.Hour), nil, nil, nil, nil, nil, nil, channel, nil, nil, nil, nil)
	f.handler = &GatewayHandler{gatewayService: gateway, cfg: cfg, billingCacheService: billing, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0), maxAccountSwitches: 2}
	return f
}
func (f *handlerCompatFixture) call(entry, body string, writer http.ResponseWriter) *gin.Context {
	c, _ := gin.CreateTestContext(writer)
	path := map[string]string{"messages": "/v1/messages", "responses": "/v1/responses", "chat": "/v1/chat/completions", "count_tokens": "/v1/messages/count_tokens"}[entry]
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "ordinary-sdk/1.0")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, f.group))
	c.Set(string(middleware.ContextKeyAPIKey), f.key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.key.UserID, Concurrency: 10})
	switch entry {
	case "messages":
		f.handler.Messages(c)
	case "responses":
		f.handler.Responses(c)
	case "chat":
		f.handler.ChatCompletions(c)
	case "count_tokens":
		f.handler.CountTokens(c)
	}
	return c
}

func TestGatewayDrainingCompatAccountUsesOrdinaryUnavailableError(t *testing.T) {
	withHandlerResilience(t, nil)
	var ordinary string
	for _, draining := range []bool{false, true} {
		f := newHandlerCompatFixture(t)
		resilience.ProcessNegativeCache().InvalidateGroup(f.group.ID)
		if draining {
			f.account.Extra = map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCode, service.AnthropicCompatNewSessionsExtraKey: false, service.AnthropicCompatIdentitySeedExtraKey: strings.Repeat("ab", 32)}
		} else {
			// An ordinary account with a stale model-mapping snapshot is also
			// temporarily unavailable, although persistent configuration supports it.
			persisted := *f.account
			f.accounts.account = &persisted
			f.account.Credentials = map[string]any{"model_mapping": map[string]any{"other-model": "claude-opus-5-5"}}
		}
		recorder := httptest.NewRecorder()
		f.call("messages", `{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, recorder)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		require.Equal(t, "api_error", gjson.Get(recorder.Body.String(), "error.type").String())
		require.NotContains(t, recorder.Body.String(), "anthropic_compat")
		require.Empty(t, f.wire, "no ineligible account may be forwarded")
		if !draining {
			ordinary = recorder.Body.String()
		} else {
			require.JSONEq(t, ordinary, recorder.Body.String())
		}
	}
}

//go:build unit

package handler

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"testing"
	"time"

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
	var hops atomic.Int32
	testUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		if r.URL.Path != "/next" {
			http.Redirect(w, r, "/next", http.StatusTemporaryRedirect)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(4 * time.Second):
		}
	}))
	defer testUpstream.Close()
	account := service.Account{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{4201}, Credentials: map[string]any{"api_key": "test", "base_url": testUpstream.URL}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
	repo := &openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
	up := &factsRedirectTransport{HTTPUpstream: repository.NewHTTPUpstream(&config.Config{})}
	h, _, router := captureFixture(t, account, up, captureOptions{cache: &capturePinnedCache{owner: account.ID}, repo: repo})
	probe := gin.New()
	probe.Use(router.Handlers...)
	probe.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		resilience.StateFrom(ctx).SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
		c.Next()
	})
	probe.POST("/v1/responses", h.Responses)
	response := captureRequest(probe, "/v1/responses", `{"model":"gpt-5.1","input":"hi","stream":true,"prompt_cache_key":"review"}`)
	require.NotNil(t, up.state)
	fact, ok := up.state.OwnerCutoff()
	t.Logf("status=%d wire_hops=%d published=%t fact_seq=%d", response.Code, hops.Load(), ok, fact.AttemptSeq)
	require.EqualValues(t, 2, hops.Load())
	require.False(t, ok)
}

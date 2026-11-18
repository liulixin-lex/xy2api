//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type followHTTPAccounts struct {
	openAIImagesFailoverAccountRepo
}

func (r followHTTPAccounts) ListByGroup(context.Context, int64) ([]service.Account, error) {
	return r.accounts, nil
}

// The warmup creates a real versioned binding. On the next turn X's HTTP
// guard wins and Y delivers after X's frozen limit, as the last candidate.
func TestFirstOutputFollowHTTPDeliveryWithoutHandoff(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Budget.ModelOverrides = map[string]int{"gpt-5.1": 1}
		s.Budget.MaxAttempts = 2
	})
	db := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: db.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache, store := repository.NewGatewayCache(rdb), repository.NewResilienceStore(rdb)
	const group = int64(4201)
	accounts := []service.Account{}
	for i := int64(1); i <= 2; i++ {
		accounts = append(accounts, service.Account{ID: i, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
			Schedulable: true, Concurrency: 2, Priority: int(i), GroupIDs: []int64{group}, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://capture.example"}})
	}
	repo := followHTTPAccounts{openAIImagesFailoverAccountRepo{accounts: accounts}}
	var session string
	var turn *resilience.RequestState
	up := &captureUpstream{reply: func(n int, req *http.Request, _ []byte) (*http.Response, error) {
		state := resilience.StateFrom(req.Context())
		switch n {
		case 1:
			_, session = state.SessionIdentity()
		case 2:
			turn = state
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(5 * time.Second):
				return nil, errors.New("owner guard did not fire")
			}
		case 3:
			time.Sleep(1100 * time.Millisecond)
		}
		return captureResponse(200, captureSSE(), true), nil
	}}
	h, rec, _ := captureFixture(t, service.Account{}, up, captureOptions{repo: repo, cache: cache})
	h.maxAccountSwitches = 2
	// ClientRequestID installs the production request budget before selection.
	router := gin.New()
	gid := group
	key := &service.APIKey{ID: 1801, UserID: 1701, GroupID: &gid, User: &service.User{ID: 1701, Status: service.StatusActive}}
	router.Use(middleware.ClientRequestID(), func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 2})
		c.Next()
	})
	router.POST("/v1/responses", h.Responses)
	previous := service.Resilience()
	runtime := service.ProvideResilienceService(nil, store, nil, nil, repo, nil, nil, &service.GatewayService{}, h.gatewayService)
	runtime.Stop()
	t.Cleanup(func() { service.SetResilienceInstance(previous) })
	const body = `{"model":"gpt-5.1","input":"hello","prompt_cache_key":"follow-session","stream":true}`
	warmup := captureRequest(router, "/v1/responses", body)
	require.Equal(t, 200, warmup.Code, warmup.Body.String())
	require.NotEmpty(t, session)
	versioned := cache.(service.OpenAIStickyVersionedCache)
	before, err := versioned.GetSessionBinding(context.Background(), group, "openai:"+session)
	require.NoError(t, err)
	require.EqualValues(t, 1, before.AccountID)
	response := captureRequest(router, "/v1/responses", body)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "response.completed")
	require.Len(t, rec.samples, 3)
	require.EqualValues(t, 2, rec.samples[2].AccountID)
	require.Equal(t, schedquality.OutcomeOK, rec.samples[2].Outcome)
	fact, confirmed := turn.ConfirmedOwnerCutoff()
	require.True(t, confirmed)
	require.Equal(t, time.Second, fact.Effective)
	after, err := versioned.GetSessionBinding(context.Background(), group, "openai:"+session)
	require.NoError(t, err)
	require.Equal(t, before, after)
	soft, err := store.SessionAvoid(context.Background(), group, "first_output:"+session)
	require.NoError(t, err)
	require.Equal(t, map[int64]struct{}{1: {}}, soft)
	failed, err := store.SessionAvoid(context.Background(), group, "sticky_change:"+session)
	require.NoError(t, err)
	require.Empty(t, failed)
}

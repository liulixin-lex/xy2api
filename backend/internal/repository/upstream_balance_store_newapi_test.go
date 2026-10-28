package repository

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type newAPICooldownAccounts struct {
	service.AccountRepository
	account service.Account
}

func (r *newAPICooldownAccounts) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

type newAPICooldownHTTP struct{ client *http.Client }

type newAPISpacingLeaderLock struct{ client *redis.Client }

func (l newAPISpacingLeaderLock) TryAcquireLeaderLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return l.client.SetNX(ctx, key, owner, ttl).Result()
}
func (l newAPISpacingLeaderLock) ReleaseLeaderLock(context.Context, string, string) error { return nil }

func TestUpstreamBalanceOriginLockSpacesTokenUsageCalls(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	store := NewUpstreamBalanceStore(client)
	var ctCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`)
		case "/v1/dashboard/billing/subscription":
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":"denied"}`)
		case "/api/usage/token/":
			ctCalls.Add(1)
			_, _ = io.WriteString(w, `{"code":true,"data":{"object":"token_usage","total_available":0,"unlimited_quota":false,"expires_at":0}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	makeService := func(id int64) (*service.UpstreamBalanceService, *newAPICooldownAccounts) {
		repo := &newAPICooldownAccounts{account: service.Account{ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Credentials: map[string]any{"base_url": server.URL, "api_key": "fixture-key", "newapi_access_token": "fixture-token"}}}
		cfg := &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
		ats := service.NewAccountTestService(repo, nil, nil, nil, nil, newAPICooldownHTTP{client: server.Client()}, cfg, nil)
		return service.NewUpstreamBalanceService(repo, ats, store, newAPISpacingLeaderLock{client}), repo
	}
	a, repoA := makeService(701)
	b, repoB := makeService(702)
	ctx := context.Background()
	require.NoError(t, a.RunRound(ctx))
	require.EqualValues(t, 1, ctCalls.Load())
	ownerA, err := client.Get(ctx, "upstream_balance:round").Result()
	require.NoError(t, err)
	mr.FastForward(74 * time.Second)
	repoA.account.ID = 703
	require.NoError(t, a.RunRound(ctx))
	require.EqualValues(t, 1, ctCalls.Load())
	// Transfer round leadership only: the independent CT lock must survive.
	require.NoError(t, client.Del(ctx, "upstream_balance:round").Err())
	require.NoError(t, b.RunRound(ctx))
	require.EqualValues(t, 1, ctCalls.Load())
	ownerB, err := client.Get(ctx, "upstream_balance:round").Result()
	require.NoError(t, err)
	require.NotEqual(t, ownerA, ownerB)
	require.Equal(t, time.Second, mr.TTL(ubCTKey(server.URL)))
	mr.FastForward(time.Second)
	require.NoError(t, client.Del(ctx, "upstream_balance:round").Err())
	repoB.account.ID = 704
	require.NoError(t, b.RunRound(ctx))
	require.EqualValues(t, 2, ctCalls.Load())
	require.Equal(t, 75*time.Second, mr.TTL(ubCTKey(server.URL)))
}

func (u newAPICooldownHTTP) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(r)
}
func (u newAPICooldownHTTP) DoWithTLS(r *http.Request, p string, id int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, c)
}

func TestUpstreamBalanceOriginCooldown(t *testing.T) {
	for _, endpoint := range []string{"/api/status", "/v1/dashboard/billing/subscription", "/v1/dashboard/billing/usage", "/api/user/self", "/api/usage/token/"} {
		t.Run(endpoint, func(t *testing.T) {
			mr := miniredis.RunT(t)
			clientA := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			clientB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer func() { require.NoError(t, clientA.Close()); require.NoError(t, clientB.Close()) }()
			storeA, storeB := NewUpstreamBalanceStore(clientA), NewUpstreamBalanceStore(clientB)
			var calls, ctCalls atomic.Int64
			var limiting atomic.Bool
			limiting.Store(true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/api/usage/token/" {
					ctCalls.Add(1)
				}
				if limiting.Load() && r.URL.Path == endpoint {
					w.Header().Set("Retry-After", "1200")
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{}`)
					return
				}
				switch r.URL.Path {
				case "/api/status":
					_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`)
				case "/v1/dashboard/billing/subscription":
					if endpoint == "/api/usage/token/" && limiting.Load() {
						w.WriteHeader(401)
						_, _ = io.WriteString(w, `{"error":"denied"}`)
					} else {
						_, _ = io.WriteString(w, `{"hard_limit_usd":10}`)
					}
				case "/v1/dashboard/billing/usage":
					_, _ = io.WriteString(w, `{"total_usage":200}`)
				case "/api/user/self":
					_, _ = io.WriteString(w, `{"success":true,"data":{"quota":500}}`)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			account := func(id int64) service.Account {
				return service.Account{ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Credentials: map[string]any{"base_url": server.URL, "api_key": "test-key", "newapi_access_token": "example-token"}}
			}
			newService := func(id int64, store service.UpstreamBalanceStore) *service.UpstreamBalanceService {
				repo := &newAPICooldownAccounts{account: account(id)}
				ats := service.NewAccountTestService(repo, nil, nil, nil, nil, newAPICooldownHTTP{client: server.Client()}, &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}, nil)
				return service.NewUpstreamBalanceService(repo, ats, store, nil)
			}
			svcA, svcB := newService(1, storeA), newService(2, storeB)
			require.NoError(t, svcA.RunRound(context.Background()))
			initialCalls := calls.Load()
			require.Positive(t, initialCalls)
			entries, err := storeA.Load(context.Background(), []int64{1})
			require.NoError(t, err)
			require.Equal(t, "rate_limited", entries.States[1].LastReason)
			if endpoint == "/api/usage/token/" {
				require.EqualValues(t, 1, ctCalls.Load())
				require.Empty(t, entries.States[1].AuthEpisode)
			}
			deadline, err := storeB.GetCooldown(context.Background(), server.URL)
			require.NoError(t, err)
			require.NotNil(t, deadline)
			require.WithinDuration(t, time.Now().Add(1200*time.Second), *deadline, 2*time.Second)
			require.InDelta(t, float64(1200*time.Second), float64(mr.TTL(ubCooldownKey(server.URL))), float64(2*time.Second))
			for _, advance := range []time.Duration{0, time.Minute, 1139 * time.Second} {
				mr.FastForward(advance)
				require.NoError(t, svcB.RunRound(context.Background()))
				require.Equal(t, initialCalls, calls.Load(), "account B must send nothing during the shared cooldown")
				// Keep account B due to prove the origin deadline, independently of its own retry state.
				require.NoError(t, clientB.Del(context.Background(), ubStateKey(2)).Err())
			}
			require.True(t, mr.Exists(ubCooldownKey(server.URL)))
			mr.FastForward(time.Second)
			require.False(t, mr.Exists(ubCooldownKey(server.URL)))
			limiting.Store(false)
			require.NoError(t, svcB.RunRound(context.Background()))
			require.Greater(t, calls.Load(), initialCalls)
			entries, err = storeB.Load(context.Background(), []int64{2})
			require.NoError(t, err)
			require.Equal(t, 5.0, *entries.Records[2].AmountUSD)
		})
	}
}

func TestUpstreamBalanceNewAPISiteCacheRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	store := NewUpstreamBalanceStore(client)
	ctx := context.Background()
	origin := "https://example.test"
	site := &service.UpstreamBalanceSite{Version: 1, QuotaPerUnit: 500000, DisplayType: "CNY", USDExchangeRate: 7, FetchedAt: time.Now().UTC()}
	require.NoError(t, store.SaveSite(ctx, origin, site, 30*time.Minute))
	got, err := store.GetSite(ctx, origin)
	require.NoError(t, err)
	require.Equal(t, site, got)
	require.Equal(t, 30*time.Minute, mr.TTL(ubSiteKey(origin)))
	mr.FastForward(30 * time.Minute)
	got, err = store.GetSite(ctx, origin)
	require.NoError(t, err)
	require.Nil(t, got)
}

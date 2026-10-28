package service

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

	"github.com/stretchr/testify/require"
)

const (
	newAPIStatusPath = "/api/status"
	newAPISubPath    = "/v1/dashboard/billing/subscription"
	newAPIUsagePath  = "/v1/dashboard/billing/usage"
	newAPIWalletPath = "/api/user/self"
	newAPICTPath     = "/api/usage/token/"
)

type newAPIReply struct {
	status int
	body   string
	header http.Header
}
type newAPITestStore struct {
	upstreamBalanceMemoryStore
	siteMu      sync.Mutex
	sites       map[string]*UpstreamBalanceSite
	expires     map[string]time.Time
	cooldowns   map[string]time.Time
	locks       map[string]time.Time
	now         func() time.Time
	siteTTL     time.Duration
	lockTTL     time.Duration
	lockCalls   int
	lockHook    func()
	cooldownErr error
}

func (s *newAPITestStore) GetSite(_ context.Context, origin string) (*UpstreamBalanceSite, error) {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if !s.now().Before(s.expires[origin]) {
		return nil, nil
	}
	return s.sites[origin], nil
}
func (s *newAPITestStore) SaveSite(_ context.Context, origin string, site *UpstreamBalanceSite, ttl time.Duration) error {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	s.sites[origin], s.expires[origin], s.siteTTL = site, s.now().Add(ttl), ttl
	return nil
}
func (s *newAPITestStore) GetCooldown(_ context.Context, origin string) (*time.Time, error) {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if s.cooldownErr != nil {
		return nil, s.cooldownErr
	}
	until := s.cooldowns[origin]
	if !s.now().Before(until) {
		return nil, nil
	}
	return &until, nil
}
func (s *newAPITestStore) SaveCooldown(_ context.Context, origin string, until time.Time) error {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	if until.After(s.cooldowns[origin]) {
		s.cooldowns[origin] = until
	}
	return nil
}
func (s *newAPITestStore) TryLockOrigin(_ context.Context, origin, _ string, ttl time.Duration) (bool, error) {
	s.siteMu.Lock()
	defer s.siteMu.Unlock()
	s.lockCalls++
	s.lockTTL = ttl
	if s.lockHook != nil {
		s.lockHook()
	}
	if s.now().Before(s.locks[origin]) {
		return false, nil
	}
	s.locks[origin] = s.now().Add(ttl)
	return true, nil
}

type newAPIFixture struct {
	t       *testing.T
	mu      sync.Mutex
	replies map[string]newAPIReply
	calls   map[string]int
	headers map[string]http.Header
	server  *httptest.Server
	svc     *UpstreamBalanceService
	store   *newAPITestStore
	account Account
	state   UpstreamBalanceState
	now     time.Time
}

func newAPIFixtureFor(t *testing.T) *newAPIFixture {
	f := &newAPIFixture{t: t, replies: map[string]newAPIReply{}, calls: map[string]int{}, headers: map[string]http.Header{}, now: time.Now().UTC()}
	f.reply(newAPIStatusPath, 200, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`)
	f.reply(newAPISubPath, 200, `{"hard_limit_usd":10}`)
	f.reply(newAPIUsagePath, 200, `{"total_usage":200}`)
	f.reply(newAPIWalletPath, 200, `{"success":true,"data":{"quota":500}}`)
	f.reply(newAPICTPath, 200, `{"code":true,"data":{"object":"token_usage","total_available":0,"unlimited_quota":false,"expires_at":0}}`)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		reply, exists := f.replies[r.URL.Path]
		f.calls[r.URL.Path]++
		f.headers[r.URL.Path] = r.Header.Clone()
		f.mu.Unlock()
		if !exists {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query for %s", r.URL.Path)
		}
		for k, values := range reply.header {
			w.Header()[k] = values
		}
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
	}))
	t.Cleanup(f.server.Close)
	f.account = upstreamBalanceCandidate(901, StatusActive)
	f.account.Credentials["base_url"] = f.server.URL + "/v1"
	f.state.Fingerprint = upstreamBalanceFingerprint(&f.account, upstreamBalanceEffectiveBaseURL(&f.account))
	f.store = &newAPITestStore{sites: map[string]*UpstreamBalanceSite{}, expires: map[string]time.Time{}, cooldowns: map[string]time.Time{}, locks: map[string]time.Time{}, now: func() time.Time { return f.now }}
	f.svc = NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(f.server)}, f.store, nil)
	f.svc.now = func() time.Time { return f.now }
	return f
}
func (f *newAPIFixture) reply(path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[path] = newAPIReply{status: status, body: body}
}
func (f *newAPIFixture) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}
func (f *newAPIFixture) collect() ubOutcome {
	return f.svc.collectNewAPI(context.Background(), &f.account, upstreamBalanceEffectiveBaseURL(&f.account), &f.state, &upstreamBalanceRoundMemo{})
}
func (f *newAPIFixture) save(out ubOutcome) {
	f.svc.persistOutcome(context.Background(), f.now, f.account.ID, &f.state, out)
}

func TestUpstreamBalanceNewAPIStatusParsing(t *testing.T) {
	for _, tc := range []struct {
		name, body, typ string
		ok              bool
	}{
		{"usd", `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"usd"}}`, "USD", true},
		{"cny", `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"cny","usd_exchange_rate":7}}`, "CNY", true},
		{"tokens", `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"TOKENS"}}`, "TOKENS", true},
		{"fallback tokens", `{"success":true,"data":{"quota_per_unit":100,"display_in_currency":false}}`, "TOKENS", true},
		{"fallback usd", `{"success":true,"data":{"quota_per_unit":100}}`, "USD", true},
		{"fallback currency", `{"success":true,"data":{"quota_per_unit":100,"display_in_currency":true}}`, "USD", true},
		{"false", `{"success":false,"data":{"quota_per_unit":100}}`, "", false},
		{"missing success", `{"data":{"quota_per_unit":100}}`, "", false},
		{"zero quota", `{"success":true,"data":{"quota_per_unit":0}}`, "", false},
		{"negative quota", `{"success":true,"data":{"quota_per_unit":-1}}`, "", false},
		{"string quota", `{"success":true,"data":{"quota_per_unit":"100"}}`, "", false},
		{"overflow quota", `{"success":true,"data":{"quota_per_unit":1e999}}`, "", false},
		{"html", `<html>login</html>`, "", false},
		{"malformed", `{`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPIFixtureFor(t)
			f.reply(newAPIStatusPath, 200, tc.body)
			out := f.collect()
			require.Equal(t, tc.ok, out.success)
			if tc.ok {
				require.Equal(t, tc.typ, f.store.sites[f.server.URL].DisplayType)
			} else {
				require.True(t, out.notNewAPI)
				require.Zero(t, f.count(newAPISubPath))
			}
		})
	}
}

func TestUpstreamBalanceNewAPIStatusCachedPerOrigin(t *testing.T) {
	f := newAPIFixtureFor(t)
	require.True(t, f.collect().success)
	require.Equal(t, 30*time.Minute, f.store.siteTTL)
	require.Equal(t, f.now, f.store.sites[f.server.URL].FetchedAt)
	f.account.ID++
	f.account.Credentials["base_url"] = f.server.URL + "/v1beta"
	f.reply("/v1beta/dashboard/billing/subscription", 200, `{"hard_limit_usd":10}`)
	f.reply("/v1beta/dashboard/billing/usage", 200, `{"total_usage":200}`)
	f.now = f.now.Add(30*time.Minute - time.Second)
	require.True(t, f.collect().success)
	require.Equal(t, 1, f.count(newAPIStatusPath))
	f.now = f.now.Add(time.Second)
	require.True(t, f.collect().success)
	require.Equal(t, 2, f.count(newAPIStatusPath))
}

func TestUpstreamBalanceNewAPICurrencyConversion(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		amount       float64
		success      bool
	}{
		{"usd", `"quota_display_type":"USD"`, 8, true},
		{"cny", `"quota_display_type":"CNY","usd_exchange_rate":4`, 2, true},
		{"tokens", `"quota_display_type":"TOKENS"`, .08, true},
		{"fallback tokens", `"display_in_currency":false`, .08, true},
		{"fallback usd", `"display_in_currency":true`, 8, true},
		{"unknown", `"quota_display_type":"EUR"`, 0, false},
		{"missing rate", `"quota_display_type":"CNY"`, 0, false},
		{"zero rate", `"quota_display_type":"CNY","usd_exchange_rate":0`, 0, false},
		{"negative rate", `"quota_display_type":"CNY","usd_exchange_rate":-1`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPIFixtureFor(t)
			f.reply(newAPIStatusPath, 200, `{"success":true,"data":{"quota_per_unit":100,`+tc.fields+`}}`)
			out := f.collect()
			require.Equal(t, tc.success, out.success)
			if tc.success {
				require.InDelta(t, tc.amount, *out.amount, 1e-9)
				require.Empty(t, out.basis)
			} else {
				require.Equal(t, "no_data", out.reason)
			}
		})
	}
}

func TestUpstreamBalanceNewAPIKeyOnlyLimitedAndUnlimitedSentinel(t *testing.T) {
	for _, typ := range []string{"USD", "CNY"} {
		for _, limit := range []float64{10, 1e8 - 1, 1e8, 1e8 + 1} {
			t.Run(fmt.Sprintf("%s_%g", typ, limit), func(t *testing.T) {
				f := newAPIFixtureFor(t)
				f.reply(newAPIStatusPath, 200, fmt.Sprintf(`{"success":true,"data":{"quota_per_unit":100,"quota_display_type":%q,"usd_exchange_rate":2}}`, typ))
				f.reply(newAPISubPath, 200, fmt.Sprintf(`{"hard_limit_usd":%g}`, limit))
				out := f.collect()
				require.True(t, out.success)
				require.Empty(t, out.basis)
				require.Equal(t, limit == 1e8, out.unlimited)
				if out.unlimited {
					require.Nil(t, out.amount)
				} else {
					want := limit - 2
					if typ == "CNY" {
						want /= 2
					}
					require.Equal(t, want, *out.amount)
				}
				require.Zero(t, f.count(newAPICTPath))
			})
		}
	}
	for _, path := range []string{newAPISubPath, newAPIUsagePath} {
		for _, body := range []string{`{}`, `null`, `{"hard_limit_usd":"10","total_usage":"20"}`, `{"hard_limit_usd":1e999,"total_usage":1e999}`, `{"hard_limit_usd":null,"total_usage":null}`} {
			t.Run(path+body, func(t *testing.T) {
				f := newAPIFixtureFor(t)
				f.reply(path, 200, body)
				out := f.collect()
				require.False(t, out.success)
				require.Equal(t, "upstream_error", out.reason)
			})
		}
	}
}

func TestUpstreamBalanceNewAPITokensSentinelNoData(t *testing.T) {
	for _, token := range []string{"", "configured", "invalid token"} {
		t.Run(token, func(t *testing.T) {
			f := newAPIFixtureFor(t)
			f.account.Credentials["newapi_access_token"] = token
			f.reply(newAPIStatusPath, 200, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"TOKENS"}}`)
			f.reply(newAPISubPath, 200, `{"hard_limit_usd":100000000}`)
			// Wallet greater than the ambiguous billing number must still win.
			f.reply(newAPIWalletPath, 200, `{"success":true,"data":{"quota":200000000}}`)
			out := f.collect()
			f.save(out)
			require.False(t, out.unlimited)
			if token == "configured" {
				require.True(t, out.success)
				require.Equal(t, 2000000.0, *out.amount)
				require.Equal(t, "wallet", out.basis)
			} else {
				require.False(t, out.success)
				require.Nil(t, f.store.savedRecords[f.account.ID])
				if token == "" {
					require.Equal(t, "no_data", out.reason)
				}
			}
		})
	}
}

func TestUpstreamBalanceNewAPINegativeValues(t *testing.T) {
	for _, wallet := range []bool{false, true} {
		f := newAPIFixtureFor(t)
		f.reply(newAPISubPath, 200, `{"hard_limit_usd":1}`)
		f.reply(newAPIUsagePath, 200, `{"total_usage":420}`)
		if wallet {
			f.account.Credentials["newapi_access_token"] = "configured"
			f.reply(newAPIWalletPath, 200, `{"success":true,"data":{"quota":-550}}`)
		}
		out := f.collect()
		require.True(t, out.success)
		want := -3.2
		if wallet {
			want = -5.5
		}
		require.InDelta(t, want, *out.amount, 1e-9)
	}
}

func TestUpstreamBalanceBillingFailureSkipsFollowups(t *testing.T) {
	for _, path := range []string{newAPISubPath, newAPIUsagePath} {
		for _, status := range []int{302, 400, 403, 404, 405, 429, 500} {
			t.Run(fmt.Sprintf("%s_%d", path, status), func(t *testing.T) {
				f := newAPIFixtureFor(t)
				f.account.Credentials["newapi_access_token"] = "configured"
				f.reply(path, status, `{"error":"denied"}`)
				out := f.collect()
				require.False(t, out.success)
				require.Zero(t, f.count(newAPIWalletPath))
				require.Zero(t, f.count(newAPICTPath))
				if path == newAPISubPath {
					require.Zero(t, f.count(newAPIUsagePath))
				}
				require.Equal(t, status == 404 || status == 405, out.deleteKind)
			})
		}
	}
}

func TestUpstreamBalanceNewAPIJSONAuthClassification(t *testing.T) {
	for _, path := range []string{newAPISubPath, newAPIUsagePath, newAPIWalletPath, newAPICTPath} {
		for _, status := range []int{401, 403} {
			for _, body := range []string{` <html>blocked</html>`, ` {"error":"denied"}`} {
				t.Run(fmt.Sprintf("%s_%d_%s", path, status, body), func(t *testing.T) {
					f := newAPIFixtureFor(t)
					if path == newAPIWalletPath {
						f.account.Credentials["newapi_access_token"] = "configured"
					}
					if path == newAPICTPath {
						f.reply(newAPISubPath, 401, `{}`)
					}
					f.reply(path, status, body)
					out := f.collect()
					if strings.Contains(body, "html") {
						require.Equal(t, "upstream_error", out.reason)
						require.Zero(t, f.state.TokenFailures)
						if path != newAPICTPath {
							require.Zero(t, f.count(newAPICTPath))
						}
					} else if path == newAPIWalletPath {
						require.Equal(t, "token_invalid", out.reason)
						require.Equal(t, 1, f.state.TokenFailures)
					} else if status == 401 && path != newAPICTPath {
						require.Equal(t, "key_exhausted", out.reason)
						require.Equal(t, 1, f.count(newAPICTPath))
					} else {
						require.Equal(t, "auth_failed", out.reason)
					}
				})
			}
		}
	}
}

func TestUpstreamBalanceNewAPI401DisambiguatesWithTokenUsage(t *testing.T) {
	for _, path := range []string{newAPISubPath, newAPIUsagePath} {
		t.Run(path, func(t *testing.T) {
			f := newAPIFixtureFor(t)
			f.reply(path, 401, `{}`)
			out := f.collect()
			require.Equal(t, "key_exhausted", out.reason)
			require.Equal(t, 1, f.count(newAPICTPath))
			require.Equal(t, 75*time.Second, f.store.lockTTL)
			require.Equal(t, "Bearer test-key", f.headers[newAPICTPath].Get("Authorization"))
			require.Zero(t, f.count(newAPIWalletPath))
		})
	}
}

func TestUpstreamBalanceNewAPIHTTP200SuccessFalseIsAuthFailure(t *testing.T) {
	for _, path := range []string{newAPISubPath, newAPIUsagePath, newAPICTPath} {
		for _, body := range []string{`{"success":false}`, `{"success":false,"code":401}`, `{"code":false}`, `{"code":true,"success":false,"data":{"object":"token_usage","total_available":0}}`} {
			t.Run(path+body, func(t *testing.T) {
				f := newAPIFixtureFor(t)
				if path == newAPICTPath {
					f.reply(newAPISubPath, 401, `{}`)
				}
				f.reply(path, 200, body)
				out := f.collect()
				f.save(out)
				require.Equal(t, "auth_failed", out.reason)
				require.False(t, out.success)
				require.Nil(t, f.store.savedRecords[f.account.ID])
			})
		}
	}
}

func TestUpstreamBalanceTokenUsagePositiveIsAuthFailed(t *testing.T) {
	for _, extra := range []string{`"expires_at":0`, `"expires_at":-1`, `"expires_at":9999999999`, `"unlimited_quota":true,"expires_at":0`} {
		f := newAPIFixtureFor(t)
		f.reply(newAPISubPath, 401, `{}`)
		f.reply(newAPICTPath, 200, `{"code":true,"data":{"object":"token_usage","total_available":12,`+extra+`}}`)
		out := f.collect()
		f.save(out)
		require.Equal(t, "auth_failed", out.reason)
		require.False(t, out.success)
		require.Nil(t, f.store.savedRecords[f.account.ID])
		require.Equal(t, "auth_failed", f.state.AuthEpisode)
		require.Equal(t, f.now, *f.state.AuthEpisodeAt)
	}
}

func TestUpstreamBalanceTokenUsageExpiredIsExhausted(t *testing.T) {
	for _, tc := range []struct {
		available int
		unlimited bool
		expires   int64
	}{{12, false, 1}, {12, true, 1}, {0, false, 0}, {-3, false, -1}, {0, false, -1}} {
		f := newAPIFixtureFor(t)
		f.reply(newAPISubPath, 401, `{}`)
		f.reply(newAPICTPath, 200, fmt.Sprintf(`{"code":true,"data":{"object":"token_usage","total_available":%d,"unlimited_quota":%t,"expires_at":%d}}`, tc.available, tc.unlimited, tc.expires))
		out := f.collect()
		f.save(out)
		require.True(t, out.success)
		require.Equal(t, "key_exhausted", out.reason)
		require.Equal(t, 0.0, *f.store.savedRecords[f.account.ID].AmountUSD)
		require.Equal(t, "key_exhausted", f.state.AuthEpisode)
		require.Empty(t, out.basis)
	}
}

func TestUpstreamBalanceAuthEpisodeExpiry(t *testing.T) {
	for _, episode := range []string{"auth_failed", "key_exhausted"} {
		f := newAPIFixtureFor(t)
		f.reply(newAPISubPath, 401, `{}`)
		f.state.AuthEpisode = episode
		at := f.now
		f.state.AuthEpisodeAt = &at
		f.now = f.now.Add(time.Hour - time.Second)
		out := f.collect()
		require.Equal(t, episode, out.reason)
		require.Zero(t, f.count(newAPICTPath))
		f.now = f.now.Add(time.Second)
		out = f.collect()
		require.Equal(t, "key_exhausted", out.reason)
		require.Equal(t, 1, f.count(newAPICTPath))
		f.reply(newAPISubPath, 200, `{"hard_limit_usd":10}`)
		require.True(t, f.collect().success)
		require.Empty(t, f.state.AuthEpisode)
		require.Nil(t, f.state.AuthEpisodeAt)
	}
}

func TestUpstreamBalanceNewAPITokenHeaderStyles(t *testing.T) {
	for _, tc := range []struct {
		name, token, uid, auth string
		calls                  int
	}{
		{"rc", "  Bearer example-token  ", "", "Bearer example-token", 1},
		{"v0", " Bearer example-token ", "123", "example-token", 1},
		{"rc plain", "example-token", "", "Bearer example-token", 1},
		{"bad uid", "example-token", "bad", "", 0},
		{"too long uid", "example-token", "123456789012345678901", "", 0},
		{"whitespace token", "a b", "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPIFixtureFor(t)
			f.account.Credentials["newapi_access_token"] = tc.token
			f.account.Credentials["newapi_user_id"] = tc.uid
			f.account.Credentials[credKeyHeaderOverrideEnabled] = true
			f.account.Credentials["header_overrides"] = map[string]any{"Authorization": "wrong", "New-Api-User": "wrong"}
			out := f.collect()
			require.True(t, out.success)
			require.Equal(t, tc.calls, f.count(newAPIWalletPath))
			if tc.calls > 0 {
				require.Equal(t, tc.auth, f.headers[newAPIWalletPath].Get("Authorization"))
				require.Equal(t, tc.uid, f.headers[newAPIWalletPath].Get("New-Api-User"))
			} else {
				require.Equal(t, "token_invalid", out.reason)
				require.Equal(t, 3, f.state.TokenFailures)
			}
		})
	}
}

func TestUpstreamBalanceNewAPITokenFallbackAfterThreeFailures(t *testing.T) {
	f := newAPIFixtureFor(t)
	f.account.Credentials["newapi_access_token"] = "configured"
	f.reply(newAPIWalletPath, 200, `{"success":false}`)
	for i := 1; i <= 3; i++ {
		out := f.collect()
		f.save(out)
		require.Equal(t, "token_invalid", out.reason)
		require.Equal(t, i, f.state.TokenFailures)
		require.Equal(t, i == 3, out.success)
		if i < 3 {
			require.Nil(t, f.store.savedRecords[f.account.ID])
			require.Equal(t, f.now.Add(time.Minute), f.state.NextDueAt)
		}
	}
	retry := f.now.Add(30 * time.Minute)
	require.Equal(t, retry, *f.state.TokenRetryAt)
	require.Equal(t, 8.0, *f.store.savedRecords[f.account.ID].AmountUSD)
	require.Empty(t, f.store.savedRecords[f.account.ID].Basis)
	f.now = retry.Add(-time.Second)
	require.True(t, f.collect().success)
	require.Equal(t, 3, f.count(newAPIWalletPath))
	f.now = retry
	require.True(t, f.collect().success)
	require.Equal(t, 4, f.count(newAPIWalletPath))
	require.Equal(t, retry.Add(30*time.Minute), *f.state.TokenRetryAt)
	f.now = *f.state.TokenRetryAt
	f.reply(newAPIWalletPath, 200, `{"success":true,"data":{"quota":500}}`)
	out := f.collect()
	require.True(t, out.success)
	require.Empty(t, out.reason)
	require.Equal(t, 5.0, *out.amount)
	require.Zero(t, f.state.TokenFailures)
	require.Nil(t, f.state.TokenRetryAt)
}

func TestUpstreamBalanceNewAPITokenTransientDoesNotCountFailure(t *testing.T) {
	for _, status := range []int{302, 429, 500, 503} {
		f := newAPIFixtureFor(t)
		f.account.Credentials["newapi_access_token"] = "configured"
		f.reply(newAPIWalletPath, status, `{}`)
		out := f.collect()
		require.False(t, out.success)
		require.Zero(t, f.state.TokenFailures)
		require.Nil(t, f.state.TokenRetryAt)
	}
}

func TestUpstreamBalanceNewAPIEffectiveIsMinOfKeyAndWallet(t *testing.T) {
	for _, tc := range []struct {
		key, wallet float64
		basis       string
	}{{2, 5, "key"}, {8, 5, "wallet"}, {5, 5, "wallet"}, {1e8, 5, "wallet"}, {-3, -5, "wallet"}} {
		f := newAPIFixtureFor(t)
		f.account.Credentials["newapi_access_token"] = "configured"
		f.reply(newAPISubPath, 200, fmt.Sprintf(`{"hard_limit_usd":%g}`, tc.key))
		f.reply(newAPIUsagePath, 200, `{"total_usage":0}`)
		f.reply(newAPIWalletPath, 200, fmt.Sprintf(`{"success":true,"data":{"quota":%g}}`, tc.wallet*100))
		out := f.collect()
		require.True(t, out.success)
		require.Equal(t, min(tc.key, tc.wallet), *out.amount)
		require.Equal(t, tc.basis, out.basis)
		require.False(t, out.unlimited)
	}
}

func TestUpstreamBalanceNoBasisCalls(t *testing.T) {
	for _, token := range []string{"", "configured"} {
		for _, limit := range []int{10, 100000000} {
			f := newAPIFixtureFor(t)
			f.account.Credentials["newapi_access_token"] = token
			f.reply(newAPISubPath, 200, fmt.Sprintf(`{"hard_limit_usd":%d}`, limit))
			require.True(t, f.collect().success)
			require.Zero(t, f.count(newAPICTPath))
			require.Zero(t, f.store.lockCalls)
		}
	}
}

func TestUpstreamBalanceNewAPICooldownBeforeNestedCall(t *testing.T) {
	f := newAPIFixtureFor(t)
	f.reply(newAPISubPath, 401, `{}`)
	f.store.lockHook = func() { f.store.cooldowns[f.server.URL] = f.now.Add(20 * time.Minute) }
	out := f.collect()
	require.Equal(t, "rate_limited", out.reason)
	require.Equal(t, 20*time.Minute, out.retryAfter)
	require.Zero(t, f.count(newAPICTPath))
	require.Empty(t, f.state.AuthEpisode)
}

func TestUpstreamBalanceNewAPICooldownEveryEndpoint(t *testing.T) {
	for _, path := range []string{newAPIStatusPath, newAPISubPath, newAPIUsagePath, newAPIWalletPath, newAPICTPath} {
		t.Run(path, func(t *testing.T) {
			f := newAPIFixtureFor(t)
			f.account.Credentials["newapi_access_token"] = "configured"
			if path == newAPICTPath {
				f.reply(newAPISubPath, 401, `{}`)
			}
			f.reply(path, 429, strings.Repeat("x", upstreamBalanceStatusMaxBodyBytes+1))
			out := f.collect()
			require.Equal(t, "rate_limited", out.reason)
			require.Equal(t, 5*time.Minute, out.retryAfter)
			require.Equal(t, f.now.Add(5*time.Minute), f.store.cooldowns[f.server.URL])
			before := f.count(newAPIStatusPath) + f.count(newAPISubPath)
			f.account.ID++
			out = f.collect()
			require.Equal(t, "rate_limited", out.reason)
			require.Equal(t, before, f.count(newAPIStatusPath)+f.count(newAPISubPath))
		})
	}
}

func TestUpstreamBalanceNewAPICooldownStoreErrorSendsNothing(t *testing.T) {
	f := newAPIFixtureFor(t)
	f.store.cooldownErr = context.DeadlineExceeded
	memo := &upstreamBalanceRoundMemo{}
	f.svc.pollOne(context.Background(), f.now, f.account.ID, &f.account, &f.state, memo)
	require.True(t, memo.failed())
	require.Empty(t, f.calls)
	require.Empty(t, f.store.savedStates)
}

func TestUpstreamBalanceNewAPICooldownKeepsResponseDeadline(t *testing.T) {
	f := newAPIFixtureFor(t)
	f.reply(newAPISubPath, 429, `{}`)
	out := f.collect()
	deadline := f.store.cooldowns[f.server.URL]
	f.now = f.now.Add(2 * time.Second)
	f.save(out)
	require.Equal(t, deadline, *f.state.RetryNotBefore)
	require.False(t, upstreamBalanceDue(&f.state, f.state.Fingerprint, deadline.Add(-time.Second), deadline.Add(-time.Second)))
	require.True(t, upstreamBalanceDue(&f.state, f.state.Fingerprint, deadline, deadline))
}

func TestUpstreamBalanceNewAPIStatusBodyCap(t *testing.T) {
	for _, size := range []int{70 * 1024, upstreamBalanceStatusMaxBodyBytes} {
		f := newAPIFixtureFor(t)
		f.reply(newAPIStatusPath, 200, `{"success":true,"data":{"quota_per_unit":100},"ignored":"`+strings.Repeat("x", size)+`"}`)
		out := f.collect()
		require.Equal(t, size < upstreamBalanceStatusMaxBodyBytes, out.success)
		if size == upstreamBalanceStatusMaxBodyBytes {
			require.Equal(t, "upstream_error", out.reason)
			require.Zero(t, f.count(newAPISubPath))
		}
	}
}

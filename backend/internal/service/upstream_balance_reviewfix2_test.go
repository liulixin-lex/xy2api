package service

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These operations use the account context. An expired account deadline must
// not stop other accounts or report a Redis outage.
type upstreamBalanceBudgetStore struct {
	*newAPITestStore
	operation     string
	blockedOrigin string
}

func (s *upstreamBalanceBudgetStore) Load(ctx context.Context, ids []int64) (*UpstreamBalanceEntries, error) {
	if s.operation == "Load" && len(ids) == 1 && ids[0] <= 4 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.newAPITestStore.Load(ctx, ids)
}
func (s *upstreamBalanceBudgetStore) GetSite(ctx context.Context, origin string) (*UpstreamBalanceSite, error) {
	if s.operation == "GetSite" && origin == s.blockedOrigin {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.newAPITestStore.GetSite(ctx, origin)
}
func (s *upstreamBalanceBudgetStore) SaveSite(ctx context.Context, origin string, site *UpstreamBalanceSite, ttl time.Duration) error {
	if s.operation == "SaveSite" && origin == s.blockedOrigin {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.newAPITestStore.SaveSite(ctx, origin, site, ttl)
}
func (s *upstreamBalanceBudgetStore) SaveKind(ctx context.Context, id int64, kind *UpstreamBalanceKindRecord, ttl time.Duration) error {
	if s.operation == "SaveKind" && id <= 4 {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.newAPITestStore.SaveKind(ctx, id, kind, ttl)
}
func (s *upstreamBalanceBudgetStore) GetCooldown(ctx context.Context, origin string) (*time.Time, error) {
	if s.operation == "GetCooldown" && origin == s.blockedOrigin {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.newAPITestStore.GetCooldown(ctx, origin)
}
func (s *upstreamBalanceBudgetStore) TryLockOrigin(ctx context.Context, origin, owner string, ttl time.Duration) (bool, error) {
	if s.operation == "TryLockOrigin" && origin == s.blockedOrigin {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return s.newAPITestStore.TryLockOrigin(ctx, origin, owner, ttl)
}

func TestUpstreamBalanceStoreAccountBudgetDoesNotPauseRound(t *testing.T) {
	for _, operation := range []string{"GetSite", "SaveSite", "SaveKind", "Load", "GetCooldown", "TryLockOrigin"} {
		t.Run(operation, func(t *testing.T) {
			origin := "https://budget.example"
			accounts := make([]Account, 5)
			for i := range accounts {
				accounts[i] = upstreamBalanceCandidate(int64(i+1), StatusActive)
				accounts[i].Credentials["base_url"] = origin
			}
			accounts[4].Credentials["base_url"] = "https://healthy.example"
			entries := balanceEntriesFor(accounts, "newapi")
			entries.Kinds[5].Kind = "sub2api"
			if operation == "SaveKind" {
				for id := int64(1); id <= 4; id++ {
					delete(entries.Kinds, id)
				}
			}
			site := &UpstreamBalanceSite{QuotaPerUnit: 100, DisplayType: "USD"}
			sites := map[string]*UpstreamBalanceSite{origin: site}
			if operation == "GetSite" || operation == "SaveSite" {
				delete(sites, origin)
			}
			baseStore := &newAPITestStore{upstreamBalanceMemoryStore: upstreamBalanceMemoryStore{entries: entries}, sites: sites, expires: map[string]time.Time{origin: time.Now().Add(time.Hour)}, cooldowns: map[string]time.Time{}, locks: map[string]time.Time{}, now: time.Now}
			store := &upstreamBalanceBudgetStore{newAPITestStore: baseStore, operation: operation, blockedOrigin: origin}
			upstream := balanceCallbackHTTP(func(r *http.Request) (*http.Response, error) {
				body := `{"mode":"unrestricted","isValid":true,"balance":7}`
				status := 200
				switch r.URL.Path {
				case newAPIStatusPath:
					body = `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`
				case newAPISubPath:
					status = 401
					body = `{"error":"denied"}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
			})
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(previous)
			svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: accounts}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
			svc.accountBudget = 40 * time.Millisecond
			require.NoError(t, svc.RunRound(context.Background()))
			require.NotNil(t, store.savedStates[1])
			require.Equal(t, "upstream_error", store.savedStates[1].LastReason)
			require.Equal(t, 1, store.savedStates[1].TransientStreak)
			require.NotNil(t, store.savedRecords[5])
			require.Equal(t, 7.0, *store.savedRecords[5].AmountUSD)
			require.NotContains(t, logs.String(), "upstream_balance.paused")
		})
	}
}

type upstreamBalanceSlowSuccessStore struct {
	upstreamBalanceMemoryStore
	attempts int
}

func (s *upstreamBalanceSlowSuccessStore) SaveSuccess(ctx context.Context, id int64, rec *UpstreamBalanceRecord, st *UpstreamBalanceState) error {
	time.Sleep(80 * time.Millisecond)
	return s.upstreamBalanceMemoryStore.SaveSuccess(ctx, id, rec, st)
}
func (s *upstreamBalanceSlowSuccessStore) SaveAttempt(ctx context.Context, id int64, st *UpstreamBalanceState) error {
	s.attempts++
	return s.upstreamBalanceMemoryStore.SaveAttempt(ctx, id, st)
}
func TestUpstreamBalanceBudgetDoesNotOverwriteSuccess(t *testing.T) {
	a := upstreamBalanceCandidate(850, StatusActive)
	store := &upstreamBalanceSlowSuccessStore{upstreamBalanceMemoryStore: upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "sub2api")}}
	upstream := balanceCallbackHTTP(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(`{"mode":"unrestricted","isValid":true,"balance":7}`))}, nil
	})
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
	svc.accountBudget = 40 * time.Millisecond
	require.NoError(t, svc.RunRound(context.Background()))
	require.NotNil(t, store.savedRecords[a.ID])
	require.Equal(t, 7.0, *store.savedRecords[a.ID].AmountUSD)
	require.Empty(t, store.savedStates[a.ID].LastReason)
	require.Zero(t, store.savedStates[a.ID].TransientStreak)
	require.Nil(t, store.savedStates[a.ID].RetryNotBefore)
	require.Zero(t, store.attempts)
}

func TestUpstreamBalanceBillingPairResetsAuthStreak(t *testing.T) {
	t.Run("malformed usage", func(t *testing.T) {
		f := newAPIFixtureFor(t)
		f.reply(newAPIUsagePath, 200, "<html>ok</html>")
		f.state.AuthStreak = 4
		out := f.collect()
		require.Equal(t, "upstream_error", out.reason)
		f.save(out)
		require.Zero(t, f.state.AuthStreak)
	})
	f := newAPIFixtureFor(t)
	f.reply(newAPIStatusPath, 200, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"TOKENS"}}`)
	f.reply(newAPISubPath, 200, `{"hard_limit_usd":100000000}`)
	f.state.AuthStreak = 4
	out := f.collect()
	require.Equal(t, "no_data", out.reason)
	f.save(out)
	require.Zero(t, f.state.AuthStreak)
	f.reply(newAPISubPath, 401, `{"error":"denied"}`)
	f.reply(newAPICTPath, 200, `{"code":true,"data":{"object":"token_usage","total_available":5,"unlimited_quota":false,"expires_at":0}}`)
	out = f.collect()
	require.Equal(t, "auth_failed", out.reason)
	f.save(out)
	require.Equal(t, 1, f.state.AuthStreak)
	require.Equal(t, 5*time.Minute, f.state.NextDueAt.Sub(f.now))
}

func TestUpstreamBalanceIdentificationStatusClassification(t *testing.T) {
	for _, known := range []bool{false, true} {
		for _, status := range []int{400, 401, 403, 404, 405, 410, 422, 429, 500} {
			name := http.StatusText(status) + "/identify"
			if known {
				name = http.StatusText(status) + "/known-newapi"
			}
			t.Run(name, func(t *testing.T) {
				a := upstreamBalanceCandidate(860, StatusActive)
				store := &upstreamBalanceMemoryStore{}
				if known {
					store.entries = balanceEntriesFor([]Account{a}, "newapi")
				}
				upstream := balanceCallbackHTTP(func(r *http.Request) (*http.Response, error) {
					code := 404
					if r.URL.Path == newAPIStatusPath {
						code = status
					}
					return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
				})
				svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
				roundStart := time.Now().UTC().Truncate(time.Second).Add(999800 * time.Microsecond)
				svc.now = func() time.Time { return roundStart }
				require.NoError(t, svc.RunRound(context.Background()))
				st := store.savedStates[a.ID]
				require.NotNil(t, st)
				switch {
				case status == 429:
					require.Equal(t, "rate_limited", st.LastReason)
				case !known && status >= 400 && status < 500 && status != 401 && status != 403:
					require.Equal(t, "unidentified", st.LastReason)
					require.Equal(t, 1, st.UnsupportedStreak)
					require.Equal(t, "unsupported", store.entries.Kinds[a.ID].Kind)
					require.Equal(t, roundStart.Add(30*time.Minute), st.NextDueAt)
				default:
					require.Equal(t, "upstream_error", st.LastReason)
					require.Equal(t, 1, st.TransientStreak)
				}
				if known {
					if status == 404 || status == 405 {
						require.NotContains(t, store.entries.Kinds, a.ID)
					} else {
						require.Equal(t, "newapi", store.entries.Kinds[a.ID].Kind)
					}
				}
			})
		}
	}
}

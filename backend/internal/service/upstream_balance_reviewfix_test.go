package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceURLPolicyRejectsBeforeRequest(t *testing.T) {
	for _, test := range []string{"http-disabled", "host-not-allowed"} {
		t.Run(test, func(t *testing.T) {
			var calls atomic.Int64
			upstream := balanceCallbackHTTP(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("unexpected request")
			})
			cfg := upstreamBalanceTestConfig()
			a := upstreamBalanceCandidate(801, StatusActive)
			a.Credentials["base_url"] = "http://relay.example/v1"
			if test == "http-disabled" {
				cfg.Security.URLAllowlist.AllowInsecureHTTP = false
			} else {
				a.Credentials["base_url"] = "https://relay.example/v1"
				cfg.Security.URLAllowlist.Enabled = true
				cfg.Security.URLAllowlist.UpstreamHosts = []string{"allowed.example"}
			}
			store := &upstreamBalanceMemoryStore{}
			svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: cfg, httpUpstream: upstream}, store, nil)
			require.NoError(t, svc.RunRound(context.Background()))
			require.Zero(t, calls.Load())
			require.Equal(t, "upstream_error", store.savedStates[a.ID].LastReason)
			require.Equal(t, 1, store.savedStates[a.ID].TransientStreak)
		})
	}
}

func TestUpstreamBalanceBackoffStreaksResetAndCap(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		reason     string
		first, cap time.Duration
	}{
		{"upstream_error", time.Minute, 10 * time.Minute}, {"auth_failed", 5 * time.Minute, time.Hour}, {"unidentified", 30 * time.Minute, 24 * time.Hour},
	} {
		t.Run(test.reason, func(t *testing.T) {
			st := &UpstreamBalanceState{}
			a := upstreamBalanceCandidate(803, StatusActive)
			st.Fingerprint = upstreamBalanceFingerprint(&a, upstreamBalanceEffectiveBaseURL(&a))
			store := &upstreamBalanceMemoryStore{}
			upstream := balanceCallbackHTTP(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
			})
			svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
			svc.now = func() time.Time { return now }
			fail := func() {
				if test.reason == "unidentified" {
					// Simulate expiry of the negative kind cache, then re-identify.
					require.NoError(t, store.DeleteKind(context.Background(), a.ID))
					svc.pollOne(context.Background(), now, a.ID, &a, st, &upstreamBalanceRoundMemo{})
				} else {
					svc.persistOutcome(context.Background(), now, a.ID, st, ubOutcome{reason: test.reason})
				}
			}
			for i := 0; i < 100; i++ {
				fail()
				delay := st.NextDueAt.Sub(now)
				require.Positive(t, delay)
				if i >= 7 {
					require.Equal(t, test.cap, delay)
				}
			}
			require.LessOrEqual(t, st.TransientStreak, 16)
			require.LessOrEqual(t, st.AuthStreak, 16)
			require.LessOrEqual(t, st.UnsupportedStreak, 16)
			svc.persistOutcome(context.Background(), now, 1, st, ubOutcome{success: true})
			require.Zero(t, st.TransientStreak)
			require.Zero(t, st.AuthStreak)
			require.Zero(t, st.UnsupportedStreak)
			fail()
			require.Equal(t, test.first, st.NextDueAt.Sub(now))
		})
	}
	for _, path := range []string{newAPISubPath, newAPIUsagePath} {
		for _, body := range []string{`{"success":false}`, `{"code":false}`} {
			f := newAPIFixtureFor(t)
			f.reply(path, 200, body)
			for i := 1; i <= 4; i++ {
				out := f.collect()
				require.Equal(t, "auth_failed", out.reason)
				f.save(out)
				require.Equal(t, i, f.state.AuthStreak)
				require.Equal(t, 5*time.Minute<<(i-1), f.state.NextDueAt.Sub(f.now))
			}
		}
	}
}

func TestUpstreamBalanceKnownSub2APIClassification(t *testing.T) {
	for _, status := range []int{200, 400, 401, 403, 404, 405} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var statusCalls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == newAPIStatusPath {
					statusCalls.Add(1)
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"denied"}`)
			}))
			defer server.Close()
			a := upstreamBalanceCandidate(802, StatusActive)
			a.Credentials["base_url"] = server.URL
			store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "sub2api")}
			svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(previous)
			require.NoError(t, svc.RunRound(context.Background()))
			require.Zero(t, statusCalls.Load())
			st := store.savedStates[a.ID]
			require.NotNil(t, st)
			if status == 401 || status == 403 {
				require.Equal(t, "auth_failed", st.LastReason)
				require.Equal(t, 1, st.AuthStreak)
				require.Equal(t, "sub2api", store.entries.Kinds[a.ID].Kind)
			} else {
				require.Equal(t, "upstream_error", st.LastReason)
				require.Equal(t, 1, st.TransientStreak)
				require.NotContains(t, store.entries.Kinds, a.ID)
			}
			require.Zero(t, st.UnsupportedStreak)
			require.Contains(t, logs.String(), "upstream_kind=sub2api")
		})
	}
}

func TestUpstreamBalanceIdentificationUsesSiteSingleflight(t *testing.T) {
	f := newAPIFixtureFor(t)
	delegate := f.svc.accountTestService.httpUpstream
	f.svc.accountTestService.httpUpstream = balanceCallbackHTTP(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/usage" {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
		}
		return delegate.DoWithTLS(r, "", 0, 0, nil)
	})
	accounts := make([]Account, 7)
	for i := range accounts {
		accounts[i] = upstreamBalanceCandidate(int64(810+i), StatusActive)
		accounts[i].Credentials["base_url"] = f.server.URL
	}
	f.svc.accountRepo = &upstreamBalanceRoundRepo{accounts: accounts}
	require.NoError(t, f.svc.RunRound(context.Background()))
	require.Equal(t, 1, f.count(newAPIStatusPath))
	require.Len(t, f.store.savedRecords, len(accounts))
}

type balanceCallbackHTTP func(*http.Request) (*http.Response, error)

func (f balanceCallbackHTTP) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return f(r)
}
func (f balanceCallbackHTTP) DoWithTLS(r *http.Request, p string, id int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(r, p, id, c)
}

func TestUpstreamBalanceStatusPanicRecoveredAndStopWaits(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		a := upstreamBalanceCandidate(820, StatusActive)
		a.Credentials["base_url"] = "https://newapi.example"
		b := upstreamBalanceCandidate(821, StatusActive)
		b.Credentials["base_url"] = "https://sub2api.example"
		store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "newapi")}
		upstream := balanceCallbackHTTP(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == newAPIStatusPath {
				panic("fixture panic")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(`{"mode":"unrestricted","isValid":true,"balance":1}`))}, nil
		})
		var logs bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		defer slog.SetDefault(previous)
		svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a, b}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
		require.NoError(t, svc.RunRound(context.Background()))
		require.Contains(t, store.savedRecords, b.ID)
		require.Contains(t, logs.String(), "upstream_balance.round_failed stage=panic")
	})
	t.Run("stop-waits", func(t *testing.T) {
		entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		upstream := balanceCallbackHTTP(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			close(cancelled)
			<-release
			return nil, r.Context().Err()
		})
		a := upstreamBalanceCandidate(822, StatusActive)
		store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "newapi")}
		svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
		svc.Start()
		<-entered
		done := make(chan struct{})
		go func() { svc.Stop(); close(done) }()
		<-cancelled
		select {
		case <-done:
			t.Fatal("Stop returned while status callback was running")
		case <-time.After(30 * time.Millisecond):
		}
		close(release)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Stop did not return")
		}
		require.Empty(t, store.savedStates)
	})
}

func TestUpstreamBalanceAccountBudgetExpiryPersistsTransient(t *testing.T) {
	a := upstreamBalanceCandidate(830, StatusActive)
	store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "sub2api")}
	upstream := balanceCallbackHTTP(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
	svc.accountBudget = 20 * time.Millisecond
	require.NoError(t, svc.RunRound(context.Background()))
	require.NotNil(t, store.savedStates[a.ID])
	require.Equal(t, "upstream_error", store.savedStates[a.ID].LastReason)
	require.Equal(t, 1, store.savedStates[a.ID].TransientStreak)
	require.True(t, store.savedStates[a.ID].RetryNotBefore.After(time.Now()))
}

type balanceErrorLeaderLock struct{ LeaderLockCache }

func (balanceErrorLeaderLock) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	return false, errors.New("fixture redis failure")
}
func TestUpstreamBalanceLeaderLockErrorLogsPaused(t *testing.T) {
	var calls atomic.Int64
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	upstream := balanceCallbackHTTP(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, nil })
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{upstreamBalanceCandidate(840, StatusActive)}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, &upstreamBalanceMemoryStore{}, balanceErrorLeaderLock{})
	require.NoError(t, svc.RunRound(context.Background()))
	require.NoError(t, svc.RunRound(context.Background()))
	require.Zero(t, calls.Load())
	require.Equal(t, 1, bytes.Count(logs.Bytes(), []byte("upstream_balance.paused")))
	require.Contains(t, logs.String(), "cause=redis_unavailable")
}

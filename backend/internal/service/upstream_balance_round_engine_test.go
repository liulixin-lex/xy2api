package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func balanceEntriesFor(accounts []Account, kind string) *UpstreamBalanceEntries {
	e := emptyUpstreamBalanceEntries()
	for i := range accounts {
		fp := upstreamBalanceFingerprint(&accounts[i], upstreamBalanceEffectiveBaseURL(&accounts[i]))
		e.Kinds[accounts[i].ID] = &UpstreamBalanceKindRecord{Version: 1, Fingerprint: fp, Kind: kind}
	}
	return e
}

func TestUpstreamBalanceRoundBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	a := upstreamBalanceCandidate(151, StatusActive)
	a.Credentials["base_url"] = server.URL
	repo := &upstreamBalanceRoundRepo{accounts: []Account{a}}
	store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "sub2api")}
	up := newUpstreamBalanceTestHTTP(server)
	svc := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, store, nil)
	svc.roundBudget = 40 * time.Millisecond
	started := time.Now()
	require.NoError(t, svc.RunRound(context.Background()))
	require.WithinDuration(t, started.Add(svc.roundBudget), repo.listedDeadline, 10*time.Millisecond)
	require.Less(t, time.Since(started), 250*time.Millisecond)
	require.Nil(t, up.req)
}

func TestUpstreamBalanceAccountBudget(t *testing.T) {
	now := time.Now()
	require.False(t, upstreamBalanceCanStartAccount(now.Add(25*time.Second-time.Nanosecond), now))
	require.True(t, upstreamBalanceCanStartAccount(now.Add(25*time.Second), now))
}

func TestUpstreamBalanceCadenceAnchoredToRoundStartAfterLeaderLock(t *testing.T) {
	base := time.Now().UTC()
	clock := base
	a := upstreamBalanceCandidate(152, StatusActive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"balance":3}`)
	}))
	defer server.Close()
	a.Credentials["base_url"] = server.URL
	lock := &upstreamBalanceLeaderLock{onAcquire: func() { clock = base.Add(7 * time.Second) }}
	store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "sub2api")}
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, lock)
	svc.now = func() time.Time { return clock }
	require.NoError(t, svc.RunRound(context.Background()))
	require.Equal(t, clock.Add(time.Minute), store.savedStates[a.ID].NextDueAt)
}

func TestUpstreamBalanceConcurrencyLimit(t *testing.T) {
	var active, maximum, requests atomic.Int64
	release := make(chan struct{})
	firstFour := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		requests.Add(1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		if current == upstreamBalanceConcurrency {
			once.Do(func() { close(firstFour) })
		}
		<-release
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"balance":1}`)
	}))
	defer server.Close()
	accounts := make([]Account, 6)
	for i := range accounts {
		accounts[i] = upstreamBalanceCandidate(int64(160+i), StatusActive)
		accounts[i].Credentials["base_url"] = server.URL
	}
	store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor(accounts, "sub2api")}
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: accounts}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	done := make(chan error, 1)
	go func() { done <- svc.RunRound(context.Background()) }()
	select {
	case <-firstFour:
	case <-time.After(3 * time.Second):
		t.Fatal("four workers did not reach the server")
	}
	close(release)
	require.NoError(t, <-done)
	require.EqualValues(t, upstreamBalanceConcurrency, maximum.Load())
	require.EqualValues(t, len(accounts), requests.Load())
}

func TestUpstreamBalancePanicRecovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"balance":1}`)
	}))
	defer server.Close()
	accounts := make([]Account, 2)
	for i := range accounts {
		accounts[i] = upstreamBalanceCandidate(int64(170+i), StatusActive)
		accounts[i].Credentials["base_url"] = server.URL
	}
	store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor(accounts, "sub2api")}
	delegate := newUpstreamBalanceTestHTTP(server)
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: accounts}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: panicUpstreamBalanceHTTP{delegate: delegate, panicID: accounts[0].ID}}, store, nil)
	require.NoError(t, svc.RunRound(context.Background()))
	require.Contains(t, store.savedRecords, accounts[1].ID)
}

func TestUpstreamBalanceLogging(t *testing.T) {
	secret := "never-log-this-query-secret"
	a := upstreamBalanceCandidate(180, StatusActive)
	a.Credentials["base_url"] = "https://relay.example/v1?key=" + secret
	upstream := balanceCallbackHTTP(func(*http.Request) (*http.Response, error) { return nil, errors.New(secret) })
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "sub2api")}, nil)
	require.NoError(t, svc.RunRound(context.Background()))
	require.NotContains(t, output.String(), secret)
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var events []string
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		name, ok := entry["msg"].(string)
		require.True(t, ok)
		events = append(events, name)
		require.Equal(t, "DEBUG", entry["level"])
		delete(entry, "time")
		delete(entry, "level")
		delete(entry, "msg")
		switch name {
		case "upstream_balance.round_done":
			require.ElementsMatch(t, []string{"due", "polled", "skipped", "duration_ms"}, mapKeys(entry))
			require.Equal(t, float64(1), entry["due"])
			require.Equal(t, float64(1), entry["polled"])
		case "upstream_balance.account_result":
			require.ElementsMatch(t, []string{"account_id", "upstream_kind", "reason", "http_status", "duration_ms"}, mapKeys(entry))
			require.Equal(t, "upstream_error", entry["reason"])
		default:
			t.Fatalf("unexpected log event %s", name)
		}
	}
	require.ElementsMatch(t, []string{"upstream_balance.round_done", "upstream_balance.account_result"}, events)
	require.NotContains(t, output.String(), secret)
}
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestUpstreamBalanceCancellationKeepsDue(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(190, StatusActive)
	a.Credentials["base_url"] = server.URL
	store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor([]Account{a}, "sub2api")}
	memo := &upstreamBalanceRoundMemo{budget: map[string]int{}, reserved: map[int64]int{}, origins: map[int64]string{}, cooldowns: map[string]time.Time{}}
	origin := mustOrigin(server.URL)
	require.True(t, memo.reserveAccount(a.ID, origin, 1))
	ctx, cancel := context.WithCancel(context.Background())
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: []Account{a}}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	done := make(chan struct{})
	go func() { svc.pollOne(ctx, time.Now(), a.ID, &a, nil, memo); close(done) }()
	<-entered
	cancel()
	<-done
	require.NotContains(t, store.savedStates, a.ID)
	require.NotContains(t, store.savedRecords, a.ID)
}

func TestUpstreamBalanceOriginBudgetDefersExcessAccounts(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"balance":1}`)
	}))
	defer server.Close()
	accounts := make([]Account, upstreamBalanceOriginBudgetPerRound+1)
	for i := range accounts {
		accounts[i] = upstreamBalanceCandidate(int64(200+i), StatusActive)
		accounts[i].Credentials["base_url"] = server.URL
	}
	store := &upstreamBalanceMemoryStore{entries: balanceEntriesFor(accounts, "sub2api")}
	svc := NewUpstreamBalanceService(&upstreamBalanceRoundRepo{accounts: accounts}, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	require.NoError(t, svc.RunRound(context.Background()))
	require.EqualValues(t, upstreamBalanceOriginBudgetPerRound, requests.Load())
	deferredID := accounts[len(accounts)-1].ID
	require.NotContains(t, store.savedStates, deferredID)
	require.NoError(t, svc.RunRound(context.Background()))
	require.EqualValues(t, upstreamBalanceOriginBudgetPerRound+1, requests.Load())
	require.Contains(t, store.savedStates, deferredID)
}

func TestUpstreamBalanceStatusBudget(t *testing.T) {
	var paths atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Add(1)
		if r.URL.Path == "/v1/usage" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(250, StatusActive)
	a.Credentials["base_url"] = server.URL
	origin := mustOrigin(server.URL)
	memo := &upstreamBalanceRoundMemo{budget: map[string]int{origin: upstreamBalanceOriginBudgetPerRound}, reserved: map[int64]int{a.ID: 1}, origins: map[int64]string{a.ID: origin}, cooldowns: map[string]time.Time{}}
	store := &upstreamBalanceMemoryStore{}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	svc.pollOne(context.Background(), time.Now(), a.ID, &a, nil, memo)
	require.EqualValues(t, 1, paths.Load())
	require.NotContains(t, store.savedStates, a.ID)
}

func upstreamBalanceBudgetMemo(id int64, origin string, initial int) *upstreamBalanceRoundMemo {
	return &upstreamBalanceRoundMemo{budget: map[string]int{origin: initial}, reserved: map[int64]int{id: initial}, origins: map[int64]string{id: origin}, cooldowns: map[string]time.Time{}}
}

func TestUpstreamBalanceOriginLockReservesBeforeCT(t *testing.T) {
	var tokenCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/dashboard/billing/subscription":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		case "/api/usage/token/":
			tokenCalls.Add(1)
			_, _ = io.WriteString(w, `{"code":true,"data":{"object":"token_usage","total_available":0,"unlimited_quota":false,"expires_at":0}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(260, StatusActive)
	a.Credentials["base_url"] = server.URL
	origin := mustOrigin(server.URL)
	memo := upstreamBalanceBudgetMemo(a.ID, origin, 2)
	store := &upstreamBalanceErrorStore{site: &UpstreamBalanceSite{QuotaPerUnit: 1, DisplayType: "USD"}, lockAcquired: true}
	store.lockBudget = func() { require.Equal(t, 3, memo.budget[origin]) }
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	out := svc.collectNewAPI(context.Background(), &a, server.URL, &UpstreamBalanceState{}, memo)
	require.Equal(t, "key_exhausted", out.reason)
	require.EqualValues(t, 1, tokenCalls.Load())
	require.Equal(t, 75*time.Second, store.lockTTL)
}

func TestUpstreamBalanceOriginLockFailureReleasesBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/dashboard/billing/subscription" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		t.Errorf("unexpected request after failed origin lock: %s", r.URL.Path)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(261, StatusActive)
	a.Credentials["base_url"] = server.URL
	origin := mustOrigin(server.URL)
	memo := upstreamBalanceBudgetMemo(a.ID, origin, 2)
	store := &upstreamBalanceErrorStore{site: &UpstreamBalanceSite{QuotaPerUnit: 1, DisplayType: "USD"}, lockAcquired: false}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	out := svc.collectNewAPI(context.Background(), &a, server.URL, &UpstreamBalanceState{}, memo)
	require.Equal(t, "auth_failed", out.reason)
	require.Equal(t, 2, memo.budget[origin])
	require.Equal(t, 75*time.Second, store.lockTTL)
}

var _ HTTPUpstream = panicUpstreamBalanceHTTP{}
var _ = tlsfingerprint.Profile{}

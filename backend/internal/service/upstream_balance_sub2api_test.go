package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceSub2APIWallet(t *testing.T) {
	o, err := parseSub2APIUsage([]byte(`{"mode":"unrestricted","isValid":true,"planName":"钱包余额","remaining":4.5,"unit":"USD","balance":4.5}`))
	require.NoError(t, err)
	require.True(t, o.success)
	require.Equal(t, "wallet", o.basis)
	require.Equal(t, 4.5, *o.amount)
}

func TestUpstreamBalanceSub2APIKeyQuotaAndRateLimitsTakesMin(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want float64
	}{
		{name: "quota and every rate window", body: `{"mode":"quota_limited","isValid":true,"status":"active","quota":{"limit":100,"used":40,"remaining":60,"unit":"USD"},"rate_limits":[{"window":"5h","remaining":31},{"window":"1d","remaining":22},{"window":"7d","remaining":28}]}`, want: 22},
		{name: "rate windows without total quota", body: `{"mode":"quota_limited","isValid":true,"status":"active","rate_limits":[{"window":"5h","remaining":8},{"window":"1d","remaining":6}]}`, want: 6},
		{name: "negative remaining preserved", body: `{"mode":"quota_limited","isValid":true,"status":"active","quota":{"remaining":-3.2,"unit":"USD"}}`, want: -3.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseSub2APIUsage([]byte(tc.body))
			require.NoError(t, err)
			require.True(t, o.success)
			require.Equal(t, "key", o.basis)
			require.Equal(t, tc.want, *o.amount)
		})
	}
}

func TestUpstreamBalanceSub2APISubscriptionUnlimitedLimitedAndInactive(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		success   bool
		unlimited bool
		reason    string
		amount    *float64
	}{
		{name: "unlimited", body: `{"mode":"unrestricted","isValid":true,"remaining":-1,"unit":"USD","subscription":{"daily_limit_usd":0,"weekly_limit_usd":0,"monthly_limit_usd":0}}`, success: true, unlimited: true},
		{name: "limited", body: `{"mode":"unrestricted","isValid":true,"remaining":3.25,"unit":"USD","subscription":{"daily_limit_usd":10,"weekly_limit_usd":0,"monthly_limit_usd":0}}`, success: true, amount: sub2APIPtrFloat64(3.25)},
		{name: "inactive", body: `{"mode":"unrestricted","isValid":true,"unit":"USD"}`, reason: "no_data"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseSub2APIUsage([]byte(tc.body))
			require.NoError(t, err)
			require.NotNil(t, o)
			require.Equal(t, tc.success, o.success)
			require.Equal(t, tc.unlimited, o.unlimited)
			require.Equal(t, tc.reason, o.reason)
			if tc.amount == nil {
				require.Nil(t, o.amount)
			} else {
				require.Equal(t, *tc.amount, *o.amount)
			}
		})
	}
}

func TestUpstreamBalanceSub2APINoNumbersIsNoDataNotUnsupported(t *testing.T) {
	o, err := parseSub2APIUsage([]byte(`{"mode":"quota_limited","isValid":true,"status":"active","quota":{},"rate_limits":[]}`))
	require.NoError(t, err)
	require.Equal(t, "no_data", o.reason)
	require.NotEqual(t, "unsupported", o.kind)

	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			statusCalls.Add(1)
		}
		_, _ = io.WriteString(w, `{"mode":"quota_limited","isValid":true,"status":"active","quota":{},"rate_limits":[]}`)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(870, StatusActive)
	a.Credentials["base_url"] = server.URL
	base, _ := upstreamBalanceApplicable(&a)
	store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	svc.pollOne(context.Background(), time.Now(), a.ID, &a, &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}, &upstreamBalanceRoundMemo{})
	require.Zero(t, statusCalls.Load())
	require.Equal(t, "sub2api", store.entries.Kinds[a.ID].Kind)
	require.Equal(t, "no_data", store.savedStates[a.ID].LastReason)
}

func TestUpstreamBalanceSub2APIIsValidFalseIsAuthFailed(t *testing.T) {
	o, err := parseSub2APIUsage([]byte(`{"mode":"unrestricted","isValid":false,"unit":"USD"}`))
	require.NoError(t, err)
	require.Equal(t, "auth_failed", o.reason)
}

func TestUpstreamBalanceSub2APIExhaustedOrExpiredIsZeroWithReason(t *testing.T) {
	for _, status := range []string{"quota_exhausted", "expired"} {
		o, err := parseSub2APIUsage([]byte(`{"mode":"quota_limited","isValid":true,"status":"` + status + `","quota":{"remaining":0,"unit":"USD"}}`))
		require.NoError(t, err)
		require.True(t, o.success)
		require.Equal(t, "key_exhausted", o.reason)
		require.Equal(t, 0.0, *o.amount)
	}
}

func TestUpstreamBalanceSub2APIUngroupedKey(t *testing.T) {
	o := collectSub2APIResponse(t, http.StatusForbidden, ` {"error":{"type":"permission_error","message":"This API key is not assigned to any group"}}`, nil)
	require.Equal(t, "key_ungrouped", o.reason)
	require.Equal(t, "sub2api", o.kind)
}

func TestUpstreamBalanceJSONOnlyAuth(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
	}{
		{name: "json unauthorized", body: `{"error":{"type":"authentication_error"}}`, reason: "auth_failed"},
		{name: "html unauthorized", body: `<html>blocked</html>`, reason: "upstream_error"},
		{name: "json forbidden", body: `{"error":{"type":"permission_error","message":"denied"}}`, reason: "auth_failed"},
		{name: "html forbidden", body: `<html>blocked</html>`, reason: "upstream_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := http.StatusUnauthorized
			if strings.Contains(tc.name, "forbidden") {
				status = http.StatusForbidden
			}
			o := collectSub2APIResponse(t, status, tc.body, nil)
			require.Equal(t, tc.reason, o.reason)
		})
	}
}

func TestUpstreamBalance429RetryAfterAndDefault(t *testing.T) {
	o := collectSub2APIResponse(t, http.StatusTooManyRequests, `{}`, http.Header{"Retry-After": {"120"}})
	require.Equal(t, "rate_limited", o.reason)
	require.Equal(t, 120*time.Second, o.retryAfter)
	o = collectSub2APIResponse(t, http.StatusTooManyRequests, `{}`, nil)
	require.Equal(t, "rate_limited", o.reason)
	require.Equal(t, 0*time.Second, o.retryAfter)
	require.Equal(t, 5*time.Minute, delayForOutcome(o, &UpstreamBalanceState{}))
}

func TestUpstreamBalanceSub2API429SetsPerAccountRetryDeadline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		delay  time.Duration
	}{
		{name: "retry after", header: "120", delay: 120 * time.Second},
		{name: "default", delay: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			a := upstreamBalanceCandidate(881, StatusActive)
			a.Credentials["base_url"] = server.URL
			base, _ := upstreamBalanceApplicable(&a)
			store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
			svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
			started := time.Now()
			svc.pollOne(context.Background(), started, a.ID, &a, &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}, &upstreamBalanceRoundMemo{})
			require.EqualValues(t, 1, calls.Load())
			require.Nil(t, store.entries.Kinds[a.ID])
			require.Equal(t, "rate_limited", store.savedStates[a.ID].LastReason)
			require.NotNil(t, store.savedStates[a.ID].RetryNotBefore)
			require.WithinDuration(t, started.Add(tc.delay), *store.savedStates[a.ID].RetryNotBefore, 3*time.Second)
		})
	}
}

func TestUpstreamBalanceSub2APISlowModeAndRecovery(t *testing.T) {
	var slow atomic.Bool
	slow.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if slow.Load() {
			time.Sleep(3100 * time.Millisecond)
		}
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"balance":1,"unit":"USD"}`)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(871, StatusActive)
	a.Credentials["base_url"] = server.URL
	base, ok := upstreamBalanceApplicable(&a)
	require.True(t, ok)
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, &upstreamBalanceMemoryStore{}, nil)
	state := &UpstreamBalanceState{}
	for range 1 {
		o := svc.collectSub2API(context.Background(), &a, base, state, nil)
		require.True(t, o.success)
	}
	require.True(t, state.Slow)
	require.Equal(t, 5*time.Minute, delayForOutcome(ubOutcome{}, state))
	slow.Store(false)
	for range 3 {
		o := svc.collectSub2API(context.Background(), &a, base, state, nil)
		require.True(t, o.success)
	}
	require.False(t, state.Slow)
	require.Zero(t, state.FastStreak)
}

func TestUpstreamBalanceTokenShortcut(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`)
		case "/v1/dashboard/billing/subscription":
			_, _ = io.WriteString(w, `{"hard_limit_usd":10}`)
		case "/v1/dashboard/billing/usage":
			_, _ = io.WriteString(w, `{"total_usage":0}`)
		case "/api/user/self":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota":300}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(872, StatusActive)
	a.Credentials["base_url"] = server.URL
	a.Credentials["newapi_access_token"] = "pat"
	base, _ := upstreamBalanceApplicable(&a)
	fp := upstreamBalanceFingerprint(&a, base)
	store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	state := &UpstreamBalanceState{Fingerprint: fp}
	svc.pollOne(context.Background(), time.Now(), a.ID, &a, state, &upstreamBalanceRoundMemo{})
	require.Equal(t, "newapi", store.entries.Kinds[a.ID].Kind)
	require.Equal(t, []string{"GET /api/status", "GET /v1/dashboard/billing/subscription", "GET /v1/dashboard/billing/usage", "GET /api/user/self"}, calls)
	require.Equal(t, 3.0, *store.savedRecords[a.ID].AmountUSD)
}

func TestUpstreamBalanceTokenShortcutRejected(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"success":false}`)
			return
		}
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"planName":"wallet","remaining":2.5,"unit":"USD","balance":2.5}`)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(873, StatusActive)
	a.Credentials["base_url"] = server.URL
	a.Credentials["newapi_access_token"] = "pat"
	base, _ := upstreamBalanceApplicable(&a)
	store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	state := &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}
	svc.pollOne(context.Background(), time.Now(), a.ID, &a, state, &upstreamBalanceRoundMemo{})
	require.True(t, store.savedStates[a.ID].TokenShortcutRejected)
	require.Equal(t, "sub2api", store.entries.Kinds[a.ID].Kind)
	require.Equal(t, []string{"/api/status", "/v1/usage"}, calls)
	require.Equal(t, 2.5, *store.savedRecords[a.ID].AmountUSD)
}

func TestUpstreamBalanceTokenShortcutFingerprintScope(t *testing.T) {
	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			statusCalls.Add(1)
			_, _ = io.WriteString(w, `{"success":false}`)
			return
		}
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"balance":1,"unit":"USD"}`)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(874, StatusActive)
	a.Credentials["base_url"] = server.URL
	a.Credentials["newapi_access_token"] = "pat"
	base, _ := upstreamBalanceApplicable(&a)
	currentFP := upstreamBalanceFingerprint(&a, base)
	store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	old := &UpstreamBalanceState{Fingerprint: "old-fingerprint", TokenShortcutRejected: true}
	svc.pollOne(context.Background(), time.Now(), a.ID, &a, old, &upstreamBalanceRoundMemo{})
	require.EqualValues(t, 1, statusCalls.Load())
	require.Equal(t, currentFP, store.savedStates[a.ID].Fingerprint)
	require.True(t, store.savedStates[a.ID].TokenShortcutRejected)
}

func TestUpstreamBalanceIdentificationOrder(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/v1/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(875, StatusActive)
	a.Credentials["base_url"] = server.URL
	base, _ := upstreamBalanceApplicable(&a)
	store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	svc.pollOne(context.Background(), time.Now(), a.ID, &a, &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}, &upstreamBalanceRoundMemo{})
	require.Equal(t, []string{"/v1/usage", "/api/status"}, calls)
	require.Equal(t, "unsupported", store.entries.Kinds[a.ID].Kind)

	t.Run("usage server error stops identification", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()
		a := upstreamBalanceCandidate(877, StatusActive)
		a.Credentials["base_url"] = server.URL
		base, _ := upstreamBalanceApplicable(&a)
		store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
		svc.pollOne(context.Background(), time.Now(), a.ID, &a, &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}, &upstreamBalanceRoundMemo{})
		require.EqualValues(t, 1, calls.Load())
		require.Nil(t, store.entries.Kinds[a.ID])
		require.Equal(t, "upstream_error", store.savedStates[a.ID].LastReason)
	})
	t.Run("JSON auth response continues to public status", func(t *testing.T) {
		var calls []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.URL.Path)
			if r.URL.Path == "/v1/usage" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"type":"authentication_error"}}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		a := upstreamBalanceCandidate(8771, StatusActive)
		a.Credentials["base_url"] = server.URL
		base, _ := upstreamBalanceApplicable(&a)
		store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
		svc.pollOne(context.Background(), time.Now(), a.ID, &a, &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}, &upstreamBalanceRoundMemo{})
		require.Equal(t, []string{"/v1/usage", "/api/status"}, calls)
		require.Nil(t, store.entries.Kinds[a.ID])
		require.Equal(t, "auth_failed", store.savedStates[a.ID].LastReason)
	})
}

func TestUpstreamBalancePublicStatusAuthIsTransient(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/v1/usage" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"no"}`)
			}))
			defer server.Close()
			a := upstreamBalanceCandidate(876+int64(status), StatusActive)
			a.Credentials["base_url"] = server.URL
			base, _ := upstreamBalanceApplicable(&a)
			store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
			svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
			svc.pollOne(context.Background(), time.Now(), a.ID, &a, &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}, &upstreamBalanceRoundMemo{})
			require.EqualValues(t, 2, calls.Load())
			require.Empty(t, store.entries.Kinds[a.ID])
			require.Equal(t, "upstream_error", store.savedStates[a.ID].LastReason)
			require.Equal(t, status, store.savedStates[a.ID].LastHTTPStatus)
		})
	}
}

func TestUpstreamBalanceUnsupportedKindTTLAndDue(t *testing.T) {
	var savedTTL time.Duration
	store := &upstreamBalanceTTLStore{upstreamBalanceMemoryStore: &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}, ttl: &savedTTL}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	a := upstreamBalanceCandidate(878, StatusActive)
	a.Credentials["base_url"] = server.URL
	base, _ := upstreamBalanceApplicable(&a)
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	state := &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}
	svc.pollOne(context.Background(), time.Now(), a.ID, &a, state, &upstreamBalanceRoundMemo{})
	require.Equal(t, 30*time.Minute, savedTTL)
	require.WithinDuration(t, store.savedStates[a.ID].LastAttemptAt.Add(30*time.Minute), store.savedStates[a.ID].NextDueAt, time.Second)
	require.Equal(t, 30*time.Minute, unsupportedTTL(1))
	require.Equal(t, time.Hour, unsupportedTTL(2))
	require.Equal(t, 24*time.Hour, unsupportedTTL(10))
}

func TestUpstreamBalanceUnsupportedSkipWhileAlive(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer server.Close()
	a := upstreamBalanceCandidate(879, StatusActive)
	a.Credentials["base_url"] = server.URL
	base, _ := upstreamBalanceApplicable(&a)
	fp := upstreamBalanceFingerprint(&a, base)
	entries := emptyUpstreamBalanceEntries()
	entries.Kinds[a.ID] = &UpstreamBalanceKindRecord{Version: 1, Fingerprint: fp, Kind: "unsupported"}
	entries.States[a.ID] = &UpstreamBalanceState{Version: 1, Fingerprint: fp, NextDueAt: time.Now().Add(-time.Minute)}
	store := &upstreamBalanceMemoryStore{entries: entries}
	repo := &upstreamBalanceRoundRepo{accounts: []Account{a}}
	svc := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, store, nil)
	require.NoError(t, svc.RunRound(context.Background()))
	require.Zero(t, calls.Load())
	require.Empty(t, store.savedStates)
}

func collectSub2APIResponse(t *testing.T, status int, body string, headers http.Header) ubOutcome {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, values := range headers {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	a := upstreamBalanceCandidate(880, StatusActive)
	a.Credentials["base_url"] = server.URL
	base, _ := upstreamBalanceApplicable(&a)
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: newUpstreamBalanceTestHTTP(server)}, &upstreamBalanceMemoryStore{}, nil)
	return svc.collectSub2API(context.Background(), &a, base, &UpstreamBalanceState{}, nil)
}

type upstreamBalanceTTLStore struct {
	*upstreamBalanceMemoryStore
	ttl *time.Duration
}

func (s *upstreamBalanceTTLStore) SaveKind(ctx context.Context, id int64, k *UpstreamBalanceKindRecord, ttl time.Duration) error {
	*s.ttl = ttl
	return s.upstreamBalanceMemoryStore.SaveKind(ctx, id, k, ttl)
}

func sub2APIPtrFloat64(v float64) *float64 { return &v }

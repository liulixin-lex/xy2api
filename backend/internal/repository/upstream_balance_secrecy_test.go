package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/config"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type upstreamBalanceSecrecyRepo struct{ *newAPICooldownAccounts }

func (r *upstreamBalanceSecrecyRepo) GetByIDs(context.Context, []int64) ([]*service.Account, error) {
	return []*service.Account{&r.account}, nil
}

func TestUpstreamBalanceStoreNeverPersistsCredentialSecrets(t *testing.T) {
	const querySecret = "query-secret-marker"
	const tokenSecret = "access-token-marker"
	const keySecret = "api-key-secret-marker"
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	store := NewUpstreamBalanceStore(client)
	var failed atomic.Bool
	var calls atomic.Int64
	var querySeen, tokenSeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.URL.Query().Get("credential") == querySecret {
			querySeen.Store(true)
		}
		if req.Header.Get("Authorization") == "Bearer "+tokenSecret {
			tokenSeen.Store(true)
		}
		if failed.Load() && req.URL.Path != "/api/status" {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, querySecret+" "+tokenSecret+" "+keySecret)
			return
		}
		switch req.URL.Path {
		case "/api/status":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`)
		case "/v1/dashboard/billing/subscription":
			_, _ = io.WriteString(w, `{"hard_limit_usd":10}`)
		case "/v1/dashboard/billing/usage":
			_, _ = io.WriteString(w, `{"total_usage":200}`)
		case "/api/user/self":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota":500}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	repo := &upstreamBalanceSecrecyRepo{&newAPICooldownAccounts{account: service.Account{ID: 31, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Credentials: map[string]any{"base_url": server.URL + "/v1?credential=" + querySecret, "api_key": keySecret, "newapi_access_token": tokenSecret}}}}
	ats := service.NewAccountTestService(repo, nil, nil, nil, nil, newAPICooldownHTTP{client: server.Client()}, &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}, nil)
	svc := service.NewUpstreamBalanceService(repo, ats, store, nil)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	require.NoError(t, svc.RunRound(context.Background()))
	entries, err := store.Load(context.Background(), []int64{31})
	require.NoError(t, err)
	require.NotNil(t, entries.Records[31])
	require.True(t, tokenSeen.Load())
	entries.Records[31].ObservedAt = entries.Records[31].ObservedAt.Add(-2 * time.Second)
	require.NoError(t, store.SaveSuccess(context.Background(), 31, entries.Records[31], entries.States[31]))
	failed.Store(true)
	require.NoError(t, client.Del(context.Background(), ubStateKey(31)).Err())
	require.NoError(t, svc.RunRound(context.Background()))
	entries, err = store.Load(context.Background(), []int64{31})
	require.NoError(t, err)
	require.Equal(t, "upstream_error", entries.States[31].LastReason)
	require.True(t, querySeen.Load())
	beforeRead := calls.Load()
	items, err := svc.ReadBalances(context.Background(), []int64{31})
	require.NoError(t, err)
	require.Equal(t, beforeRead, calls.Load())
	payload, err := json.Marshal(items)
	require.NoError(t, err)
	require.Equal(t, "ok", items[0].State)
	require.NotNil(t, items[0].Reason)
	require.Equal(t, "upstream_error", *items[0].Reason)
	require.Contains(t, logs.String(), "upstream_balance.round_done")
	for _, secret := range []string{querySecret, tokenSecret, keySecret} {
		require.NotContains(t, logs.String(), secret)
		require.NotContains(t, string(payload), secret)
		for _, key := range mr.Keys() {
			if !strings.HasPrefix(key, "upstream_balance:") {
				continue
			}
			value, err := mr.Get(key)
			require.NoError(t, err)
			require.NotContains(t, value, secret)
		}
	}
}

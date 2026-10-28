package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/config"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type upstreamBalanceHandlerRepo struct {
	service.AccountRepository
	account service.Account
}

func (r *upstreamBalanceHandlerRepo) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	for _, id := range ids {
		if id == r.account.ID {
			return []*service.Account{&r.account}, nil
		}
	}
	return nil, nil
}

func (r *upstreamBalanceHandlerRepo) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

type upstreamBalanceHandlerHTTP struct{ client *http.Client }

func (u upstreamBalanceHandlerHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(req)
}
func (u upstreamBalanceHandlerHTTP) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestGetUpstreamBalancesRejectsInvalidIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AccountHandler{}
	r := gin.New()
	r.GET("/", h.GetUpstreamBalances)
	for _, tc := range []struct{ ids, reason string }{
		{"abc", "UPSTREAM_BALANCE_INVALID_IDS"},
		{"0", "UPSTREAM_BALANCE_INVALID_IDS"},
		{"-1", "UPSTREAM_BALANCE_INVALID_IDS"},
		{strings.Repeat("1,", 200) + "1", "UPSTREAM_BALANCE_TOO_MANY_IDS"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?ids="+tc.ids, nil))
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), tc.reason)
	}
}

func TestGetUpstreamBalancesReturnsContractShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := service.Account{ID: 21, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Credentials: map[string]any{"api_key": "ordinary-key", "base_url": "https://relay.example"}}
	repo := &upstreamBalanceHandlerRepo{account: account}
	store := &upstreamBalanceHandlerStore{entries: &service.UpstreamBalanceEntries{
		Records: map[int64]*service.UpstreamBalanceRecord{},
		States:  map[int64]*service.UpstreamBalanceState{},
		Kinds:   map[int64]*service.UpstreamBalanceKindRecord{},
	}}
	svc := service.NewUpstreamBalanceService(repo, nil, store, nil)
	h := &AccountHandler{}
	h.SetUpstreamBalanceService(svc)
	r := gin.New()
	r.GET("/api/v1/admin/accounts/upstream-balances", h.GetUpstreamBalances)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/upstream-balances?ids=21,21,999", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Items      []service.UpstreamBalanceItem `json:"items"`
			ServerTime string                        `json:"server_time"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.Equal(t, "success", body.Message)
	require.Len(t, body.Data.Items, 1)
	require.Equal(t, int64(21), body.Data.Items[0].AccountID)
	require.Equal(t, "unavailable", body.Data.Items[0].State)
	serverTime, err := time.Parse(time.RFC3339, body.Data.ServerTime)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(body.Data.ServerTime, "Z"))
	require.WithinDuration(t, time.Now().UTC(), serverTime, 2*time.Second)
}

func TestUpstreamBalanceSecretsNeverInStoreLogsOrResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	querySecret := "query-secret-marker"
	tokenSecret := "access-token-marker"
	account := service.Account{ID: 22, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Credentials: map[string]any{
		"api_key":             "ordinary-api-key",
		"base_url":            "https://relay.example/v1?credential=" + querySecret,
		"newapi_access_token": tokenSecret,
	}}
	repo := &upstreamBalanceHandlerRepo{account: account}
	store := &upstreamBalanceHandlerStore{entries: &service.UpstreamBalanceEntries{Records: map[int64]*service.UpstreamBalanceRecord{}, States: map[int64]*service.UpstreamBalanceState{}, Kinds: map[int64]*service.UpstreamBalanceKindRecord{}}}
	var calls atomic.Int64
	var querySeen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"USD"}}`)
			return
		}
		if req.URL.Query().Get("credential") == querySecret {
			querySeen.Store(true)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, querySecret+" "+tokenSecret)
	}))
	defer server.Close()
	account.Credentials["base_url"] = server.URL + "/v1?credential=" + querySecret
	repo.account = account
	upstream := upstreamBalanceHandlerHTTP{client: server.Client()}
	accountTest := service.NewAccountTestService(repo, nil, nil, nil, nil, upstream, &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}, nil)
	svc := service.NewUpstreamBalanceService(repo, accountTest, store, nil)
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	require.NoError(t, svc.RunRound(context.Background()))
	require.Positive(t, calls.Load())
	require.True(t, querySeen.Load(), "a failing upstream request must carry the secret query")
	require.Contains(t, store.values, "upstream_error")
	require.Contains(t, logs.String(), "upstream_balance.round_done")
	require.NotContains(t, logs.String(), "https://")
	callsBeforeRead := calls.Load()
	h := &AccountHandler{}
	h.SetUpstreamBalanceService(svc)
	r := gin.New()
	r.GET("/api/v1/admin/accounts/upstream-balances", h.GetUpstreamBalances)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/upstream-balances?ids=22", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "upstream_error")
	require.Equal(t, callsBeforeRead, calls.Load(), "the read handler must not issue upstream requests")
	for _, secret := range []string{querySecret, tokenSecret, "ordinary-api-key"} {
		require.NotContains(t, logs.String(), secret)
		require.NotContains(t, w.Body.String(), secret)
		require.NotContains(t, store.values, secret)
	}
}

func TestGetUpstreamBalancesRedisDownReturnsHTTP200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := service.Account{ID: 23, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key", "base_url": "https://relay.example"}}
	store := &upstreamBalanceHandlerStore{entries: &service.UpstreamBalanceEntries{}, err: errors.New("redis unavailable")}
	svc := service.NewUpstreamBalanceService(&upstreamBalanceHandlerRepo{account: account}, nil, store, nil)
	h := &AccountHandler{}
	h.SetUpstreamBalanceService(svc)
	r := gin.New()
	r.GET("/", h.GetUpstreamBalances)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?ids=23", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"state":"unavailable"`)
	require.Contains(t, w.Body.String(), `"reason":"redis_unavailable"`)
}

type upstreamBalanceHandlerStore struct {
	service.UpstreamBalanceStore
	entries *service.UpstreamBalanceEntries
	values  string
	err     error
}

func (s *upstreamBalanceHandlerStore) Load(context.Context, []int64) (*service.UpstreamBalanceEntries, error) {
	return s.entries, s.err
}

func (s *upstreamBalanceHandlerStore) GetCooldown(context.Context, string) (*time.Time, error) {
	return nil, nil
}
func (s *upstreamBalanceHandlerStore) SaveCooldown(context.Context, string, time.Time) error {
	return nil
}
func (s *upstreamBalanceHandlerStore) TryLockOrigin(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *upstreamBalanceHandlerStore) GetSite(context.Context, string) (*service.UpstreamBalanceSite, error) {
	return nil, nil
}
func (s *upstreamBalanceHandlerStore) SaveSite(context.Context, string, *service.UpstreamBalanceSite, time.Duration) error {
	return nil
}
func (s *upstreamBalanceHandlerStore) DeleteKind(context.Context, int64) error { return nil }

func (s *upstreamBalanceHandlerStore) SaveKind(_ context.Context, id int64, kind *service.UpstreamBalanceKindRecord, _ time.Duration) error {
	s.values += fmt.Sprintf("%+v", kind)
	if s.entries.Kinds == nil {
		s.entries.Kinds = make(map[int64]*service.UpstreamBalanceKindRecord)
	}
	s.entries.Kinds[id] = kind
	return nil
}
func (s *upstreamBalanceHandlerStore) SaveAttempt(_ context.Context, id int64, state *service.UpstreamBalanceState) error {
	s.values += fmt.Sprintf("%+v", state)
	if s.entries.States == nil {
		s.entries.States = make(map[int64]*service.UpstreamBalanceState)
	}
	s.entries.States[id] = state
	return nil
}
func (s *upstreamBalanceHandlerStore) SaveSuccess(_ context.Context, id int64, record *service.UpstreamBalanceRecord, state *service.UpstreamBalanceState) error {
	s.values += fmt.Sprintf("%+v%+v", record, state)
	if s.entries.Records == nil {
		s.entries.Records = make(map[int64]*service.UpstreamBalanceRecord)
	}
	if s.entries.States == nil {
		s.entries.States = make(map[int64]*service.UpstreamBalanceState)
	}
	s.entries.Records[id], s.entries.States[id] = record, state
	return nil
}

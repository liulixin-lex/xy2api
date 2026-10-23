//go:build unit

package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cacheRouteStore struct {
	service.SettingRepository
	rows map[string]string
}

func (r *cacheRouteStore) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.rows[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (r *cacheRouteStore) UpdateCachePolicy(_ context.Context, key string, f func(string) (string, error)) error {
	v, err := f(r.rows[key])
	if err == nil {
		r.rows[key] = v
	}
	return err
}

type cacheRouteAudit struct {
	service.AuditLogRepository
	mu   sync.Mutex
	rows []*service.AuditLog
}

func (r *cacheRouteAudit) BatchInsert(_ context.Context, rows []*service.AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rows...)
	return int64(len(rows)), nil
}
func (r *cacheRouteAudit) List(_ context.Context, f *service.AuditLogFilter) (*service.AuditLogList, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*service.AuditLog{}
	for _, row := range r.rows {
		if !strings.Contains(row.Action, f.Action) || row.Method != f.Method {
			continue
		}
		if f.CachePolicyBefore != nil && !row.CreatedAt.Before(*f.CachePolicyBefore) {
			continue
		}
		if f.Success != nil && *f.Success != (row.StatusCode < 400) {
			continue
		}
		rev, ok := row.Extra["rev_after"].(int64)
		ok = ok && row.StatusCode < 400
		if f.CachePolicyAfterRev != nil && (!ok || rev <= *f.CachePolicyAfterRev) {
			continue
		}
		if f.CachePolicyRevisionOnly && !ok {
			continue
		}
		out = append(out, row)
	}
	if f.CachePolicyCoverage {
		coverage := &service.CachePolicyRevisionCoverage{}
		unique := map[int64]bool{}
		for _, row := range out {
			rev, ok := row.Extra["rev_after"].(int64)
			if !ok || row.StatusCode >= 400 {
				continue
			}
			unique[rev] = true
		}
		for rev := range unique {
			if coverage.Count == 0 || rev < coverage.MinRev {
				coverage.MinRev = rev
			}
			if coverage.Count == 0 || rev > coverage.MaxRev {
				coverage.MaxRev = rev
			}
			coverage.Count++
		}
		return &service.AuditLogList{PolicyRevisions: coverage}, nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := len(out)
	if len(out) > f.PageSize {
		out = out[:f.PageSize]
	}
	return &service.AuditLogList{Logs: out, Total: total}, nil
}

type cacheContract struct {
	Fixture *struct {
		Key       string  `json:"key"`
		Revisions []int64 `json:"persisted_success_rev_after"`
		Stored    struct {
			Rev       int64     `json:"rev"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"stored"`
		Now time.Time `json:"now"`
	} `json:"_fixture"`
	Request struct {
		Method, Path string
		Body         json.RawMessage
	} `json:"request"`
	Status   int            `json:"status"`
	Response map[string]any `json:"response"`
}

func contractMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, e := json.Marshal(v)
	require.NoError(t, e)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}
func seedPolicy(t *testing.T, r *cacheRouteStore, p service.CachePolicyView) {
	t.Helper()
	if p.Stored == nil {
		return
	}
	b, e := json.Marshal(p.Stored)
	require.NoError(t, e)
	r.rows[p.SettingKey] = string(b)
}
func normalizePolicyTimes(t *testing.T, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			switch k {
			case "at", "generated_at", "process_started_at", "loaded_at", "updated_at":
				if value != nil {
					txt, ok := value.(string)
					require.True(t, ok)
					_, e := time.Parse(time.RFC3339, txt)
					require.NoError(t, e)
					require.True(t, strings.HasSuffix(txt, "Z"))
					x[k] = "<UTC>"
				}
				continue
			}
			normalizePolicyTimes(t, value)
		}
	case []any:
		for _, value := range x {
			normalizePolicyTimes(t, value)
		}
	}
}
func cacheTestRouter(p *service.CachePolicyService, audit *service.AuditLogService, auth gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/api/v1/admin")
	if auth != nil {
		group.Use(auth)
	} else {
		group.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
			c.Set(middleware.ContextKeyAuthEmail, "admin@example.com")
			c.Next()
		})
	}
	group.Use(gin.HandlerFunc(middleware.NewAuditLogMiddleware(audit)))
	h := &handler.Handlers{Admin: &handler.AdminHandlers{CachePolicy: adminhandler.NewCachePolicyHandler(p, audit, "0.0.0-contract")}}
	registerCachePolicyRoutes(group, h)
	return r
}
func cacheCall(t *testing.T, r *gin.Engine, method, path string, body []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

// Future milestone fixtures are replayed against M1 availability, never used
// to expose unimplemented policies. Their 409/422 rules are exercised on the
// available keys by the service and HTTP tests.
func TestCachePolicyContractFixtures(t *testing.T) {
	root := "testdata/cache_policies_contract"
	manifest, err := os.ReadFile(filepath.Join(root, "SHA256.json"))
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	require.Len(t, hashes, 27)
	defaultRaw, err := os.ReadFile(filepath.Join(root, "list_default_all_off.json"))
	require.NoError(t, err)
	var defaultFixture cacheContract
	require.NoError(t, json.Unmarshal(defaultRaw, &defaultFixture))
	defaultPolicies := defaultFixture.Response["data"].(map[string]any)["policies"].([]any)
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, err)
			require.Equal(t, hashes[name], fmt.Sprintf("%x", sha256.Sum256(raw)))
			var f cacheContract
			require.NoError(t, json.Unmarshal(raw, &f))
			repo := &cacheRouteStore{rows: map[string]string{service.SettingKeyClaudeCacheFallbackPolicy: `{"enabled":true,"group_ids":[1,2]}`}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			auditRepo := &cacheRouteAudit{}
			audit := service.NewAuditLogService(auditRepo, nil)
			var list service.CachePolicyList
			data, _ := f.Response["data"].(map[string]any)
			var afterRefresh func()
			if strings.HasPrefix(name, "list_") {
				b, _ := json.Marshal(data)
				require.NoError(t, json.Unmarshal(b, &list))
				cfg.Gateway.CacheFeatures.KillSwitch = list.Static.KillSwitch
				cfg.Gateway.CacheTrace.MaxByteBudgetMiB = list.Static.Caps.MaxByteBudgetMiB
				for _, p := range list.Policies {
					if p.Available {
						seedPolicy(t, repo, p)
					}
				}
				if name == "list_pending_reload.json" {
					saved := repo.rows["cache_observability_policy"]
					repo.rows["cache_observability_policy"] = `{"rev":4,"spec":{"enabled":false}}`
					afterRefresh = func() { repo.rows["cache_observability_policy"] = saved }
				}
				if name == "list_parse_error.json" {
					var doc map[string]any
					require.NoError(t, json.Unmarshal([]byte(repo.rows["cache_observability_policy"]), &doc))
					doc["spec"] = map[string]any{"enabled": true, "future_option": true}
					b, _ := json.Marshal(doc)
					repo.rows["cache_observability_policy"] = string(b)
				}
				// M3 list cases still execute against M1 and keep their master/observability state.
				if strings.Contains(name, "m3") {
					ps := data["policies"].([]any)
					for i := 2; i < 6; i++ {
						ps[i] = contractMap(t, defaultPolicies[i])
					}
				}
			} else {
				switch name {
				case "put_200.json", "put_partial_update.json":
					b, _ := json.Marshal(data)
					var view service.CachePolicyView
					require.NoError(t, json.Unmarshal(b, &view))
					view.Stored.Rev--
					if name == "put_200.json" {
						view.Stored.Spec["enabled"] = false
					} else {
						view.Stored.Spec["rate_mib_per_s"] = 32
					}
					seedPolicy(t, repo, view)
				case "validate_invalid.json":
					repo.rows["cache_observability_policy"] = `{"rev":2,"spec":{}}`
				case "validate_ok_disable.json":
					repo.rows["cache_observability_policy"] = `{"rev":2,"spec":{"enabled":true}}`
				}
			}
			var options []service.CachePolicyOption
			if f.Fixture != nil {
				options = append(options, service.WithCachePolicyClock(func() time.Time { return f.Fixture.Now }))
			}
			s := service.NewCachePolicyService(repo, cfg, options...)
			require.NoError(t, s.Refresh(context.Background()))
			if afterRefresh != nil {
				afterRefresh()
			}
			router := cacheTestRouter(s, audit, nil)
			expectedStatus := f.Status
			expected := f.Response
			switch name {
			case "put_403.json":
				// Permission fixture is replayed using the real JWT/admin middleware below.
				router = cacheTestRouter(s, audit, cacheOrdinaryUserAuth(t))
			case "put_409.json", "put_422.json", "validate_rev_stale.json", "validate_ok_routing_enable_m4.json":
				parts := strings.Split(f.Request.Path, "/")
				key := parts[6]
				expectedStatus = 404
				expected = map[string]any{"code": float64(404), "message": "cache policy not available in this build", "reason": "CACHE_POLICY_NOT_IN_BUILD", "metadata": map[string]any{"key": key}}
			case "validate_ok_master_enable_m3.json":
				data["affected"] = []any{}
				for _, key := range []string{"upstream_identity", "anthropic_identity", "routing"} {
					data["affected"] = append(data["affected"].([]any), map[string]any{"key": key, "state_before": "not_in_build", "state_after": "not_in_build", "active_after": nil})
				}
			case "history.json", "history_after_rev.json", "history_pending_flush.json",
				"history_truncated_retention.json", "history_empty_within_window.json",
				"history_empty_outside_window.json", "history_gap.json":
				require.NotNil(t, f.Fixture)
				settings := map[string]string{"master": "cache_feature_master", "observability": "cache_observability_policy",
					"northstar": "cache_northstar_policy", "upstream_identity": "cache_upstream_identity_policy",
					"anthropic_identity": "cache_anthropic_identity_policy", "routing": "cache_routing_policy"}
				stored, err := json.Marshal(map[string]any{"rev": f.Fixture.Stored.Rev, "updated_at": f.Fixture.Stored.UpdatedAt,
					"spec": map[string]any{"enabled": false}})
				require.NoError(t, err)
				repo.rows[settings[f.Fixture.Key]] = string(stored)
				present := map[int64]bool{}
				oldest := f.Fixture.Now
				items := data["items"].([]any)
				for _, rawItem := range items {
					b, _ := json.Marshal(rawItem)
					var item service.CachePolicyHistoryItem
					require.NoError(t, json.Unmarshal(b, &item))
					var uid *int64
					email := ""
					if item.Actor != nil {
						uid = &item.Actor.UserID
						email = item.Actor.Email
					}
					row := &service.AuditLog{CreatedAt: item.At, Action: item.Action, Method: "PUT", ActorUserID: uid, ActorEmail: email, StatusCode: item.StatusCode, Extra: map[string]any{}}
					if item.At.Before(oldest) {
						oldest = item.At
					}
					if item.RevAfter != nil {
						present[*item.RevAfter] = true
						row.Extra["rev_after"] = *item.RevAfter
						row.Extra["rev_before"] = *item.RevBefore
					}
					row.Extra["changed_fields"] = strings.Join(item.ChangedFields, ",")
					b, _ = json.Marshal(map[string]any{"reason": item.Reason})
					row.RequestBody = string(b)
					auditRepo.rows = append(auditRepo.rows, row)
				}
				for _, rev := range f.Fixture.Revisions {
					if present[rev] {
						continue
					}
					auditRepo.rows = append(auditRepo.rows, &service.AuditLog{
						CreatedAt: oldest.Add(-time.Duration(100-rev) * time.Minute),
						Action:    "admin.cache.policies." + f.Fixture.Key + ".update", Method: "PUT", StatusCode: 200,
						Extra: map[string]any{"rev_after": rev, "rev_before": rev - 1},
					})
				}
			}
			status, got := cacheCall(t, router, f.Request.Method, f.Request.Path, f.Request.Body)
			require.Equal(t, expectedStatus, status, got)
			normalizePolicyTimes(t, got)
			normalizePolicyTimes(t, expected)
			require.Equal(t, expected, got)
			// Drain middleware output synchronously; validate must produce zero records.
			audit.Start()
			audit.Stop()
			if strings.HasSuffix(f.Request.Path, "/validate") {
				require.Empty(t, auditRepo.rows)
			}
			if f.Request.Method == "PUT" && status == 200 {
				require.Len(t, auditRepo.rows, 1)
				row := auditRepo.rows[0]
				require.Equal(t, "admin.cache.policies.observability.update", row.Action)
				require.Equal(t, "observability", row.Extra["policy_key"])
				require.Contains(t, row.Extra, "rev_before")
				require.Contains(t, row.Extra, "rev_after")
				require.NotContains(t, fmt.Sprint(row.Extra["changed_fields"]), "true")
			}
		})
	}
}

type cacheUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r cacheUserRepo) GetByID(context.Context, int64) (*service.User, error) { return r.user, nil }
func cacheOrdinaryUserAuth(t *testing.T) gin.HandlerFunc {
	t.Helper()
	cfg := &config.Config{}
	cfg.JWT.Secret = "contract-only-signing-secret"
	cfg.JWT.ExpireHour = 1
	user := &service.User{ID: 9, Email: "user@example.com", Role: "user", Status: "active"}
	repo := cacheUserRepo{user: user}
	auth := service.NewAuthService(nil, repo, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	token, err := auth.GenerateToken(context.Background(), user)
	require.NoError(t, err)
	adminAuth := middleware.NewAdminAuthMiddleware(auth, service.NewUserService(repo, nil, nil, nil), nil, nil)
	return func(c *gin.Context) {
		c.Request.Header.Set("Authorization", "Bearer "+token)
		gin.HandlerFunc(adminAuth)(c)
	}
}
func TestCachePolicyAdminPermissionAllEndpoints(t *testing.T) {
	repo := &cacheRouteStore{rows: map[string]string{}}
	s := service.NewCachePolicyService(repo, &config.Config{})
	router := cacheTestRouter(s, service.NewAuditLogService(&cacheRouteAudit{}, nil), cacheOrdinaryUserAuth(t))
	for _, v := range []struct{ method, path string }{{"GET", ""}, {"GET", "/observability"}, {"POST", "/observability/validate"}, {"PUT", "/observability"}, {"GET", "/history"}} {
		status, body := cacheCall(t, router, v.method, "/api/v1/admin/cache/policies"+v.path, []byte(`{}`))
		require.Equal(t, 403, status)
		require.Equal(t, "FORBIDDEN", body["code"])
		require.Equal(t, "Admin access required", body["message"])
	}
}
func TestCachePolicyHTTPConfirmConflictMalformed(t *testing.T) {
	repo := &cacheRouteStore{rows: map[string]string{}}
	s := service.NewCachePolicyService(repo, &config.Config{})
	auditRepo := &cacheRouteAudit{}
	audit := service.NewAuditLogService(auditRepo, nil)
	router := cacheTestRouter(s, audit, nil)
	path := "/api/v1/admin/cache/policies/master"
	status, body := cacheCall(t, router, "PUT", path, []byte(`{"expected_rev":0,"spec":{"enabled":true}}`))
	require.Equal(t, 422, status)
	require.Equal(t, "reason,checkbox", body["metadata"].(map[string]any)["missing"])
	status, _ = cacheCall(t, router, "PUT", path, []byte(`{"expected_rev":0,"spec":{"enabled":true},"reason":"确认","confirm":{"checkbox":true}}`))
	require.Equal(t, 200, status)
	status, body = cacheCall(t, router, "PUT", path, []byte(`{"expected_rev":0,"spec":{"enabled":false}}`))
	require.Equal(t, 409, status)
	require.Equal(t, map[string]any{"current_rev": "1", "expected_rev": "0"}, body["metadata"])
	for _, raw := range []string{"{", "null", `{}`, `{"expected_rev":0,"spec":{}} {}`} {
		status, body = cacheCall(t, router, "POST", path+"/validate", []byte(raw))
		require.Equal(t, 200, status)
		require.Equal(t, false, body["data"].(map[string]any)["valid"])
	}
	audit.Start()
	audit.Stop()
	require.Len(t, auditRepo.rows, 3)
	status, body = cacheCall(t, router, "GET", "/api/v1/admin/cache/policies/history?key=master", nil)
	require.Equal(t, 200, status)
	data := body["data"].(map[string]any)
	require.True(t, data["history_complete"].(bool))
	require.Len(t, data["items"], 3)
	auditRepo.rows = []*service.AuditLog{{
		Action: "admin.cache.policies.master.update", Method: "PUT", StatusCode: 200,
		CreatedAt: time.Now(), Extra: map[string]any{"rev_before": int64(1), "rev_after": int64(2)},
	}}
	status, body = cacheCall(t, router, "GET", "/api/v1/admin/cache/policies/history?key=master", nil)
	require.Equal(t, 200, status)
	require.Equal(t, false, body["data"].(map[string]any)["history_complete"])
}

func TestCachePolicyPublicSettingsDoNotExposePolicies(t *testing.T) {
	repo := &cacheRouteStore{rows: map[string]string{}}
	keys := []string{"cache_feature_master", "cache_observability_policy", "cache_northstar_policy", "cache_upstream_identity_policy", "cache_anthropic_identity_policy", "cache_routing_policy"}
	for _, key := range keys {
		repo.rows[key] = `{"rev":123,"reason":"admin-only-canary","spec":{"enabled":true}}`
	}
	public := handler.NewSettingHandler(service.NewSettingService(repo, &config.Config{}), "contract")
	r := gin.New()
	r.GET("/api/v1/settings/public", public.GetPublicSettings)
	status, body := cacheCall(t, r, "GET", "/api/v1/settings/public", nil)
	require.Equal(t, 200, status)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	for _, key := range keys {
		require.NotContains(t, string(raw), key)
	}
	require.NotContains(t, string(raw), "admin-only-canary")
	// All shared DTO source files are covered, not only the public settings type.
	files, err := filepath.Glob("../../handler/dto/*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, key := range keys {
			require.NotContains(t, string(source), key, file)
		}
	}
}

func (r cacheUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func TestCachePolicyHistoryBeforeAuditFlush(t *testing.T) {
	repo := &cacheRouteStore{rows: map[string]string{}}
	policies := service.NewCachePolicyService(repo, &config.Config{})
	auditRepo := &cacheRouteAudit{}
	audit := service.NewAuditLogService(auditRepo, nil)
	router := cacheTestRouter(policies, audit, nil)
	status, _ := cacheCall(t, router, "PUT", "/api/v1/admin/cache/policies/observability",
		[]byte(`{"expected_rev":0,"spec":{"enabled":true}}`))
	require.Equal(t, 200, status)
	require.EqualValues(t, 1, policies.Snapshot().ObservabilityRevision())
	require.Empty(t, auditRepo.rows, "writer deliberately has not started")
	status, body := cacheCall(t, router, "GET", "/api/v1/admin/cache/policies/history?key=observability", nil)
	require.Equal(t, 200, status)
	data := body["data"].(map[string]any)
	require.Equal(t, true, data["history_complete"])
	require.Empty(t, data["items"])
	require.Nil(t, data["history_incomplete_reason"])
	audit.Start()
	audit.Stop()
	status, body = cacheCall(t, router, "GET", "/api/v1/admin/cache/policies/history?key=observability", nil)
	require.Equal(t, 200, status)
	require.Equal(t, true, body["data"].(map[string]any)["history_complete"])
	require.Len(t, body["data"].(map[string]any)["items"], 1)
}

func TestCachePolicyHistoryPersistedContinuity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		revisions []int64
		complete  bool
	}{
		{"empty_old_history", nil, false},
		{"failed_only_unwritten", nil, true},
		{"continuous", []int64{3, 1, 2}, true},
		{"early_retention", []int64{4, 5}, false},
		{"middle_gap", []int64{1, 3}, false},
		{"duplicate_is_not_a_gap", []int64{1, 1, 2}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &cacheRouteStore{rows: map[string]string{
				"cache_observability_policy": `{"rev":99,"spec":{"enabled":false}}`,
			}}
			if tc.name == "failed_only_unwritten" {
				repo.rows["cache_observability_policy"] = `{"rev":0,"spec":{"enabled":false}}`
			}
			policies := service.NewCachePolicyService(repo, &config.Config{})
			auditRepo := &cacheRouteAudit{}
			for _, rev := range tc.revisions {
				auditRepo.rows = append(auditRepo.rows, &service.AuditLog{
					Action: "admin.cache.policies.observability.update", Method: "PUT", StatusCode: 200,
					Extra: map[string]any{"rev_after": rev, "rev_before": rev - 1},
				})
			}
			// Even if a failed record has stale metadata, it cannot fill rev 2.
			auditRepo.rows = append(auditRepo.rows, &service.AuditLog{
				Action: "admin.cache.policies.observability.update", Method: "PUT", StatusCode: 422,
				Extra: map[string]any{"rev_after": int64(2)},
			})
			after := int64(100)
			before := time.Time{}
			for _, query := range []service.CachePolicyHistoryQuery{
				{Key: "observability", Limit: 1},
				{Key: "observability", Before: &before},
				{Key: "observability", AfterRev: &after},
			} {
				history, err := policies.History(context.Background(), service.NewAuditLogService(auditRepo, nil), query)
				require.NoError(t, err)
				require.Equal(t, tc.complete, history.HistoryComplete)
				if tc.complete {
					require.Nil(t, history.HistoryIncompleteReason)
				}
			}
		})
	}
}

func TestCachePolicyHistoryCombinesReasonsAcrossKeys(t *testing.T) {
	policies := service.NewCachePolicyService(&cacheRouteStore{rows: map[string]string{}}, &config.Config{})
	auditRepo := &cacheRouteAudit{}
	for key, revisions := range map[string][]int64{"master": {1, 3}, "observability": {5, 6}} {
		for _, rev := range revisions {
			auditRepo.rows = append(auditRepo.rows, &service.AuditLog{
				Action: "admin.cache.policies." + key + ".update", Method: "PUT", StatusCode: 200,
				Extra: map[string]any{"rev_before": rev - 1, "rev_after": rev},
			})
		}
	}
	for key, want := range map[string]string{"": "earlier_missing", "master": "gap", "observability": "earlier_missing"} {
		history, err := policies.History(context.Background(), service.NewAuditLogService(auditRepo, nil), service.CachePolicyHistoryQuery{Key: key})
		require.NoError(t, err)
		require.False(t, history.HistoryComplete)
		require.NotNil(t, history.HistoryIncompleteReason)
		require.Equal(t, want, *history.HistoryIncompleteReason)
	}
}

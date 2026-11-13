//go:build unit

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedAdminHandlerEngine struct {
	service.SchedQualityEngine
	p        *service.SchedQualitySnapshot
	conflict bool
	version  int64
	override service.SchedOverride
	resetErr error
}

func (e *schedAdminHandlerEngine) Snapshot() *service.SchedQualitySnapshot { return e.p }
func (e *schedAdminHandlerEngine) Reset(context.Context, int64) (time.Time, error) {
	return e.p.At, e.resetErr
}
func (e *schedAdminHandlerEngine) SetOverride(_ context.Context, o service.SchedOverride) error {
	e.override = o
	return nil
}
func (e *schedAdminHandlerEngine) ClearOverride(context.Context, int64, int64) error {
	e.override = service.SchedOverride{}
	return nil
}
func (e *schedAdminHandlerEngine) Policy(_ context.Context, id int64) (service.SchedPolicyRecord, error) {
	return service.SchedPolicyRecord{GroupID: id, Version: e.version, Policy: schedquality.Policy{TTFTSeconds: 10}}, nil
}
func (e *schedAdminHandlerEngine) UpdatePolicy(_ context.Context, id, v int64, p schedquality.Policy) (service.SchedPolicyRecord, error) {
	if e.conflict || v != e.version {
		return service.SchedPolicyRecord{}, service.ErrSchedPolicyConflict
	}
	e.version++
	return service.SchedPolicyRecord{GroupID: id, Version: e.version, Policy: p}, nil
}

type schedAdminHandlerAccounts struct {
	service.SchedAdminAccountRepository
	a *service.Account
}

func (r *schedAdminHandlerAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if id != r.a.ID {
		return nil, service.ErrAccountNotFound
	}
	return r.a, nil
}
func (r *schedAdminHandlerAccounts) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	return []*service.Account{r.a}, nil
}
func (r *schedAdminHandlerAccounts) SchedAdminClearError(_ context.Context, id int64, message string) (bool, error) {
	r.a.Status, r.a.ErrorMessage = service.StatusActive, ""
	return true, nil
}

type schedAdminHandlerGroups struct{}

func (schedAdminHandlerGroups) GetByID(_ context.Context, id int64) (*service.Group, error) {
	if id != 1 && id != 2 {
		return nil, service.ErrGroupNotFound
	}
	return &service.Group{ID: id, Name: "group", Platform: service.PlatformOpenAI}, nil
}

type schedAdminHandlerBoard struct{}

func (schedAdminHandlerBoard) Board(context.Context, int64, string) ([]service.SchedBoardRow, error) {
	return []service.SchedBoardRow{{AccountID: 1, Rank: 1, Name: "one", Layer: "normal"}}, nil
}

type schedAdminHandlerAudit struct {
	service.AuditLogRepository
	rows []*service.AuditLog
}

func (a *schedAdminHandlerAudit) BatchInsert(_ context.Context, rows []*service.AuditLog) (int64, error) {
	a.rows = append(a.rows, rows...)
	return int64(len(rows)), nil
}

func schedAdminHandlerFixture() (*SchedQualityHandler, *schedAdminHandlerEngine, *schedAdminHandlerAccounts) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	engine := &schedAdminHandlerEngine{p: &service.SchedQualitySnapshot{At: now, GlobalEnabled: true, GroupRows: map[int64][]service.SchedGroupRow{}, Accounts: map[int64]schedquality.AccountSummary{}}, version: 63}
	repo := &schedAdminHandlerAccounts{a: &service.Account{ID: 1, Name: "one", Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{1}, Groups: []*service.Group{{ID: 1, Name: "group", Platform: service.PlatformOpenAI}}}}
	s := service.NewSchedAdminService(service.SchedAdminDependencies{Engine: engine, Accounts: repo, Groups: schedAdminHandlerGroups{}, Board: schedAdminHandlerBoard{}, Usage: schedAdminHandlerUsage{}, Now: func() time.Time { return now }})
	return NewSchedQualityHandler(s), engine, repo
}
func schedAdminHandlerRoutes(r *gin.Engine, h *SchedQualityHandler) {
	g := r.Group("/api/v1/admin/sched-quality")
	g.POST("/accounts/summary", h.Summary)
	g.GET("/accounts/attention", h.Attention)
	g.GET("/accounts/:id", h.Detail)
	g.POST("/accounts/:id/reset", h.Reset)
	g.PUT("/accounts/:id/override", h.SetOverride)
	g.DELETE("/accounts/:id/override", h.ClearOverride)
	g.DELETE("/accounts/:id/auto-states/*key", h.ClearAutoState)
	g.GET("/groups/:id/policy", h.Policy)
	g.PUT("/groups/:id/policy", h.UpdatePolicy)
	g.GET("/groups/:id/order", h.Order)
	g.POST("/groups/summary", h.GroupsSummary)
	g.POST("/groups/:id/policy/preview", h.PreviewPolicy)
}
func schedAdminHandlerRouter(h *SchedQualityHandler, middlewareHandlers ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", middleware.AuthSubject{UserID: 7}); c.Next() })
	r.Use(middlewareHandlers...)
	schedAdminHandlerRoutes(r, h)
	return r
}
func schedAdminHandlerRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/v1/admin/sched-quality"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
func schedAdminHandlerData(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	require.Equal(t, 200, w.Code, w.Body.String())
	var envelope struct{ Data map[string]json.RawMessage }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	return envelope.Data
}
func TestSchedQualityContractShapes(t *testing.T) {
	cases := []struct {
		method, path, body string
		keys               []string
	}{{"POST", "/accounts/summary", `{"account_ids":[1]}`, []string{"at", "stale", "global_enabled", "accounts"}}, {"GET", "/accounts/attention", "", []string{"substandard", "exempt", "observing"}}, {"GET", "/accounts/1", "", []string{"account", "at", "stale", "global_enabled", "status", "stop", "reset_at", "groups", "auto_states", "exemptions"}}, {"POST", "/accounts/1/reset", "", []string{"account_id", "reset_at", "cleared", "kept", "failed"}}, {"GET", "/groups/1/policy", "", []string{"group_id", "version", "policy"}}, {"PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"enabled":true,"ttft_seconds":10,"max_error_rate":0.05,"min_cache_ratio":0,"takeover_idle_seconds":-1}}`, []string{"group_id", "version", "policy"}}, {"GET", "/groups/1/order", "", []string{"group_id", "model", "models", "at", "stale", "global_enabled", "rows"}}, {"DELETE", "/accounts/1/auto-states/error", "", []string{"cleared"}}, {"POST", "/groups/summary", `{"group_ids":[1]}`, []string{"at", "stale", "groups"}}, {"POST", "/groups/1/policy/preview", `{"expected_version":63,"policy":{"enabled":true,"ttft_seconds":10,"max_error_rate":0.05,"min_cache_ratio":0,"takeover_idle_seconds":0}}`, []string{"now", "after", "changed"}}}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			h, _, repo := schedAdminHandlerFixture()
			if tc.method == "DELETE" {
				repo.a.Status = service.StatusError
			}
			data := schedAdminHandlerData(t, schedAdminHandlerRequest(schedAdminHandlerRouter(h), tc.method, tc.path, tc.body))
			keys := []string{}
			for k := range data {
				keys = append(keys, k)
			}
			require.ElementsMatch(t, tc.keys, keys)
			if tc.path == "/accounts/1" {
				require.Equal(t, "null", string(data["reset_at"]))
				require.Equal(t, "null", string(data["stop"]))
				for _, key := range []string{"auto_states", "exemptions"} {
					require.Equal(t, "[]", string(data[key]))
				}
				var groups []service.SchedAdminAccountDetailGroups
				require.NoError(t, json.Unmarshal(data["groups"], &groups))
				require.Len(t, groups, 1)
				require.Equal(t, int64(1), groups[0].GroupID)
				require.Equal(t, []service.SchedAdminAccountDetailGroupsByModel{}, groups[0].ByModel)
			}
		})
	}
}
func TestSchedQualityValidationAndNotFound(t *testing.T) {
	h, _, _ := schedAdminHandlerFixture()
	r := schedAdminHandlerRouter(h)
	cases := []struct {
		method, path, body string
		code               int
	}{{"GET", "/accounts/nope", "", 400}, {"GET", "/accounts/0", "", 400}, {"GET", "/accounts/999", "", 404}, {"GET", "/groups/999/policy", "", 404}, {"GET", "/groups/999/order", "", 404}, {"POST", "/accounts/summary", `{"account_ids":[0]}`, 400}, {"POST", "/accounts/summary", `{}`, 400}, {"PUT", "/accounts/1/override", `{"group_id":9,"mode":"exempt","reason":"x"}`, 404}, {"PUT", "/accounts/1/override", `{"group_id":2,"mode":"exempt","reason":"x"}`, 400}, {"PUT", "/accounts/1/override", `{"group_id":0,"mode":"bad","reason":"x"}`, 400}, {"PUT", "/accounts/1/override", `{"group_id":0,"mode":"exempt","reason":"x","expires_at":"2020-01-01T00:00:00Z"}`, 400}, {"DELETE", "/accounts/1/override?group_id=bad", "", 400}, {"DELETE", "/accounts/1/auto-states/nope", "", 404}, {"PUT", "/groups/1/policy", `{"policy":{"enabled":true,"ttft_seconds":10,"max_error_rate":0.05,"min_cache_ratio":0,"takeover_idle_seconds":0}}`, 400}, {"PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"ttft_seconds":0.1}}`, 400}, {"PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"ttft_seconds":10,"takeover_idle_seconds":1}}`, 400}}
	for _, tc := range cases {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			w := schedAdminHandlerRequest(r, tc.method, tc.path, tc.body)
			require.Equal(t, tc.code, w.Code, w.Body.String())
		})
	}
	ids := make([]int64, schedquality.SummaryMaxAccounts+1)
	for i := range ids {
		ids[i] = 1
	}
	body, _ := json.Marshal(map[string]any{"account_ids": ids})
	require.Equal(t, 400, schedAdminHandlerRequest(r, "POST", "/accounts/summary", string(body)).Code)
}
func TestSchedQualityOverrideAllowsEmptyReason(t *testing.T) {
	for _, reason := range []string{"", "   "} {
		t.Run(reason, func(t *testing.T) {
			h, e, _ := schedAdminHandlerFixture()
			body, err := json.Marshal(map[string]any{"group_id": 0, "mode": "exempt", "reason": reason})
			require.NoError(t, err)
			w := schedAdminHandlerRequest(schedAdminHandlerRouter(h), "PUT", "/accounts/1/override", string(body))
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, int64(1), e.override.AccountID)
			require.Equal(t, int64(7), e.override.OperatorID)
			require.Equal(t, schedquality.OverrideExempt, e.override.Mode)
			require.Empty(t, e.override.Reason)
		})
	}
}

func TestSchedQualityGlobalPolicyConflict(t *testing.T) {
	h, e, _ := schedAdminHandlerFixture()
	r := schedAdminHandlerRouter(h)
	e.conflict = true
	body := `{"expected_version":63,"policy":{"enabled":true,"ttft_seconds":10,"max_error_rate":0.05,"min_cache_ratio":0,"takeover_idle_seconds":0}}`
	require.Equal(t, 409, schedAdminHandlerRequest(r, "PUT", "/groups/1/policy", body).Code)
	e.conflict = false
	require.Equal(t, 200, schedAdminHandlerRequest(r, "PUT", "/groups/1/policy", body).Code)
	require.Equal(t, 409, schedAdminHandlerRequest(r, "PUT", "/groups/2/policy", body).Code)
}

func TestSchedQualityResetMissingAccount(t *testing.T) {
	h, engine, _ := schedAdminHandlerFixture()
	r := schedAdminHandlerRouter(h)
	require.Equal(t, 404, schedAdminHandlerRequest(r, "POST", "/accounts/999/reset", "").Code)
	engine.resetErr = sql.ErrNoRows
	require.Equal(t, 404, schedAdminHandlerRequest(r, "POST", "/accounts/1/reset", "").Code)
}
func TestSchedQualityWritesAuditWithOperator(t *testing.T) {
	h, e, repo := schedAdminHandlerFixture()
	audit := &schedAdminHandlerAudit{}
	auditService := service.NewAuditLogService(audit, nil)
	auditService.Start()
	r := schedAdminHandlerRouter(h, gin.HandlerFunc(middleware.NewAuditLogMiddleware(auditService)))
	require.Equal(t, 200, schedAdminHandlerRequest(r, "POST", "/accounts/summary", `{"account_ids":[1]}`).Code)
	require.Equal(t, 200, schedAdminHandlerRequest(r, "PUT", "/accounts/1/override", `{"group_id":0,"mode":"exempt","reason":"repair","expires_at":null}`).Code)
	require.Equal(t, int64(7), e.override.OperatorID)
	require.Equal(t, 200, schedAdminHandlerRequest(r, "DELETE", "/accounts/1/override?group_id=0", "").Code)
	repo.a.Status = service.StatusError
	require.Equal(t, 200, schedAdminHandlerRequest(r, "DELETE", "/accounts/1/auto-states/error", "").Code)
	require.Equal(t, 200, schedAdminHandlerRequest(r, "POST", "/accounts/1/reset", "").Code)
	require.Equal(t, 200, schedAdminHandlerRequest(r, "PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"enabled":true,"ttft_seconds":10,"max_error_rate":0.05,"min_cache_ratio":0,"takeover_idle_seconds":0}}`).Code)
	auditService.Stop()
	require.Len(t, audit.rows, 5)
	for _, a := range audit.rows {
		require.Equal(t, int64(7), *a.ActorUserID)
	}
	require.Contains(t, audit.rows[0].RequestBody, `"reason":"repair"`)
	for i, action := range []string{"override", "clear_override", "clear_auto_state", "reset", "policy"} {
		require.Equal(t, "sched_quality."+action, audit.rows[i].Action)
		require.NotEmpty(t, audit.rows[i].Method)
		require.NotEmpty(t, audit.rows[i].Path)
	}
}

func TestSchedQualityPermissionsFinalCatalog(t *testing.T) {
	cases := []struct {
		method, path string
		module       operatoraccess.Module
		level        operatoraccess.Level
	}{{"POST", "/accounts/summary", "accounts", operatoraccess.LevelView}, {"GET", "/accounts/attention", "accounts", operatoraccess.LevelView}, {"GET", "/accounts/:id", "accounts", operatoraccess.LevelView}, {"POST", "/accounts/:id/reset", "accounts", operatoraccess.LevelManage}, {"PUT", "/accounts/:id/override", "accounts", operatoraccess.LevelManage}, {"DELETE", "/accounts/:id/override", "accounts", operatoraccess.LevelManage}, {"DELETE", "/accounts/:id/auto-states/*key", "accounts", operatoraccess.LevelManage}, {"GET", "/groups/:id/policy", "groups", operatoraccess.LevelView}, {"PUT", "/groups/:id/policy", "groups", operatoraccess.LevelManage}, {"GET", "/groups/:id/order", "groups", operatoraccess.LevelView}, {"POST", "/groups/summary", "groups", operatoraccess.LevelView}, {"POST", "/groups/:id/policy/preview", "groups", operatoraccess.LevelView}}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			path := "/api/v1/admin/sched-quality" + tc.path
			entry, found := operatoraccess.Lookup(operatoraccess.Key(tc.method, path))
			require.True(t, found)
			require.Equal(t, operatoraccess.KindModule, entry.Kind)
			require.Equal(t, tc.module, entry.Module)
			require.Equal(t, tc.level, entry.Level)
			if tc.module == "accounts" {
				require.Equal(t, []operatoraccess.Redaction{"account"}, entry.Redact)
			} else {
				require.Empty(t, entry.Redact)
			}
			for _, level := range []operatoraccess.Level{operatoraccess.LevelView, operatoraccess.LevelManage} {
				perms := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{tc.module: level}}
				decision := operatoraccess.Decide(entry, found, perms)
				want := 200
				if level < tc.level {
					want = 403
					require.NotEmpty(t, decision.Reason)
				} else {
					require.Empty(t, decision.Reason)
				}
				fake := &schedAdminPermissionService{}
				r := gin.New()
				r.Use(func(c *gin.Context) {
					c.Set("user_role", service.RoleOperator)
					c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), &operatoraccess.Scope{UserID: 7, Perms: perms}))
					c.Next()
				}, middleware.AdminPermission())
				schedAdminHandlerRoutes(r, NewSchedQualityHandler(fake))
				w := httptest.NewRecorder()
				actual := strings.ReplaceAll(strings.ReplaceAll(path, ":id", "1"), "*key", "error")
				if tc.method == "DELETE" && strings.HasSuffix(tc.path, "/override") {
					actual += "?group_id=0"
				}
				body := `{"group_ids":[1],"account_ids":[1],"group_id":0,"mode":"exempt","reason":"repair","expected_version":1,"policy":{"enabled":true,"ttft_seconds":10,"max_error_rate":0.05,"min_cache_ratio":0,"takeover_idle_seconds":0}}`
				req := httptest.NewRequest(tc.method, actual, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				r.ServeHTTP(w, req)
				require.Equal(t, want, w.Code, w.Body.String())
				if want == 403 {
					require.Zero(t, fake.calls)
				} else {
					require.Equal(t, 1, fake.calls)
					if strings.HasSuffix(tc.path, "/reset") {
						require.NotContains(t, w.Body.String(), `"error":`)
						require.NotContains(t, w.Body.String(), "upstream.example")
					}
					if strings.HasSuffix(tc.path, "/order") {
						require.Contains(t, w.Body.String(), "https://upstream.example/group")
					}
				}
			}
		})
	}
}

type schedAdminPermissionService struct {
	calls int
}

func (s *schedAdminPermissionService) Summary(context.Context, []int64) (service.SchedAdminSummary, error) {
	s.calls++
	return service.SchedAdminSummary{Accounts: []service.SchedAdminAccountSummary{}}, nil
}
func (s *schedAdminPermissionService) Attention(context.Context) (service.SchedAdminAttention, error) {
	s.calls++
	return service.SchedAdminAttention{Substandard: []int64{}}, nil
}
func (s *schedAdminPermissionService) Detail(context.Context, int64) (service.SchedAdminAccountDetail, error) {
	s.calls++
	return service.SchedAdminAccountDetail{}, nil
}
func (s *schedAdminPermissionService) Reset(context.Context, int64, int64) (service.SchedAdminReset, error) {
	s.calls++
	return service.SchedAdminReset{Failed: []service.SchedAdminResetFailed{{Kind: "temp_auth"}}}, nil
}
func (s *schedAdminPermissionService) SetOverride(context.Context, service.SchedOverride) error {
	s.calls++
	return nil
}
func (s *schedAdminPermissionService) ClearOverride(context.Context, int64, int64, int64) error {
	s.calls++
	return nil
}
func (s *schedAdminPermissionService) ClearAutoState(context.Context, int64, string, int64) (bool, error) {
	s.calls++
	return true, nil
}
func (s *schedAdminPermissionService) Policy(context.Context, int64) (service.SchedAdminPolicy, error) {
	s.calls++
	return service.SchedAdminPolicy{}, nil
}
func (s *schedAdminPermissionService) UpdatePolicy(context.Context, int64, int64, schedquality.Policy, int64) (service.SchedAdminPolicy, error) {
	s.calls++
	return service.SchedAdminPolicy{}, nil
}
func (s *schedAdminPermissionService) Order(context.Context, int64, string) (service.SchedAdminOrder, error) {
	s.calls++
	return service.SchedAdminOrder{Rows: []service.SchedAdminOrderRow{{Name: "https://upstream.example/group"}}}, nil
}

func (r *schedAdminHandlerAccounts) SchedAdminAccountIDs(context.Context) ([]int64, error) {
	return []int64{r.a.ID}, nil
}
func (schedAdminHandlerGroups) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	return []int64{1}, nil
}
func (e *schedAdminHandlerEngine) PreviewPolicy(context.Context, int64, schedquality.Policy) ([]service.SchedPolicyChange, error) {
	return []service.SchedPolicyChange{}, nil
}

type schedAdminHandlerUsage struct{}

func (schedAdminHandlerUsage) GroupLast60(context.Context, []int64, time.Time) (map[int64]usagestats.GroupLast60, error) {
	return map[int64]usagestats.GroupLast60{}, nil
}
func (s *schedAdminPermissionService) GroupsSummary(context.Context, []int64) (service.SchedAdminGroupsSummary, error) {
	s.calls++
	return service.SchedAdminGroupsSummary{}, nil
}
func (s *schedAdminPermissionService) PreviewPolicy(context.Context, int64, int64, schedquality.Policy) (service.SchedAdminPolicyPreview, error) {
	s.calls++
	return service.SchedAdminPolicyPreview{}, nil
}
func TestSchedQualityPolicyFloorAndRequiredFields(t *testing.T) {
	for _, method := range []string{"save", "preview"} {
		t.Run(method, func(t *testing.T) {
			h, _, _ := schedAdminHandlerFixture()
			r := schedAdminHandlerRouter(h)
			path, verb := "/groups/1/policy", "PUT"
			if method == "preview" {
				path += "/preview"
				verb = "POST"
			}
			valid := map[string]any{"enabled": false, "ttft_seconds": 0.01, "max_error_rate": 0.05, "min_cache_ratio": 0.0, "takeover_idle_seconds": 0}
			request := func(p map[string]any) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(map[string]any{"expected_version": 63, "policy": p})
				return schedAdminHandlerRequest(r, verb, path, string(raw))
			}
			for key, value := range valid {
				delete(valid, key)
				w := request(valid)
				require.Equal(t, 400, w.Code)
				valid[key] = nil
				require.Equal(t, 400, request(valid).Code)
				valid[key] = value
			}
			valid["max_error_rate"] = 0.049
			w := request(valid)
			require.Equal(t, 400, w.Code)
			require.Contains(t, w.Body.String(), "SCHED_ERROR_RATE_TOO_LOW")
			valid["max_error_rate"] = 1.01
			w = request(valid)
			require.Equal(t, 400, w.Code)
			require.Contains(t, w.Body.String(), "INVALID_SCHED_POLICY")
			require.NotContains(t, w.Body.String(), "SCHED_ERROR_RATE_TOO_LOW")
			valid["max_error_rate"] = 0.05
			require.Equal(t, 200, request(valid).Code)
		})
	}
}
func TestSchedQualityDemoteAndRemovedEndpoints(t *testing.T) {
	h, _, _ := schedAdminHandlerFixture()
	r := schedAdminHandlerRouter(h)
	require.Equal(t, 400, schedAdminHandlerRequest(r, "PUT", "/accounts/1/override", `{"group_id":0,"mode":"demote"}`).Code)
	require.Equal(t, 404, schedAdminHandlerRequest(r, "POST", "/accounts/1/test", `{}`).Code)
	require.Equal(t, 404, schedAdminHandlerRequest(r, "GET", "/groups/1/board", "").Code)
}

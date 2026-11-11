//go:build unit

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
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
	return []service.SchedBoardRow{{AccountID: 1, Rank: 1, Eligible: true}}, nil
}

type schedAdminHandlerTests struct {
	service.SchedTestRunner
	ticket service.SchedTestTicket
}

func (s *schedAdminHandlerTests) Request(_ context.Context, _ int64, m string, _ bool, _ int64) (service.SchedTestTicket, error) {
	ticket := s.ticket
	ticket.Model = m
	return ticket, nil
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
	engine := &schedAdminHandlerEngine{p: &service.SchedQualitySnapshot{At: now, GlobalEnabled: true, GroupRows: map[int64][]service.SchedGroupRow{}, Evidence: map[service.SchedAccountModel]schedquality.Summary{}}, version: 63}
	repo := &schedAdminHandlerAccounts{a: &service.Account{ID: 1, Name: "one", Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{1}}}
	s := service.NewSchedAdminService(service.SchedAdminDependencies{Engine: engine, Accounts: repo, Groups: schedAdminHandlerGroups{}, Board: schedAdminHandlerBoard{}, Tests: &schedAdminHandlerTests{ticket: service.SchedTestTicket{TestID: "ticket", Started: true}}, Now: func() time.Time { return now }})
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
	g.POST("/accounts/:id/test", h.Test)
	g.DELETE("/accounts/:id/auto-states/:key", h.ClearAutoState)
	g.GET("/groups/:id/policy", h.Policy)
	g.PUT("/groups/:id/policy", h.UpdatePolicy)
	g.GET("/groups/:id/board", h.Board)
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
	}{{"POST", "/accounts/summary", `{"account_ids":[1]}`, []string{"at", "stale", "global_enabled", "accounts"}}, {"GET", "/accounts/attention", "", []string{"cooling"}}, {"GET", "/accounts/1", "", []string{"account", "at", "stale", "global_enabled", "tests_enabled", "reset_at", "models", "groups", "auto_states", "recent_attempts"}}, {"POST", "/accounts/1/reset", "", []string{"reset_at", "cleared", "preserved", "failed"}}, {"POST", "/accounts/1/test", `{"model":"m"}`, []string{"tests"}}, {"GET", "/groups/1/policy", "", []string{"group_id", "version", "policy", "class_limits_ms"}}, {"PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"enabled":true,"ttft_seconds":10,"takeover_idle_seconds":-1}}`, []string{"group_id", "version", "policy", "class_limits_ms"}}, {"GET", "/groups/1/board", "", []string{"group_id", "model", "models", "at", "stale", "global_enabled", "accounts"}}, {"DELETE", "/accounts/1/auto-states/error", "", []string{"cleared"}}}
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
				for _, key := range []string{"models", "groups", "auto_states", "recent_attempts"} {
					require.Equal(t, "[]", string(data[key]))
				}
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
	}{{"GET", "/accounts/nope", "", 400}, {"GET", "/accounts/0", "", 400}, {"GET", "/accounts/999", "", 404}, {"GET", "/groups/999/policy", "", 404}, {"GET", "/groups/999/board", "", 404}, {"POST", "/accounts/summary", `{"account_ids":[0]}`, 400}, {"POST", "/accounts/summary", `{}`, 400}, {"PUT", "/accounts/1/override", `{"group_id":9,"mode":"demote","reason":"x"}`, 404}, {"PUT", "/accounts/1/override", `{"group_id":2,"mode":"demote","reason":"x"}`, 400}, {"PUT", "/accounts/1/override", `{"group_id":0,"mode":"bad","reason":"x"}`, 400}, {"PUT", "/accounts/1/override", `{"group_id":0,"mode":"demote","reason":" "}`, 400}, {"PUT", "/accounts/1/override", `{"group_id":0,"mode":"demote","reason":"x","expires_at":"2020-01-01T00:00:00Z"}`, 400}, {"DELETE", "/accounts/1/override?group_id=bad", "", 400}, {"POST", "/accounts/1/test", "bad", 400}, {"DELETE", "/accounts/1/auto-states/nope", "", 404}, {"PUT", "/groups/1/policy", `{"policy":{"ttft_seconds":10}}`, 400}, {"PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"ttft_seconds":0.1}}`, 400}, {"PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"ttft_seconds":10,"takeover_idle_seconds":1}}`, 400}}
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
func TestSchedQualityGlobalPolicyConflict(t *testing.T) {
	h, e, _ := schedAdminHandlerFixture()
	r := schedAdminHandlerRouter(h)
	e.conflict = true
	body := `{"expected_version":63,"policy":{"ttft_seconds":10}}`
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
	require.Equal(t, 200, schedAdminHandlerRequest(r, "POST", "/accounts/1/test", `{"model":"m"}`).Code)
	repo.a.Status = service.StatusError
	require.Equal(t, 200, schedAdminHandlerRequest(r, "DELETE", "/accounts/1/auto-states/error", "").Code)
	require.Equal(t, 200, schedAdminHandlerRequest(r, "POST", "/accounts/1/reset", "").Code)
	require.Equal(t, 200, schedAdminHandlerRequest(r, "PUT", "/groups/1/policy", `{"expected_version":63,"policy":{"ttft_seconds":10}}`).Code)
	auditService.Stop()
	require.Len(t, audit.rows, 6)
	for _, a := range audit.rows {
		require.Equal(t, int64(7), *a.ActorUserID)
	}
	require.Contains(t, audit.rows[0].RequestBody, `"reason":"repair"`)
	for i, action := range []string{"override", "clear_override", "test", "clear_auto_state", "reset", "policy"} {
		require.Equal(t, "sched_quality."+action, audit.rows[i].Action)
		require.NotEmpty(t, audit.rows[i].Method)
		require.NotEmpty(t, audit.rows[i].Path)
	}
}

func TestSchedQualityPermissionsFinalCatalog(t *testing.T) {
	t.Skip("needs W2 catalog registration")
	cases := []struct {
		method, path string
		module       operatoraccess.Module
		level        operatoraccess.Level
	}{{"POST", "/accounts/summary", "accounts", operatoraccess.LevelView}, {"GET", "/accounts/attention", "accounts", operatoraccess.LevelView}, {"GET", "/accounts/:id", "accounts", operatoraccess.LevelView}, {"POST", "/accounts/:id/reset", "accounts", operatoraccess.LevelManage}, {"PUT", "/accounts/:id/override", "accounts", operatoraccess.LevelManage}, {"DELETE", "/accounts/:id/override", "accounts", operatoraccess.LevelManage}, {"POST", "/accounts/:id/test", "accounts", operatoraccess.LevelManage}, {"DELETE", "/accounts/:id/auto-states/:key", "accounts", operatoraccess.LevelManage}, {"GET", "/groups/:id/policy", "groups", operatoraccess.LevelView}, {"PUT", "/groups/:id/policy", "groups", operatoraccess.LevelManage}, {"GET", "/groups/:id/board", "groups", operatoraccess.LevelView}}
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
				actual := strings.ReplaceAll(strings.ReplaceAll(path, ":id", "1"), ":key", "error")
				if tc.method == "DELETE" && strings.HasSuffix(tc.path, "/override") {
					actual += "?group_id=0"
				}
				body := `{"account_ids":[1],"group_id":0,"mode":"demote","reason":"repair","expected_version":1,"policy":{"ttft_seconds":10}}`
				req := httptest.NewRequest(tc.method, actual, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				r.ServeHTTP(w, req)
				require.Equal(t, want, w.Code, w.Body.String())
				if want == 403 {
					require.Zero(t, fake.calls)
				} else {
					require.Equal(t, 1, fake.calls)
					if strings.HasSuffix(tc.path, "/reset") {
						require.Contains(t, w.Body.String(), operatoraccess.ErrorDetailHidden)
						require.NotContains(t, w.Body.String(), "upstream.example")
					}
					if strings.HasSuffix(tc.path, "/board") {
						require.Contains(t, w.Body.String(), "https://upstream.example/group")
					}
				}
			}
		})
	}
}

type schedAdminPermissionService struct {
	calls     int
	testModel string
	testErr   error
}

func (s *schedAdminPermissionService) Summary(context.Context, []int64, string) (service.SchedAdminSummary, error) {
	s.calls++
	return service.SchedAdminSummary{Accounts: []service.SchedAdminAccountSummary{}}, nil
}
func (s *schedAdminPermissionService) Attention(context.Context) (service.SchedAdminAttention, error) {
	s.calls++
	return service.SchedAdminAttention{Cooling: []int64{}}, nil
}
func (s *schedAdminPermissionService) Detail(context.Context, int64) (service.SchedAdminAccountDetail, error) {
	s.calls++
	return service.SchedAdminAccountDetail{}, nil
}
func (s *schedAdminPermissionService) Reset(context.Context, int64, int64) (service.SchedResetResult, error) {
	s.calls++
	return service.SchedResetResult{Failed: []service.SchedResetItem{{Key: "temp", Kind: "temp_auth", Error: "connection failed at https://upstream.example/private"}}}, nil
}
func (s *schedAdminPermissionService) SetOverride(context.Context, service.SchedOverride) error {
	s.calls++
	return nil
}
func (s *schedAdminPermissionService) ClearOverride(context.Context, int64, int64, int64) error {
	s.calls++
	return nil
}
func (s *schedAdminPermissionService) Test(_ context.Context, _ int64, m string, _ int64) (service.SchedAdminTest, error) {
	s.calls++
	s.testModel = m
	return service.SchedAdminTest{Tests: []service.SchedAdminTestTests{{Model: "m", TestID: "started", Started: true}}}, s.testErr
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
func (s *schedAdminPermissionService) Board(context.Context, int64, string) (service.SchedAdminBoard, error) {
	s.calls++
	return service.SchedAdminBoard{Accounts: []service.SchedAdminBoardAccounts{{Name: "https://upstream.example/group"}}}, nil
}

func TestSchedQualityEmptyTestBodyAndPartialFailureAudit(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(strconv.FormatBool(partial), func(t *testing.T) {
			fake := &schedAdminPermissionService{}
			if partial {
				fake.testErr = &service.SchedAdminTestErrors{Models: []string{"failed-model"}, Err: errors.New("runner failed")}
			}
			audit := &schedAdminHandlerAudit{}
			auditService := service.NewAuditLogService(audit, nil)
			auditService.Start()
			r := schedAdminHandlerRouter(NewSchedQualityHandler(fake), gin.HandlerFunc(middleware.NewAuditLogMiddleware(auditService)))
			w := schedAdminHandlerRequest(r, "POST", "/accounts/1/test", "")
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), `"test_id":"started"`)
			require.Empty(t, fake.testModel)
			require.Equal(t, 1, fake.calls)
			auditService.Stop()
			require.Len(t, audit.rows, 1)
			require.Equal(t, "sched_quality.test", audit.rows[0].Action)
			if partial {
				require.Equal(t, "partial_failure", audit.rows[0].Extra["result"])
			}
		})
	}
}

package admin

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type SchedQualityAdmin interface {
	Summary(context.Context, []int64, string) (service.SchedAdminSummary, error)
	Attention(context.Context) (service.SchedAdminAttention, error)
	Detail(context.Context, int64) (service.SchedAdminAccountDetail, error)
	Reset(context.Context, int64, int64) (service.SchedResetResult, error)
	SetOverride(context.Context, service.SchedOverride) error
	ClearOverride(context.Context, int64, int64, int64) error
	Test(context.Context, int64, string, int64) (service.SchedAdminTest, error)
	ClearAutoState(context.Context, int64, string, int64) (bool, error)
	Policy(context.Context, int64) (service.SchedAdminPolicy, error)
	UpdatePolicy(context.Context, int64, int64, schedquality.Policy, int64) (service.SchedAdminPolicy, error)
	Board(context.Context, int64, string) (service.SchedAdminBoard, error)
}
type SchedQualityHandler struct{ service SchedQualityAdmin }

func NewSchedQualityHandler(s SchedQualityAdmin) *SchedQualityHandler {
	return &SchedQualityHandler{service: s}
}
func schedQualityID(c *gin.Context) (int64, bool) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return 0, false
	}
	return id, true
}
func schedQualityOperator(c *gin.Context) int64 {
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	return subject.UserID
}
func schedQualityReply(c *gin.Context, data any, e error) {
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			e = infraerrors.NotFound("SCHED_RESOURCE_NOT_FOUND", "Account or group not found")
		}
		if errors.Is(e, service.ErrSchedPolicyConflict) {
			e = infraerrors.Conflict("SCHED_POLICY_CONFLICT", "Scheduling policy has changed")
		}
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, data)
}
func (h *SchedQualityHandler) Summary(c *gin.Context) {
	middleware.SkipAudit(c)
	var r struct {
		AccountIDs []int64 `json:"account_ids" binding:"required,dive,gt=0"`
		Model      string  `json:"model"`
	}
	if e := c.ShouldBindJSON(&r); e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	if len(r.AccountIDs) > schedquality.SummaryMaxAccounts {
		response.BadRequest(c, "Too many account ids")
		return
	}
	v, e := h.service.Summary(c.Request.Context(), r.AccountIDs, r.Model)
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) Attention(c *gin.Context) {
	v, e := h.service.Attention(c.Request.Context())
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) Detail(c *gin.Context) {
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	v, e := h.service.Detail(c.Request.Context(), id)
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) Reset(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.reset")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	v, e := h.service.Reset(c.Request.Context(), id, schedQualityOperator(c))
	if e != nil {
		schedQualityReply(c, nil, e)
		return
	}
	out := service.SchedAdminReset{Cleared: []service.SchedAdminResetCleared{}, Preserved: []service.SchedAdminResetPreserved{}, Failed: []service.SchedAdminResetFailed{}}
	if !v.ResetAt.IsZero() {
		stamp := v.ResetAt.UTC().Format(time.RFC3339Nano)
		out.ResetAt = &stamp
	}
	for _, x := range v.Cleared {
		out.Cleared = append(out.Cleared, service.SchedAdminResetCleared{Key: x.Key, Kind: x.Kind})
	}
	for _, x := range v.Preserved {
		out.Preserved = append(out.Preserved, service.SchedAdminResetPreserved{Key: x.Key, Kind: x.Kind, Why: x.Why})
	}
	for _, x := range v.Failed {
		out.Failed = append(out.Failed, service.SchedAdminResetFailed{Key: x.Key, Kind: x.Kind, Error: x.Error})
	}
	middleware.SetAuditExtra(c, map[string]any{"cleared_count": len(v.Cleared), "preserved_count": len(v.Preserved), "failed_count": len(v.Failed)})
	schedQualityReply(c, out, nil)
}
func (h *SchedQualityHandler) SetOverride(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.override")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	var r struct {
		GroupID   *int64     `json:"group_id" binding:"required,gte=0"`
		Mode      string     `json:"mode" binding:"required,oneof=exempt demote"`
		ExpiresAt *time.Time `json:"expires_at"`
		Reason    string     `json:"reason"`
	}
	if e := c.ShouldBindJSON(&r); e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	mode := schedquality.OverrideExempt
	if r.Mode == "demote" {
		mode = schedquality.OverrideDemote
	}
	e := h.service.SetOverride(c.Request.Context(), service.SchedOverride{AccountID: id, GroupID: *r.GroupID, Mode: mode, ExpiresAt: r.ExpiresAt, Reason: r.Reason, OperatorID: schedQualityOperator(c)})
	schedQualityReply(c, nil, e)
}
func (h *SchedQualityHandler) ClearOverride(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.clear_override")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	g, e := strconv.ParseInt(c.Query("group_id"), 10, 64)
	if e != nil || g < 0 {
		response.BadRequest(c, "Invalid group_id")
		return
	}
	e = h.service.ClearOverride(c.Request.Context(), id, g, schedQualityOperator(c))
	schedQualityReply(c, nil, e)
}
func (h *SchedQualityHandler) Test(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.test")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	var r struct {
		Model string `json:"model"`
	}
	if e := c.ShouldBindJSON(&r); e != nil && !errors.Is(e, io.EOF) {
		response.BadRequest(c, e.Error())
		return
	}
	v, e := h.service.Test(c.Request.Context(), id, r.Model, schedQualityOperator(c))
	var failures *service.SchedAdminTestErrors
	if errors.As(e, &failures) {
		result := "failure"
		if len(v.Tests) > 0 {
			result = "partial_failure"
			e = nil
		}
		middleware.SetAuditExtra(c, map[string]any{"result": result, "test_failed_count": len(failures.Models), "test_ticket_count": len(v.Tests)})
	}
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) ClearAutoState(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.clear_auto_state")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	if c.Param("key") == "" {
		response.BadRequest(c, "Invalid state key")
		return
	}
	v, e := h.service.ClearAutoState(c.Request.Context(), id, c.Param("key"), schedQualityOperator(c))
	schedQualityReply(c, service.SchedAdminClearAutoState{Cleared: v}, e)
}
func (h *SchedQualityHandler) Policy(c *gin.Context) {
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	v, e := h.service.Policy(c.Request.Context(), id)
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) UpdatePolicy(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.policy")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	var r struct {
		Version *int64                          `json:"expected_version" binding:"required,gte=0"`
		Policy  *service.SchedAdminPolicyPolicy `json:"policy" binding:"required"`
	}
	if e := c.ShouldBindJSON(&r); e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	p := r.Policy
	v, e := h.service.UpdatePolicy(c.Request.Context(), id, *r.Version, schedquality.Policy{Enabled: p.Enabled, TTFTSeconds: p.TTFTSeconds, MaxErrorRate: p.MaxErrorRate, MinCacheRatio: p.MinCacheRatio, TakeoverIdleSeconds: p.TakeoverIdleSeconds}, schedQualityOperator(c))
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) Board(c *gin.Context) {
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	v, e := h.service.Board(c.Request.Context(), id, c.Query("model"))
	schedQualityReply(c, v, e)
}

var _ SchedQualityAdmin = (*service.SchedAdminService)(nil)

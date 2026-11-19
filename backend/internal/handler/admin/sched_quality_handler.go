package admin

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type SchedQualityAdmin interface {
	GroupPerformance(context.Context, int64) (service.SchedAdminPerformance, error)
	AccountPerformance(context.Context, int64) (service.SchedAdminPerformance, error)
	Summary(context.Context, []int64) (service.SchedAdminSummary, error)
	Attention(context.Context) (service.SchedAdminAttention, error)
	AccountFilter(context.Context, string) (service.SchedQualityFilter, error)
	Detail(context.Context, int64) (service.SchedAdminAccountDetail, error)
	Reset(context.Context, int64, int64) (service.SchedAdminReset, error)
	SetOverride(context.Context, service.SchedOverride) error
	ClearOverride(context.Context, int64, int64, int64) error
	ClearAutoState(context.Context, int64, string, int64) (bool, error)
	Policy(context.Context, int64) (service.SchedAdminPolicy, error)
	UpdatePolicy(context.Context, int64, int64, schedquality.Policy, int64) (service.SchedAdminPolicySaved, error)
	Decisions(context.Context, int64, int, int64) ([]service.SchedAdminDecision, error)
	TTFTRule(context.Context) (service.SchedAdminTTFTRule, error)
	UpdateTTFTRule(context.Context, int64, schedquality.RuleBParams, int64) (service.SchedAdminTTFTRuleSaved, error)
	Order(context.Context, int64, string) (service.SchedAdminOrder, error)
	GroupsSummary(context.Context, []int64) (service.SchedAdminGroupsSummary, error)
	PreviewPolicy(context.Context, int64, int64, schedquality.Policy) (service.SchedAdminPolicyPreview, error)
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
	}
	if e := c.ShouldBindJSON(&r); e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	if len(r.AccountIDs) > schedquality.SummaryMaxAccounts {
		response.BadRequest(c, "Too many account ids")
		return
	}
	v, e := h.service.Summary(c.Request.Context(), r.AccountIDs)
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
	middleware.SetAuditExtra(c, map[string]any{"cleared_count": len(v.Cleared), "preserved_count": len(v.Kept), "failed_count": len(v.Failed)})
	schedQualityReply(c, v, nil)
}
func (h *SchedQualityHandler) SetOverride(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.override")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	var r struct {
		GroupID   *int64     `json:"group_id" binding:"required,gte=0"`
		Mode      string     `json:"mode" binding:"required,oneof=exempt"`
		ExpiresAt *time.Time `json:"expires_at"`
		Reason    string     `json:"reason"`
	}
	if e := c.ShouldBindJSON(&r); e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	e := h.service.SetOverride(c.Request.Context(), service.SchedOverride{AccountID: id, GroupID: *r.GroupID, Mode: schedquality.OverrideExempt, ExpiresAt: r.ExpiresAt, Reason: r.Reason, OperatorID: schedQualityOperator(c)})
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
func (h *SchedQualityHandler) ClearAutoState(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.clear_auto_state")
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	key := strings.TrimPrefix(c.Param("key"), "/")
	if key == "" {
		response.BadRequest(c, "Invalid state key")
		return
	}
	v, e := h.service.ClearAutoState(c.Request.Context(), id, key, schedQualityOperator(c))
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
	version, p, ok := schedQualityPolicyRequest(c)
	if !ok {
		return
	}
	v, e := h.service.UpdatePolicy(c.Request.Context(), id, version, p, schedQualityOperator(c))
	schedQualityReply(c, v, e)
}
func schedQualityPolicyRequest(c *gin.Context) (int64, schedquality.Policy, bool) {
	var r struct {
		Version *int64 `json:"expected_version"`
		Policy  *struct {
			Enabled             *bool    `json:"enabled"`
			TTFTSeconds         *float64 `json:"ttft_seconds"`
			MaxErrorRate        *float64 `json:"max_error_rate"`
			MinCacheRatio       *float64 `json:"min_cache_ratio"`
			TakeoverIdleSeconds *int     `json:"takeover_idle_seconds"`
		} `json:"policy"`
	}
	invalid := func() {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_SCHED_POLICY", "All policy fields and a nonnegative expected_version are required"))
	}
	if e := c.ShouldBindJSON(&r); e != nil {
		invalid()
		return 0, schedquality.Policy{}, false
	}
	if r.Version == nil || *r.Version < 0 || r.Policy == nil {
		invalid()
		return 0, schedquality.Policy{}, false
	}
	p := r.Policy
	if p.Enabled == nil || p.TTFTSeconds == nil || p.MaxErrorRate == nil || p.MinCacheRatio == nil || p.TakeoverIdleSeconds == nil {
		invalid()
		return 0, schedquality.Policy{}, false
	}
	value := schedquality.Policy{Enabled: *p.Enabled, TTFTSeconds: *p.TTFTSeconds, MaxErrorRate: *p.MaxErrorRate, MinCacheRatio: *p.MinCacheRatio, TakeoverIdleSeconds: *p.TakeoverIdleSeconds}
	if e := service.ValidateSchedQualityPolicy(value); e != nil {
		response.ErrorFrom(c, e)
		return 0, value, false
	}
	return *r.Version, value, true
}
func (h *SchedQualityHandler) Order(c *gin.Context) {
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	v, e := h.service.Order(c.Request.Context(), id, c.Query("model"))
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) GroupsSummary(c *gin.Context) {
	middleware.SkipAudit(c)
	var r struct {
		GroupIDs []int64 `json:"group_ids" binding:"required,dive,gt=0"`
	}
	if e := c.ShouldBindJSON(&r); e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	v, e := h.service.GroupsSummary(c.Request.Context(), r.GroupIDs)
	schedQualityReply(c, v, e)
}
func (h *SchedQualityHandler) PreviewPolicy(c *gin.Context) {
	middleware.SkipAudit(c)
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	version, p, ok := schedQualityPolicyRequest(c)
	if !ok {
		return
	}
	v, e := h.service.PreviewPolicy(c.Request.Context(), id, version, p)
	schedQualityReply(c, v, e)
}

var _ SchedQualityAdmin = (*service.SchedAdminService)(nil)

func (h *SchedQualityHandler) Decisions(c *gin.Context) {
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 200 {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_DECISION_PAGE", "limit must be 1..200"))
		return
	}
	before := int64(0)
	if raw, ok := c.GetQuery("before_id"); ok {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before <= 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("INVALID_DECISION_PAGE", "before_id must be positive"))
			return
		}
	}
	rows, err := h.service.Decisions(c.Request.Context(), id, limit, before)
	schedQualityReply(c, rows, err)
}
func (h *SchedQualityHandler) TTFTRule(c *gin.Context) {
	v, err := h.service.TTFTRule(c.Request.Context())
	schedQualityReply(c, v, err)
}
func (h *SchedQualityHandler) UpdateTTFTRule(c *gin.Context) {
	middleware.SetAuditAction(c, "sched_quality.ttft_rule")
	var r struct {
		Version    *int64   `json:"expected_version"`
		Confidence *float64 `json:"confidence"`
		Tolerance  *float64 `json:"tolerance"`
		PeerMargin *float64 `json:"peer_margin"`
	}
	invalid := func(message string) { response.ErrorFrom(c, infraerrors.BadRequest("INVALID_TTFT_RULE", message)) }
	if err := c.ShouldBindJSON(&r); err != nil {
		invalid("expected_version, confidence, tolerance and peer_margin must be finite numbers")
		return
	}
	if r.Version == nil || *r.Version < 0 {
		invalid("expected_version must be nonnegative")
		return
	}
	if r.Confidence == nil {
		invalid("confidence is required")
		return
	}
	if r.Tolerance == nil {
		invalid("tolerance is required")
		return
	}
	if r.PeerMargin == nil {
		invalid("peer_margin is required")
		return
	}
	p := schedquality.RuleBParams{Confidence: *r.Confidence, Tolerance: *r.Tolerance, PeerMargin: *r.PeerMargin}
	if err := p.Validate(); err != nil {
		invalid(err.Error())
		return
	}
	v, err := h.service.UpdateTTFTRule(c.Request.Context(), *r.Version, p, schedQualityOperator(c))
	schedQualityReply(c, v, err)
}

func (h *SchedQualityHandler) GroupPerformance(c *gin.Context) {
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	data, err := h.service.GroupPerformance(c.Request.Context(), id)
	schedQualityReply(c, data, err)
}

func (h *SchedQualityHandler) AccountPerformance(c *gin.Context) {
	id, ok := schedQualityID(c)
	if !ok {
		return
	}
	data, err := h.service.AccountPerformance(c.Request.Context(), id)
	schedQualityReply(c, data, err)
}

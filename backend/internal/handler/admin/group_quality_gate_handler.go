package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// GroupQualityGateHandler serves the quality-gate policy and status APIs.
type GroupQualityGateHandler struct {
	svc *service.GroupQualityGateService
}

func NewGroupQualityGateHandler(svc *service.GroupQualityGateService) *GroupQualityGateHandler {
	return &GroupQualityGateHandler{svc: svc}
}

func (h *GroupQualityGateHandler) Policies(c *gin.Context) {
	if h == nil || h.svc == nil {
		response.Error(c, http.StatusServiceUnavailable, "group quality gate unavailable")
		return
	}
	out, err := h.svc.Policies(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

type groupQualityGatePolicyRequest struct {
	ExpectedVersion int `json:"expected_version"`
	Policy          struct {
		V3 *service.GroupQualityGateV3Policy `json:"v3"`
	} `json:"policy"`
}

func (h *GroupQualityGateHandler) UpdatePolicy(c *gin.Context) {
	if h == nil || h.svc == nil {
		response.Error(c, http.StatusServiceUnavailable, "group quality gate unavailable")
		return
	}
	groupID, err := strconv.ParseInt(c.Param("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var req groupQualityGatePolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	out, err := h.svc.UpdatePolicy(c.Request.Context(), groupID, req.ExpectedVersion, service.GroupQualityGatePolicy{V3: req.Policy.V3})
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{"version": out.Version, "policy": out.Groups[strconv.FormatInt(groupID, 10)]})
}

func (h *GroupQualityGateHandler) Status(c *gin.Context) {
	if h == nil || h.svc == nil {
		response.Error(c, http.StatusServiceUnavailable, "group quality gate unavailable")
		return
	}
	var groupID *int64
	if raw := c.Query("group_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid group ID")
			return
		}
		groupID = &id
	}
	out, err := h.svc.Status(c.Request.Context(), groupID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

func (h *GroupQualityGateHandler) ProfitControlPreview(c *gin.Context) {
	if h == nil || h.svc == nil {
		response.Error(c, http.StatusServiceUnavailable, "group quality gate unavailable")
		return
	}
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var margin *float64
	if raw, ok := c.GetQuery("margin"); ok && raw != "" {
		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			response.BadRequest(c, "Invalid margin")
			return
		}
		margin = &value
	}
	out, err := h.svc.ProfitControlPreview(c.Request.Context(), groupID, margin)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

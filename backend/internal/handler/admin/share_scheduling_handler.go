package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// shareSchedulingMaxBody bounds policy documents: 5000 account rules fit
// comfortably.
const shareSchedulingMaxBody = 2 << 20

// ShareSchedulingHandler serves /api/v1/admin/share-scheduling.
type ShareSchedulingHandler struct {
	svc *service.ShareSchedulingService
}

// NewShareSchedulingHandler creates the handler.
func NewShareSchedulingHandler(svc *service.ShareSchedulingService) *ShareSchedulingHandler {
	return &ShareSchedulingHandler{svc: svc}
}

// decodeStrictJSON decodes one JSON document, rejecting unknown fields and
// trailing data so a typo never silently falls back to a default.
func decodeStrictJSON(c *gin.Context, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, shareSchedulingMaxBody))
	if err != nil {
		response.BadRequest(c, "Request body too large or unreadable")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Invalid request: trailing data")
		return false
	}
	return true
}

func shareGroupID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid group id")
		return 0, false
	}
	return id, true
}

// AccountRules returns an account's rules in its share scheduling groups.
// GET /api/v1/admin/share-scheduling/accounts/:id/rules
func (h *ShareSchedulingHandler) AccountRules(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account id")
		return
	}
	rules, err := h.svc.AccountRules(c.Request.Context(), accountID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, rules)
}

// GetSettings returns the global settings.
// GET /api/v1/admin/share-scheduling/settings
func (h *ShareSchedulingHandler) GetSettings(c *gin.Context) {
	response.Success(c, h.svc.Settings())
}

// UpdateSettings merges the given fields into the current settings.
// PUT /api/v1/admin/share-scheduling/settings
func (h *ShareSchedulingHandler) UpdateSettings(c *gin.Context) {
	settings := h.svc.Settings()
	if !decodeStrictJSON(c, &settings) {
		return
	}
	updated, err := h.svc.UpdateSettings(c.Request.Context(), settings)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, updated)
}

// ListGroups lists active groups with their scheduler and summary.
// GET /api/v1/admin/share-scheduling/groups
func (h *ShareSchedulingHandler) ListGroups(c *gin.Context) {
	groups, err := h.svc.ListGroups(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, groups)
}

// GetPolicy returns a group's policy and members.
// GET /api/v1/admin/share-scheduling/groups/:id/policy
func (h *ShareSchedulingHandler) GetPolicy(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	view, err := h.svc.GetPolicy(c.Request.Context(), groupID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, view)
}

type sharePolicyUpdateRequest struct {
	ExpectedVersion *int64          `json:"expected_version"`
	Policy          json.RawMessage `json:"policy"`
}

// UpdatePolicy stores a policy when expected_version still matches.
// PUT /api/v1/admin/share-scheduling/groups/:id/policy
func (h *ShareSchedulingHandler) UpdatePolicy(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	var req sharePolicyUpdateRequest
	if !decodeStrictJSON(c, &req) {
		return
	}
	if req.ExpectedVersion == nil || *req.ExpectedVersion < 0 || len(req.Policy) == 0 {
		response.BadRequest(c, "expected_version and policy are required")
		return
	}
	var updatedBy int64
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		updatedBy = subject.UserID
	}
	view, err := h.svc.SavePolicy(c.Request.Context(), groupID, *req.ExpectedVersion, req.Policy, updatedBy)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, view)
}

type sharePolicyPreviewRequest struct {
	Policy json.RawMessage `json:"policy"`
}

// Preview evaluates a proposed policy without saving it.
// POST /api/v1/admin/share-scheduling/groups/:id/preview
func (h *ShareSchedulingHandler) Preview(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	var req sharePolicyPreviewRequest
	if !decodeStrictJSON(c, &req) {
		return
	}
	if len(req.Policy) == 0 {
		response.BadRequest(c, "policy is required")
		return
	}
	preview, err := h.svc.Preview(c.Request.Context(), groupID, req.Policy)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, preview)
}

// Explain shows the full plan of one request without serving it.
// POST /api/v1/admin/share-scheduling/groups/:id/explain
func (h *ShareSchedulingHandler) Explain(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	var req service.ShareSchedulingExplainInput
	if !decodeStrictJSON(c, &req) {
		return
	}
	req.Model, req.SessionHash = strings.TrimSpace(req.Model), strings.TrimSpace(req.SessionHash)
	explanation, err := h.svc.Explain(c.Request.Context(), groupID, req)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, explanation)
}

// Report returns target and actual shares with decision, cache and latency
// statistics. window is a Go duration such as 1h, at most 24h.
// GET /api/v1/admin/share-scheduling/groups/:id/report?window=1h
func (h *ShareSchedulingHandler) Report(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	window := time.Hour
	if raw := strings.TrimSpace(c.Query("window")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < time.Minute || parsed > 24*time.Hour {
			response.BadRequest(c, "window must be a duration between 1m and 24h")
			return
		}
		window = parsed
	}
	report, err := h.svc.Report(c.Request.Context(), groupID, window)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, report)
}

// Decisions returns the latest decision samples.
// GET /api/v1/admin/share-scheduling/groups/:id/decisions?limit=100
func (h *ShareSchedulingHandler) Decisions(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 500 {
			response.BadRequest(c, "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}
	decisions, err := h.svc.RecentDecisions(c.Request.Context(), groupID, limit)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, decisions)
}

type shareRebalanceRequest struct {
	Mode string `json:"mode"`
}

// Rebalance clears session bindings (cold ones by default).
// POST /api/v1/admin/share-scheduling/groups/:id/rebalance
func (h *ShareSchedulingHandler) Rebalance(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	var req shareRebalanceRequest
	if c.Request.ContentLength != 0 && !decodeStrictJSON(c, &req) {
		return
	}
	result, err := h.svc.Rebalance(c.Request.Context(), groupID, strings.TrimSpace(req.Mode))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, result)
}

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

// shareSchedulingMaxBody bounds request bodies; a policy document of an
// older admin page with 5000 account rules fits comfortably.
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

// AccountRules returns an account's expected shares in its share groups.
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

// shareSettingsRequest is the settings update. The switches of 0.3.0–0.4.3
// are accepted from older admin pages and ignored.
type shareSettingsRequest struct {
	Enabled               *bool           `json:"enabled"`
	CacheAttribution      json.RawMessage `json:"cache_attribution"`
	AttributionSampleRate json.RawMessage `json:"attribution_sample_rate"`
	DecisionSamples       json.RawMessage `json:"decision_samples"`
}

// UpdateSettings sets the master switch; an omitted switch keeps its value.
// PUT /api/v1/admin/share-scheduling/settings
func (h *ShareSchedulingHandler) UpdateSettings(c *gin.Context) {
	var req shareSettingsRequest
	if !decodeStrictJSON(c, &req) {
		return
	}
	enabled := h.svc.Settings().Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updated, err := h.svc.UpdateSettings(c.Request.Context(), enabled)
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

// sharePolicyRequest is a save or preview request: the scheduler and
// session keeping, or the whole policy document an admin page of
// 0.3.0–0.4.3 submits (design §7.4).
type sharePolicyRequest struct {
	Scheduler       *string         `json:"scheduler"`
	SessionKeep     *bool           `json:"session_keep"`
	ExpectedVersion *int64          `json:"expected_version"`
	Policy          json.RawMessage `json:"policy"`
}

// UpdatePolicy stores the scheduler and session keeping when
// expected_version still matches.
// PUT /api/v1/admin/share-scheduling/groups/:id/policy
func (h *ShareSchedulingHandler) UpdatePolicy(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	var req sharePolicyRequest
	if !decodeStrictJSON(c, &req) {
		return
	}
	if req.ExpectedVersion == nil || *req.ExpectedVersion < 0 {
		response.BadRequest(c, "expected_version is required")
		return
	}
	update, err := service.ParseSharePolicyUpdate(req.Scheduler, req.SessionKeep, req.Policy)
	if response.ErrorFrom(c, err) {
		return
	}
	var updatedBy int64
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		updatedBy = subject.UserID
	}
	view, err := h.svc.SavePolicy(c.Request.Context(), groupID, *req.ExpectedVersion, update, updatedBy)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, view)
}

// Preview evaluates a proposed scheduler and session keeping without saving
// them; expected_version is accepted and ignored.
// POST /api/v1/admin/share-scheduling/groups/:id/preview
func (h *ShareSchedulingHandler) Preview(c *gin.Context) {
	groupID, ok := shareGroupID(c)
	if !ok {
		return
	}
	var req sharePolicyRequest
	if !decodeStrictJSON(c, &req) {
		return
	}
	update, err := service.ParseSharePolicyUpdate(req.Scheduler, req.SessionKeep, req.Policy)
	if response.ErrorFrom(c, err) {
		return
	}
	preview, err := h.svc.Preview(c.Request.Context(), groupID, update)
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

// Report returns the health figures of the window and of the same window
// before the switch to share scheduling. window is a Go duration such as 1h,
// between 1m and 24h.
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

// RetiredShareSchedulingEndpoint answers the removed rebalance and decision
// sample routes with 410, so admin pages of 0.3.0–0.4.3 get an explicit
// answer instead of a 404 (design §7.9).
func RetiredShareSchedulingEndpoint(c *gin.Context) {
	response.ErrorWithDetails(c, http.StatusGone,
		"This share scheduling endpoint has been retired; sessions follow account priorities and load factors",
		"SHARE_SCHEDULING_ENDPOINT_RETIRED", nil)
}

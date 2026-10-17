package admin

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

// ResilienceHandler serves the gateway resilience administration endpoints.
type ResilienceHandler struct {
	svc *service.ResilienceService
}

// NewResilienceHandler creates the handler.
func NewResilienceHandler(svc *service.ResilienceService) *ResilienceHandler {
	return &ResilienceHandler{svc: svc}
}

// GetSettings returns the effective gateway resilience settings.
// GET /api/v1/admin/settings/gateway-resilience
func (h *ResilienceHandler) GetSettings(c *gin.Context) {
	response.Success(c, gin.H{"settings": h.svc.GetSettings(), "defaults": resilience.Defaults()})
}

// UpdateSettings replaces the settings. Omitted fields keep their current
// value; unknown fields are rejected.
// PUT /api/v1/admin/settings/gateway-resilience
func (h *ResilienceHandler) UpdateSettings(c *gin.Context) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, shareSchedulingMaxBody))
	if err != nil {
		response.BadRequest(c, "Request body too large or unreadable")
		return
	}
	next := h.svc.GetSettings()
	// A map in the body replaces the current one (decoding would merge into
	// it, so keys could never be removed); slices are replaced anyway.
	if gjson.GetBytes(body, "budget.model_overrides").Exists() {
		next.Budget.ModelOverrides = nil
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if !decodeStrictJSON(c, &next) {
		return
	}
	updated, err := h.svc.UpdateSettings(c.Request.Context(), next)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{"settings": updated, "defaults": resilience.Defaults()})
}

// ListUndelivered lists requests whose client left before any output.
// GET /api/v1/admin/resilience/undelivered?page=&page_size=&user_id=&api_key_id=&group_id=&since=&until=
func (h *ResilienceHandler) ListUndelivered(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	filter := service.UndeliveredFilter{}
	filter.UserID, _ = strconv.ParseInt(c.Query("user_id"), 10, 64)
	filter.APIKeyID, _ = strconv.ParseInt(c.Query("api_key_id"), 10, 64)
	filter.GroupID, _ = strconv.ParseInt(c.Query("group_id"), 10, 64)
	for key, dst := range map[string]*time.Time{"since": &filter.Since, "until": &filter.Until} {
		if raw := c.Query(key); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				response.BadRequest(c, "Invalid "+key+" (RFC3339 expected)")
				return
			}
			*dst = t
		}
	}
	items, result, err := h.svc.ListUndelivered(c.Request.Context(), filter, pagination.PaginationParams{Page: page, PageSize: pageSize})
	if response.ErrorFrom(c, err) {
		return
	}
	if items == nil {
		items = []service.UndeliveredRequest{}
	}
	response.Paginated(c, items, result.Total, result.Page, result.PageSize)
}

// Stats returns this instance's resilience counters over a window (at most
// one hour).
// GET /api/v1/admin/resilience/stats?window=15m
func (h *ResilienceHandler) Stats(c *gin.Context) {
	window := time.Hour
	if raw := c.Query("window"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			response.BadRequest(c, "Invalid window (Go duration expected, e.g. 15m)")
			return
		}
		window = d
	}
	response.Success(c, h.svc.Stats(window))
}

// Explain shows how gateway resilience sees a group and optionally one
// session: penalty tiers, the session's failures and avoid set, the
// last-resort pick and the availability fallback decision. Read-only; it is
// the Explain of groups on the original scheduler (design §4).
// GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=
func (h *ResilienceHandler) Explain(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	out, err := h.svc.Explain(c.Request.Context(), groupID, strings.TrimSpace(c.Query("session_hash")), strings.TrimSpace(c.Query("model")))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

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
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

const resilienceMaxBody = 2 << 20

// decodeStrictJSON decodes one JSON document, rejecting unknown fields and
// trailing data so a typo never silently falls back to a default.
func decodeStrictJSON(c *gin.Context, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, resilienceMaxBody))
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
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, resilienceMaxBody))
	if err != nil {
		response.BadRequest(c, "Request body too large or unreadable")
		return
	}
	if err := checkCollabPatch(body); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	next := h.svc.GetSettings()
	// A map in the body replaces the current one (decoding would merge into
	// it, so keys could never be removed); slices are replaced anyway.
	if gjson.GetBytes(body, "budget.model_overrides").Exists() {
		next.Budget.ModelOverrides = nil
	}
	namesGiven, err := checkFCOCompatPatch(body)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if namesGiven {
		// Decoding into the current list would reuse its elements.
		next.FCOCompat.AdminNames = nil
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

// checkFCOCompatPatch rejects explicit fco_compat values that decoding over
// the current settings would otherwise hide: a JSON null leaves the current
// value in place and an empty mode would be filled with the default. Omitted
// fields keep their current value like every other section. The probes use
// encoding/json so key matching and repeated keys resolve as in the strict
// decode that follows (repeated sections merge, the last value of a field
// wins); documents that do not decode are left to that decode. It reports
// whether admin_names was given.
func checkFCOCompatPatch(body []byte) (bool, error) {
	invalid := func(problem string) error {
		return infraerrors.BadRequest("GATEWAY_RESILIENCE_SETTINGS_INVALID", "invalid gateway resilience settings: "+problem)
	}
	var whole struct {
		FCOCompat json.RawMessage `json:"fco_compat"`
	}
	if json.Unmarshal(body, &whole) != nil || len(whole.FCOCompat) == 0 {
		return false, nil
	}
	if isJSONNull(whole.FCOCompat) {
		return false, invalid("fco_compat must be an object")
	}
	var fields struct {
		FCOCompat struct {
			Mode       json.RawMessage `json:"mode"`
			AdminNames json.RawMessage `json:"admin_names"`
		} `json:"fco_compat"`
	}
	if json.Unmarshal(body, &fields) != nil {
		return false, nil
	}
	section := fields.FCOCompat
	if len(section.Mode) > 0 {
		var mode any
		_ = json.Unmarshal(section.Mode, &mode)
		if s, ok := mode.(string); !ok || !resilience.ValidFCOCompatMode(s) {
			return false, invalid("fco_compat.mode must be off, reactive or proactive")
		}
	}
	if len(section.AdminNames) == 0 {
		return false, nil
	}
	var names []any
	if isJSONNull(section.AdminNames) || json.Unmarshal(section.AdminNames, &names) != nil {
		return true, invalid("fco_compat.admin_names must be an array of strings")
	}
	for _, name := range names {
		if _, ok := name.(string); !ok {
			return true, invalid("fco_compat.admin_names must be an array of strings")
		}
	}
	return true, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
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
// GET /api/v1/admin/resilience/groups/:id/explain?session_hash=&model=&protocol=responses&stream=false
func (h *ResilienceHandler) Explain(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	query := c.Request.URL.Query()
	if query.Has("protocol") || query.Has("stream") {
		protocol, stream := query.Get("protocol"), query.Get("stream")
		if len(query["protocol"]) != 1 || len(query["stream"]) != 1 ||
			(protocol != resilience.CapabilityProtocolResponses && protocol != resilience.CapabilityProtocolChat) ||
			(stream != "true" && stream != "false") {
			response.BadRequest(c, "protocol (responses or chat) and stream (true or false) must be provided together")
			return
		}
		c.Request = c.Request.WithContext(resilience.WithCapabilityRequest(c.Request.Context(), protocol, stream == "true"))
	}
	out, err := h.svc.Explain(c.Request.Context(), groupID, strings.TrimSpace(c.Query("session_hash")), strings.TrimSpace(c.Query("model")))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, out)
}

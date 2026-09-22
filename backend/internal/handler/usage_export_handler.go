package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
)

type exportUserReader interface {
	GetByID(context.Context, int64) (*service.User, error)
}
type UsageExportHandler struct {
	engine   *usageexport.Engine
	user     *UsageHandler
	admin    *admin.UsageHandler
	auth     *service.AuthService
	users    exportUserReader
	sessions service.RefreshTokenCache
	settings *service.SettingService
}

func NewUsageExportHandler(e *usageexport.Engine, u *UsageHandler, a *admin.UsageHandler, auth *service.AuthService, users *service.UserService, sessions service.RefreshTokenCache, settings *service.SettingService) *UsageExportHandler {
	return &UsageExportHandler{e, u, a, auth, users, sessions, settings}
}
func exportScope(c *gin.Context) string {
	if strings.Contains(c.FullPath(), "/admin/") {
		return "admin"
	}
	return "user"
}
func exportOwner(c *gin.Context) int64 {
	s, _ := middleware.GetAuthSubjectFromContext(c)
	return s.UserID
}
func exportError(c *gin.Context, err error) {
	var f *usageexport.Fault
	if errors.As(err, &f) {
		if f.Retry > 0 {
			c.Header("Retry-After", strconv.Itoa(f.Retry))
		}
		c.JSON(f.Status, gin.H{"code": f.Code, "message": f.Code})
		return
	}
	c.JSON(500, gin.H{"code": "EXPORT_INTERNAL_ERROR", "message": "Unable to process export"})
}
func (h *UsageExportHandler) Create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(c.Request.Body).Decode(&raw); err != nil {
		response.BadRequest(c, "Invalid export parameters")
		return
	}
	allowed := " start_date end_date api_key_id model group_id request_type stream billing_type billing_mode sort_by sort_order timezone language native_compaction_v2 "
	scope := exportScope(c)
	if scope == "admin" {
		allowed += " user_id account_id request_id native_compaction_v2 upstream_model_mismatch "
	}
	q := url.Values{}
	for k, v := range raw {
		if k == "page" || k == "page_size" || k == "exact_total" {
			continue
		}
		if !strings.Contains(allowed, " "+k+" ") {
			response.BadRequest(c, "Unsupported export parameter: "+k)
			return
		}
		if string(v) == "null" {
			continue
		}
		var value any
		d := json.NewDecoder(strings.NewReader(string(v)))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			response.BadRequest(c, "Invalid export value")
			return
		}
		switch x := value.(type) {
		case string:
			q.Set(k, x)
		case bool:
			q.Set(k, strconv.FormatBool(x))
		case json.Number:
			q.Set(k, x.String())
		default:
			response.BadRequest(c, "Invalid export value")
			return
		}
	}
	zone := q.Get("timezone")
	if zone == "" {
		zone = "UTC"
	}
	if _, err := time.LoadLocation(zone); err != nil {
		response.BadRequest(c, "Invalid timezone")
		return
	}
	lang := q.Get("language")
	if lang == "" {
		lang = c.GetHeader("Accept-Language")
	}
	if strings.HasPrefix(lang, "zh") {
		lang = "zh"
	} else {
		lang = "en"
	}
	sortBy := q.Get("sort_by")
	if sortBy == "" {
		sortBy = "created_at"
	}
	if sortBy != "created_at" && sortBy != "model" && sortBy != "id" {
		response.BadRequest(c, "Invalid export sort")
		return
	}
	order := q.Get("sort_order")
	if order == "" {
		order = "desc"
	}
	if order != "asc" && order != "desc" {
		response.BadRequest(c, "Invalid export sort order")
		return
	}
	q.Set("timezone", zone)
	c.Request.URL.RawQuery = q.Encode()
	o := usageexport.Options{SortBy: sortBy, SortOrder: order, Timezone: zone, Language: lang, Version: 1, Format: "csv"}
	if scope == "admin" {
		_, filters, ok := h.admin.ParseExportFilters(c)
		if !ok {
			return
		}
		filters.ExactTotal = false
		o.Filters = filters
		o.Format = "xlsx"
	} else {
		parsed, ok := h.user.parseUserUsageFilters(c, false)
		if !ok {
			return
		}
		o.Filters = parsed.Filters
	}
	if o.Filters.StartTime != nil && o.Filters.EndTime != nil && !o.Filters.StartTime.Before(*o.Filters.EndTime) {
		response.BadRequest(c, "Invalid date range")
		return
	}
	t, err := h.engine.Create(c.Request.Context(), exportOwner(c), scope, c.GetHeader("Idempotency-Key"), o)
	if err != nil {
		exportError(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "data": t.Public()})
}
func (h *UsageExportHandler) List(c *gin.Context) {
	owner := exportOwner(c)
	if err := h.engine.Rate(c.Request.Context(), owner, "status", h.engine.Config.StatusRPM); err != nil {
		exportError(c, err)
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.engine.List(c.Request.Context(), owner, exportScope(c), page, size)
	if err != nil {
		exportError(c, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, t := range items {
		out = append(out, t.Public())
	}
	response.Paginated(c, out, total, page, size)
}
func (h *UsageExportHandler) Get(c *gin.Context) {
	owner := exportOwner(c)
	if err := h.engine.Rate(c.Request.Context(), owner, "status", h.engine.Config.StatusRPM); err != nil {
		exportError(c, err)
		return
	}
	t, err := h.engine.Get(c.Request.Context(), owner, exportScope(c), c.Param("id"))
	if err != nil {
		exportError(c, err)
		return
	}
	response.Success(c, t.Public())
}
func (h *UsageExportHandler) Cancel(c *gin.Context) {
	if err := h.engine.Cancel(c.Request.Context(), exportOwner(c), exportScope(c), c.Param("id")); err != nil {
		exportError(c, err)
		return
	}
	response.Success(c, gin.H{})
}
func (h *UsageExportHandler) Delete(c *gin.Context) {
	if err := h.engine.Delete(c.Request.Context(), exportOwner(c), exportScope(c), c.Param("id")); err != nil {
		exportError(c, err)
		return
	}
	response.Success(c, gin.H{})
}
func (h *UsageExportHandler) Ticket(c *gin.Context) {
	claims, err := h.auth.ValidateToken(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if err != nil || claims.SessionID == "" || claims.ExpiresAt == nil || claims.UserID != exportOwner(c) {
		response.Unauthorized(c, "A current login session is required")
		return
	}
	identity := usageexport.TicketIdentity{Owner: claims.UserID, Session: claims.SessionID, TokenVersion: claims.TokenVersion, Binding: claims.BindingHash, TokenExpires: claims.ExpiresAt.Unix()}
	token, err := h.engine.Ticket(c.Request.Context(), identity.Owner, exportScope(c), c.Param("id"), identity)
	if err != nil {
		exportError(c, err)
		return
	}
	// An HttpOnly path-scoped cookie keeps the capability out of URLs and access logs.
	target := strings.TrimSuffix(c.Request.URL.Path, "/download-ticket") + "/download"
	http.SetCookie(c.Writer, &http.Cookie{Name: "usage_export_ticket", Value: token, Path: target, MaxAge: 60, HttpOnly: true, Secure: c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode})
	response.Success(c, gin.H{"url": target, "expires_in": 60})
}
func (h *UsageExportHandler) Download(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	token, _ := c.Cookie("usage_export_ticket")
	t, identity, err := h.engine.Redeem(c.Request.Context(), exportScope(c), c.Param("id"), token)
	if err != nil {
		exportError(c, err)
		return
	}
	if !h.authorizeDownload(c, t, identity) {
		return
	}
	release, err := h.engine.DownloadSlot(c.Request.Context(), identity.Owner)
	if err != nil {
		exportError(c, err)
		return
	}
	defer release()
	t, body, err := h.engine.OpenDownload(c.Request.Context(), identity.Owner, exportScope(c), t.ID)
	if err != nil {
		exportError(c, err)
		return
	}
	defer func() { _ = body.Close() }()
	contentType := "text/csv; charset=utf-8"
	if t.Options.Format == "xlsx" {
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="usage_%s.%s"`, t.ID, t.Options.Format))
	c.Header("Content-Length", strconv.FormatInt(t.Size, 10))
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(200)
	sent, copyErr := io.CopyN(c.Writer, body, t.Size)
	slog.Info("usage export download", "task_id", t.ID, "bytes", sent, "complete", copyErr == nil)
}

func (h *UsageExportHandler) authorizeDownload(c *gin.Context, t *usageexport.Task, identity usageexport.TicketIdentity) bool {
	user, err := h.users.GetByID(c.Request.Context(), identity.Owner)
	if err != nil || user == nil || !user.IsActive() || user.TokenVersion != identity.TokenVersion || time.Now().Unix() >= identity.TokenExpires || (t.Scope == "admin" && user.Role != service.RoleAdmin) {
		response.Unauthorized(c, "Export session is no longer valid")
		return false
	}
	if h.settings != nil {
		if t.Scope == "admin" {
			allowed, er := h.settings.IsAdminComplianceAcknowledged(c.Request.Context(), identity.Owner)
			if er != nil || !allowed {
				response.Forbidden(c, "Administrator acknowledgement required")
				return false
			}
		} else if h.settings.IsBackendModeEnabled(c.Request.Context()) && user.Role != service.RoleAdmin {
			response.Forbidden(c, "User panel disabled")
			return false
		}
	}
	hashes, err := h.sessions.GetFamilyTokenHashes(c.Request.Context(), identity.Session)
	valid := false
	if err == nil {
		for _, hash := range hashes {
			data, er := h.sessions.GetRefreshToken(c.Request.Context(), hash)
			if er == nil && data != nil && data.UserID == identity.Owner && data.FamilyID == identity.Session && data.TokenVersion == identity.TokenVersion && time.Now().Before(data.ExpiresAt) {
				valid = true
				break
			}
		}
	}
	if !valid {
		response.Unauthorized(c, "Export session has ended")
		return false
	}
	if h.settings != nil && h.settings.IsSessionBindingEnabled(c.Request.Context()) && identity.Binding != "" {
		binding := service.SessionBindingFromContext(c.Request.Context())
		if binding == nil || binding.Hash() != identity.Binding {
			response.Unauthorized(c, "Export session changed")
			return false
		}
	}
	return true
}

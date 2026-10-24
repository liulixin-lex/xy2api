package admin

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type CachePolicyHandler struct {
	policies *service.CachePolicyService
	audit    *service.AuditLogService
	version  string
}

func NewCachePolicyHandler(policies *service.CachePolicyService, audit *service.AuditLogService, version string) *CachePolicyHandler {
	return &CachePolicyHandler{policies, audit, version}
}
func cachePolicyResponseError(c *gin.Context, err error) {
	var p *service.CachePolicyError
	if errors.As(err, &p) {
		response.ErrorWithDetails(c, p.Status, p.Message, p.Reason, p.Metadata)
		return
	}
	response.Error(c, 500, "cache policy storage unavailable")
}
func (h *CachePolicyHandler) List(c *gin.Context) {
	v, err := h.policies.List(c.Request.Context(), h.version)
	if err != nil {
		cachePolicyResponseError(c, err)
		return
	}
	response.Success(c, v)
}
func (h *CachePolicyHandler) Get(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := h.policies.Get(c.Request.Context(), key)
		if err != nil {
			cachePolicyResponseError(c, err)
			return
		}
		response.Success(c, v)
	}
}
func (h *CachePolicyHandler) Validate(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		middleware.SkipAudit(c)
		w, errs := service.DecodeCachePolicyWrite(c.Request.Body)
		v, err := h.policies.Validate(c.Request.Context(), key, w, errs)
		if err != nil {
			cachePolicyResponseError(c, err)
			return
		}
		response.Success(c, v)
	}
}
func (h *CachePolicyHandler) Put(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		w, errs := service.DecodeCachePolicyWrite(c.Request.Body)
		actor := service.CachePolicyActor{Email: c.GetString(middleware.ContextKeyAuthEmail)}
		if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
			actor.UserID = subject.UserID
		}
		middleware.SetAuditExtra(c, map[string]any{"policy_key": key})
		v, diff, err := h.policies.Update(c.Request.Context(), key, w, errs, actor)
		if err != nil {
			cachePolicyResponseError(c, err)
			return
		}
		fields := make([]string, 0, len(diff))
		for _, d := range diff {
			fields = append(fields, d.Field)
		}
		middleware.SetAuditExtra(c, map[string]any{"rev_before": v.Stored.Rev - 1, "rev_after": v.Stored.Rev, "changed_fields": strings.Join(fields, ",")})
		slog.Info("cache policy updated", "audit", true, "user_id", actor.UserID, "policy_key", key, "rev_before", v.Stored.Rev-1, "rev_after", v.Stored.Rev, "changed_fields", fields)
		response.Success(c, v)
	}
}
func (h *CachePolicyHandler) History(c *gin.Context) {
	q := service.CachePolicyHistoryQuery{Key: c.Query("key"), Limit: 20}
	invalid := func() {
		response.ErrorWithDetails(c, 400, "invalid cache policy history query", "CACHE_POLICY_INVALID", map[string]string{"errors": `[{"field":"","code":"malformed_body"}]`})
	}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			invalid()
			return
		}
		q.Limit = min(n, 100)
	}
	if raw := c.Query("before"); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			invalid()
			return
		}
		q.Before = &at
	}
	if raw := c.Query("after_rev"); raw != "" {
		rev, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || rev < 0 || q.Key == "" {
			invalid()
			return
		}
		q.AfterRev = &rev
	}
	v, err := h.policies.History(c.Request.Context(), h.audit, q)
	if err != nil {
		cachePolicyResponseError(c, err)
		return
	}
	response.Success(c, v)
}

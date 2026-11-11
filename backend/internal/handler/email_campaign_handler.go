package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type EmailCampaignHandler struct{ service *service.EmailCampaignService }

func NewEmailCampaignHandler(s *service.EmailCampaignService) *EmailCampaignHandler {
	return &EmailCampaignHandler{s}
}
func campaignRespond(c *gin.Context, v any, e error) {
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, v)
}
func campaignBind(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	if e := c.ShouldBindJSON(v); e != nil {
		response.ErrorFrom(c, service.ErrEmailCampaignInvalidInput)
		return false
	}
	return true
}
func campaignID(c *gin.Context) (int64, bool) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.ErrorFrom(c, service.ErrEmailCampaignInvalidInput)
		return 0, false
	}
	return id, true
}
func campaignCaller(c *gin.Context) (int64, bool) {
	sub, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || sub.UserID <= 0 {
		response.Unauthorized(c, "user not found in context")
		return 0, false
	}
	return sub.UserID, true
}
func campaignPage(c *gin.Context) (int, int) {
	p, _ := strconv.Atoi(c.Query("page"))
	n, _ := strconv.Atoi(c.Query("page_size"))
	return service.CampaignPagination(p, n)
}
func (h *EmailCampaignHandler) Settings(c *gin.Context) {
	v, e := h.service.Settings(c.Request.Context())
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) UpdateSettings(c *gin.Context) {
	var in service.EmailCampaignLimits
	if !campaignBind(c, &in) {
		return
	}
	v, e := h.service.UpdateSettings(c.Request.Context(), in)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) Preview(c *gin.Context) {
	var in struct {
		Audience service.EmailCampaignAudience `json:"audience"`
		Page     int                           `json:"page"`
		PageSize int                           `json:"page_size"`
	}
	if !campaignBind(c, &in) {
		return
	}
	v, e := h.service.Preview(c.Request.Context(), in.Audience, in.Page, in.PageSize)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) Render(c *gin.Context) {
	var in service.EmailCampaignContent
	if !campaignBind(c, &in) {
		return
	}
	id, ok := campaignCaller(c)
	if !ok {
		return
	}
	v, e := h.service.RenderForAdmin(c.Request.Context(), in, id, 0)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) TestSend(c *gin.Context) {
	var in struct {
		service.EmailCampaignContent
		To string `json:"to"`
	}
	if !campaignBind(c, &in) {
		return
	}
	to, e := h.service.TestSend(c.Request.Context(), in.EmailCampaignContent, in.To)
	campaignRespond(c, gin.H{"to": to}, e)
}
func (h *EmailCampaignHandler) Create(c *gin.Context) {
	var in service.EmailCampaignCreate
	if !campaignBind(c, &in) {
		return
	}
	id, ok := campaignCaller(c)
	if !ok {
		return
	}
	v, e := h.service.Create(c.Request.Context(), in, id)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) List(c *gin.Context) {
	p, n := campaignPage(c)
	v, e := h.service.List(c.Request.Context(), c.Query("status"), p, n)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) Detail(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	uid, ok := campaignCaller(c)
	if !ok {
		return
	}
	v, e := h.service.Detail(c.Request.Context(), id, uid)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) Recipients(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	p, n := campaignPage(c)
	v, e := h.service.Recipients(c.Request.Context(), id, c.Query("status"), c.Query("search"), p, n)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) Pause(c *gin.Context)       { h.action(c, "pause") }
func (h *EmailCampaignHandler) Resume(c *gin.Context)      { h.action(c, "resume") }
func (h *EmailCampaignHandler) Cancel(c *gin.Context)      { h.action(c, "cancel") }
func (h *EmailCampaignHandler) RetryFailed(c *gin.Context) { h.action(c, "retry-failed") }
func (h *EmailCampaignHandler) action(c *gin.Context, action string) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	v, n, e := h.service.Action(c.Request.Context(), id, action)
	if action == "retry-failed" {
		campaignRespond(c, gin.H{"campaign": v, "requeued": n}, e)
	} else {
		campaignRespond(c, v, e)
	}
}
func (h *EmailCampaignHandler) Suppressions(c *gin.Context) {
	p, n := campaignPage(c)
	v, e := h.service.Suppressions(c.Request.Context(), c.Query("search"), p, n)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) AddSuppressions(c *gin.Context) {
	var in struct {
		Entries []string `json:"entries"`
		Note    string   `json:"note"`
	}
	if !campaignBind(c, &in) {
		return
	}
	uid, ok := campaignCaller(c)
	if !ok {
		return
	}
	v, e := h.service.AddSuppressions(c.Request.Context(), in.Entries, in.Note, uid)
	campaignRespond(c, v, e)
}
func (h *EmailCampaignHandler) RemoveSuppression(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	v, e := h.service.RemoveSuppression(c.Request.Context(), id)
	campaignRespond(c, gin.H{"removed": v}, e)
}
func (h *EmailCampaignHandler) Unsubscribe(c *gin.Context) {
	token := c.Query("token")
	var claims service.EmailCampaignToken
	var e error
	if c.Request.Method == http.MethodPost {
		claims, e = h.service.Unsubscribe(c.Request.Context(), token)
	} else {
		claims, e = h.service.ParseUnsubscribe(c.Request.Context(), token)
	}
	status, body := http.StatusOK, ""
	if e != nil {
		if errors.Is(e, service.ErrEmailCampaignInvalidInput) {
			status = http.StatusBadRequest
			body = "<p>退订链接无效 / Invalid unsubscribe link</p>"
		} else {
			status = http.StatusServiceUnavailable
			body = "<p>暂时无法退订，请稍后重试 / Temporarily unavailable, please retry</p>"
		}
	} else if c.Request.Method == http.MethodPost {
		body = "<p>已退订 / Unsubscribed</p>"
	} else {
		parts := strings.SplitN(claims.Email, "@", 2)
		masked := "***"
		if len(parts) == 2 {
			masked += "@" + parts[1]
		}
		action := "/api/v1/email-campaigns/unsubscribe?token=" + url.QueryEscape(token)
		body = fmt.Sprintf(`<p>确认不再接收营销邮件 / Stop marketing emails: %s</p><form method="post" action="%s"><button type="submit">确认退订 / Unsubscribe</button></form>`, html.EscapeString(masked), html.EscapeString(action))
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	styleHash := sha256.Sum256([]byte(campaignUnsubscribeStyle))
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'sha256-"+base64.StdEncoding.EncodeToString(styleHash[:])+"'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	c.Data(status, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>营销邮件退订 / Unsubscribe</title><style>`+campaignUnsubscribeStyle+`</style></head><body><main><p class="site">`+html.EscapeString(h.service.SiteName(c.Request.Context()))+`</p><h1>营销邮件退订<br><span>Marketing email preferences</span></h1>`+body+`<p class="note">不影响验证码及系统通知。<br>Verification codes and system notifications are unaffected.</p></main></body></html>`))
}

// A fixed style hash permits this stylesheet without unsafe-inline or external assets.
const campaignUnsubscribeStyle = `html{font-family:system-ui,sans-serif;color:#172033;background:#f4f6fa}body{margin:0;min-height:100vh;display:grid;place-items:center}main{box-sizing:border-box;width:calc(100% - 32px);max-width:520px;margin:32px 0;padding:36px;border:1px solid #e2e7ef;border-radius:16px;background:#fff;box-shadow:0 8px 28px #17203312;line-height:1.7;overflow-wrap:anywhere}.site{font-weight:700;color:#4f46e5}h1{font-size:24px;line-height:1.4}h1 span{font-size:17px;font-weight:400;color:#526079}button{font:inherit;padding:12px 20px;border:0;border-radius:8px;background:#4f46e5;color:#fff;cursor:pointer}button:focus-visible{outline:3px solid #a5b4fc;outline-offset:3px}.note{margin-top:28px;font-size:13px;color:#526079}`

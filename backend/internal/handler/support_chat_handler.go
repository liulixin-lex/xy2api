package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type SupportChatHandler struct{ service *service.SupportChatService }

func NewSupportChatHandler(s *service.SupportChatService) *SupportChatHandler {
	return &SupportChatHandler{service: s}
}
func supportChatCaller(c *gin.Context) (int64, bool) {
	sub, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || sub.UserID <= 0 {
		response.Unauthorized(c, "user not found in context")
		return 0, false
	}
	return sub.UserID, true
}
func supportChatID(c *gin.Context) (int64, bool) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.BadRequest(c, "invalid chat id")
		return 0, false
	}
	return id, true
}
func supportChatRespond(c *gin.Context, v any, e error) {
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, v)
}

func (h *SupportChatHandler) Summary(c *gin.Context) {
	uid, ok := supportChatCaller(c)
	if !ok {
		return
	}
	v, e := h.service.UserSummary(c.Request.Context(), uid)
	supportChatRespond(c, v, e)
}
func (h *SupportChatHandler) Messages(c *gin.Context) {
	uid, ok := supportChatCaller(c)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.Query("after_id"), 10, 64)
	before, _ := strconv.ParseInt(c.Query("before_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	v, e := h.service.Messages(c.Request.Context(), service.SupportChatMessagePageInput{OwnerID: uid, AfterID: id, BeforeID: before, Limit: limit})
	supportChatRespond(c, v, e)
}
func (h *SupportChatHandler) Send(c *gin.Context) {
	uid, ok := supportChatCaller(c)
	if !ok {
		return
	}
	in, ok := parseSupportChatSend(c)
	if !ok {
		return
	}
	in.UserID = uid
	in.AuthorRole = service.SupportChatRoleUser
	in.AuthorUserID = uid
	m, dup, e := h.service.Send(c.Request.Context(), in)
	var chatID int64
	if m != nil {
		chatID = m.ChatID
	}
	supportChatRespond(c, gin.H{"message": m, "duplicate": dup, "chat_id": chatID}, e)
}
func (h *SupportChatHandler) Read(c *gin.Context) {
	uid, ok := supportChatCaller(c)
	if !ok {
		return
	}
	var req struct {
		LastMessageID int64 `json:"last_message_id"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "invalid request body")
		return
	} // chat is derived from the user, avoiding an ownership oracle
	n, e := h.service.ReadAsUser(c.Request.Context(), uid, req.LastMessageID)
	middleware.SkipAudit(c)
	supportChatRespond(c, gin.H{"read_message_id": n}, e)
}
func (h *SupportChatHandler) Image(c *gin.Context) {
	uid, ok := supportChatCaller(c)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.NotFound(c, "image not found")
		return
	}
	v, e := h.service.Image(c.Request.Context(), id, uid, false)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	writeSupportChatImage(c, v)
}

func (h *SupportChatHandler) AdminSummary(c *gin.Context) {
	v, e := h.service.AdminSummary(c.Request.Context())
	supportChatRespond(c, v, e)
}
func (h *SupportChatHandler) AdminList(c *gin.Context) {
	page, size := response.ParsePagination(c)
	items, total, e := h.service.AdminList(c.Request.Context(), service.SupportChatListFilter{Filter: c.DefaultQuery("filter", "waiting"), Keyword: c.Query("keyword"), Page: page, PageSize: size})
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Paginated(c, items, total, page, size)
}
func (h *SupportChatHandler) AdminGet(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	v, e := h.service.AdminGet(c.Request.Context(), id)
	supportChatRespond(c, v, e)
}
func (h *SupportChatHandler) AdminMessages(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(c.Query("after_id"), 10, 64)
	before, _ := strconv.ParseInt(c.Query("before_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	v, e := h.service.Messages(c.Request.Context(), service.SupportChatMessagePageInput{ChatID: id, AfterID: after, BeforeID: before, Limit: limit, Admin: true})
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	if after > 0 && len(v.Items) == 0 {
		middleware.SkipAudit(c)
	}
	supportChatRespond(c, v, e)
}
func (h *SupportChatHandler) AdminSend(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	in, ok := parseSupportChatSend(c)
	if !ok {
		return
	}
	in.ChatID = id
	in.AuthorRole = service.SupportChatRoleAdmin
	in.AuthorUserID = adminID(c)
	m, dup, e := h.service.Send(c.Request.Context(), in)
	supportChatRespond(c, gin.H{"message": m, "duplicate": dup}, e)
}
func (h *SupportChatHandler) AdminRead(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	var req struct {
		LastMessageID int64 `json:"last_message_id"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	n, e := h.service.Read(c.Request.Context(), id, true, req.LastMessageID)
	middleware.SkipAudit(c)
	supportChatRespond(c, gin.H{"read_message_id": n}, e)
}
func (h *SupportChatHandler) AdminImage(c *gin.Context) {
	imageID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || imageID <= 0 {
		response.NotFound(c, "image not found")
		return
	}
	v, e := h.service.Image(c.Request.Context(), imageID, 0, true)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	writeSupportChatImage(c, v)
}
func (h *SupportChatHandler) AdminStatus(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	var req struct {
		Status            string `json:"status"`
		LastSeenMessageID int64  `json:"last_seen_message_id"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	v, e := h.service.AdminStatus(c.Request.Context(), service.SupportChatStatusInput{ChatID: id, Status: req.Status, LastSeenMessageID: req.LastSeenMessageID})
	supportChatRespond(c, v, e)
}
func (h *SupportChatHandler) AdminBlock(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	var req struct {
		Blocked bool   `json:"blocked"`
		Reason  string `json:"reason"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	v, e := h.service.AdminBlock(c.Request.Context(), service.SupportChatBlockInput{ChatID: id, Blocked: req.Blocked, Reason: req.Reason})
	supportChatRespond(c, v, e)
}
func (h *SupportChatHandler) AdminDeleteMessage(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	mid, _ := strconv.ParseInt(c.Param("message_id"), 10, 64)
	v, e := h.service.AdminDeleteMessage(c.Request.Context(), id, mid)
	supportChatRespond(c, gin.H{"message": v}, e)
}
func (h *SupportChatHandler) AdminDelete(c *gin.Context) {
	id, ok := supportChatID(c)
	if !ok {
		return
	}
	e := h.service.AdminDeleteChat(c.Request.Context(), id)
	supportChatRespond(c, gin.H{"deleted": true}, e)
}

func adminID(c *gin.Context) int64 {
	sub, ok := middleware.GetAuthSubjectFromContext(c)
	if ok {
		return sub.UserID
	}
	return 0
}
func parseSupportChatSend(c *gin.Context) (service.SupportChatSendInput, bool) {
	var in service.SupportChatSendInput
	if c.Request.Method != "POST" {
		return in, false
	}
	if c.Request.ContentLength > service.SupportChatRequestMaxBytes {
		response.ErrorFrom(c, service.ErrSupportChatImageTooLarge)
		return in, false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.SupportChatRequestMaxBytes)
	if c.ContentType() == "application/json" {
		var req struct {
			Body        string `json:"body"`
			ClientMsgID string `json:"client_msg_id"`
		}
		// Read the whole capped body: a decoder would stop after the first JSON
		// value and leave oversized trailing data unread and unrejected.
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			if _, tooLarge := extractMaxBytesError(err); tooLarge {
				response.ErrorFrom(c, service.ErrSupportChatImageTooLarge)
			} else {
				response.BadRequest(c, "invalid request body")
			}
			return in, false
		}
		if json.Unmarshal(raw, &req) != nil {
			response.BadRequest(c, "invalid request body")
			return in, false
		}
		in.Body = req.Body
		in.ClientMsgID = req.ClientMsgID
		return in, true
	}
	if e := c.Request.ParseMultipartForm(service.SupportChatRequestMaxBytes); e != nil {
		var maxErr *http.MaxBytesError
		if errors.As(e, &maxErr) {
			response.ErrorFrom(c, service.ErrSupportChatImageTooLarge)
		} else {
			response.BadRequest(c, "invalid multipart request")
		}
		return in, false
	}
	defer func() { _ = c.Request.MultipartForm.RemoveAll() }()
	in.Body = c.PostForm("body")
	in.ClientMsgID = c.PostForm("client_msg_id")
	f, e := c.FormFile("image")
	if e == nil {
		file, e := f.Open()
		if e != nil {
			response.BadRequest(c, "invalid image")
			return in, false
		}
		data, e := io.ReadAll(io.LimitReader(file, service.SupportChatImageMaxBytes+1))
		_ = file.Close()
		if e != nil {
			response.BadRequest(c, "invalid image")
			return in, false
		}
		in.Image = &service.SupportChatImageInput{Data: data}
	}
	return in, true
}
func writeSupportChatImage(c *gin.Context, v *service.SupportChatImageResult) {
	c.Header("Content-Type", v.Image.ContentType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "sandbox")
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Disposition", "inline")
	c.Data(http.StatusOK, v.Image.ContentType, v.Data)
}

package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	apperrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

type OpenAITOTPHandler struct {
	svc      *service.OpenAITOTPService
	accounts *AccountHandler
	totp     *service.TotpService
	users    *service.UserService
	cfg      *config.Config
}

func NewOpenAITOTPHandler(svc *service.OpenAITOTPService, accounts *AccountHandler, totp *service.TotpService, users *service.UserService, cfg *config.Config) *OpenAITOTPHandler {
	return &OpenAITOTPHandler{svc: svc, accounts: accounts, totp: totp, users: users, cfg: cfg}
}
func openAITOTPNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
}
func (h *OpenAITOTPHandler) AdminGuard(c *gin.Context) {
	openAITOTPNoStore(c)
	if role, _ := middleware.GetUserRoleFromContext(c); role != service.RoleAdmin {
		c.Abort()
		response.Forbidden(c, "Full administrator access required")
		return
	}
	c.Next()
}
func (h *OpenAITOTPHandler) StepUp(c *gin.Context) {
	if h.totp == nil || h.users == nil {
		c.Abort()
		response.Forbidden(c, "A two-factor verified administrator session is required")
		return
	}
	if !middleware.EnforceStepUpAlways(c, h.totp, h.users) {
		return
	}
	c.Next()
}
func (h *OpenAITOTPHandler) WorkerGuard(c *gin.Context) {
	openAITOTPNoStore(c)
	secret := ""
	if h.cfg != nil {
		secret = h.cfg.OpenAITOTP.WorkerSecret
	}
	auth := c.GetHeader("Authorization")
	if len(secret) < 32 || !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(secret)) != 1 {
		c.Abort()
		response.Unauthorized(c, "Worker authorization required")
		return
	}
	c.Request.Header.Del("Authorization")
	c.Set("auth_method", "openai_totp_worker")
	c.Next()
}
func openAITOTPBind(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		response.BadRequest(c, "Invalid request body")
		return false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		response.BadRequest(c, "Invalid request body")
		return false
	}
	return true
}
func openAITOTPParam(c *gin.Context, key string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(key), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid identifier")
		return 0, false
	}
	return id, true
}
func openAITOTPRespond(c *gin.Context, data any, err error) {
	if err != nil {
		status, domain := apperrors.ToHTTP(err)
		if status >= 500 {
			response.ErrorWithDetails(c, status, "Managed OpenAI operation unavailable", "OPENAI_TOTP_UNAVAILABLE", nil)
		} else {
			response.ErrorWithDetails(c, status, domain.Message, domain.Reason, nil)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "success", "data": data})
}
func (h *OpenAITOTPHandler) Status(c *gin.Context) {
	id, ok := openAITOTPParam(c, "id")
	if !ok {
		return
	}
	out, err := h.svc.Status(c.Request.Context(), id)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) Save(c *gin.Context) {
	id, ok := openAITOTPParam(c, "id")
	if !ok {
		return
	}
	var in service.OpenAITOTPCredentials
	if !openAITOTPBind(c, &in) {
		return
	}
	out, err := h.svc.Save(c.Request.Context(), id, in)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) Rotate(c *gin.Context) {
	id, ok := openAITOTPParam(c, "id")
	if !ok {
		return
	}
	out, err := h.svc.Rotate(c.Request.Context(), id)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) Reconcile(c *gin.Context) {
	id, ok := openAITOTPParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		TaskID int64  `json:"task_id"`
		Action string `json:"action"`
	}
	if !openAITOTPBind(c, &in) {
		return
	}
	out, err := h.svc.Reconcile(c.Request.Context(), id, in.TaskID, in.Action)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) Export(c *gin.Context) {
	id, ok := openAITOTPParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Source          string `json:"source"`
		IncludePassword bool   `json:"include_password"`
	}
	if !openAITOTPBind(c, &in) {
		return
	}
	out, err := h.svc.Export(c.Request.Context(), id, in.Source, in.IncludePassword)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) Login(c *gin.Context) {
	var in service.OpenAILoginInput
	if !openAITOTPBind(c, &in) {
		return
	}
	out, err := h.svc.StartLogin(c.Request.Context(), in)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) LoginTask(c *gin.Context) {
	id, ok := openAITOTPParam(c, "task_id")
	if !ok {
		return
	}
	out, err := h.svc.LoginTask(c.Request.Context(), id)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) Import(c *gin.Context) {
	id, ok := openAITOTPParam(c, "task_id")
	if !ok {
		return
	}
	accountID, err := h.svc.ImportLogin(c.Request.Context(), id, h.importSession)
	openAITOTPRespond(c, gin.H{"account_id": accountID}, err)
}
func validateManagedOpenAISession(session map[string]any, email string) error {
	if session == nil {
		return service.ErrOpenAITOTPInvalid
	}
	user, ok := session["user"].(map[string]any)
	if !ok {
		return service.ErrOpenAITOTPInvalid
	}
	userEmail, _ := user["email"].(string)
	if service.NormalizeOpenAIEmail(userEmail) != email {
		return service.ErrOpenAITOTPInvalid
	}
	token, _ := session["accessToken"].(string)
	claims, err := decodeCodexJWTClaims(token)
	if err != nil || claims.Exp <= time.Now().Unix() || claims.OpenAIAuth == nil || claims.OpenAIAuth.ChatGPTAccountID == "" {
		return service.ErrOpenAITOTPInvalid
	}
	claimEmail := claims.Email
	if claimEmail == "" {
		parts := strings.Split(token, ".")
		payload, err := decodeCodexJWTSegment(parts[1])
		if err != nil {
			return service.ErrOpenAITOTPInvalid
		}
		var raw map[string]any
		if json.Unmarshal(payload, &raw) != nil {
			return service.ErrOpenAITOTPInvalid
		}
		if profile, ok := raw["https://api.openai.com/profile"].(map[string]any); ok {
			claimEmail, _ = profile["email"].(string)
		}
	}
	if service.NormalizeOpenAIEmail(claimEmail) != email {
		return service.ErrOpenAITOTPInvalid
	}
	if account, ok := session["account"].(map[string]any); ok {
		if id, ok := account["id"].(string); ok && id != "" && id != claims.OpenAIAuth.ChatGPTAccountID {
			return service.ErrOpenAITOTPInvalid
		}
	}
	item, err := normalizeCodexImportEntry(codexImportEntry{Index: 1, Value: session})
	if err != nil || item.IsAgentIdentity || item.AccountID != claims.OpenAIAuth.ChatGPTAccountID || service.NormalizeOpenAIEmail(item.Email) != email {
		return service.ErrOpenAITOTPInvalid
	}
	return nil
}
func (h *OpenAITOTPHandler) importSession(ctx context.Context, taskID int64, in service.OpenAILoginInput, session map[string]any) (int64, error) {
	if err := validateManagedOpenAISession(session, in.Email); err != nil {
		return 0, err
	}
	if len(in.ImportMarker) != 64 {
		return 0, service.ErrOpenAITOTPInvalid
	}
	item, err := normalizeCodexImportEntry(codexImportEntry{Index: 1, Value: session})
	if err != nil {
		return 0, service.ErrOpenAITOTPInvalid
	}
	existing, err := h.accounts.listAccountsFiltered(ctx, service.PlatformOpenAI, service.AccountTypeOAuth, "", "", 0, "", "created_at", "desc")
	if err != nil {
		return 0, service.ErrOpenAITOTPUnavailable
	}
	matched := int64(0)
	for _, a := range existing {
		if value, _ := a.Extra[service.ManagedOpenAILoginMarkerExtraKey].(string); value == in.ImportMarker {
			if matched != 0 || a.IsShadow() || a.GetCredential("chatgpt_account_id") != item.AccountID || a.GetCredential("chatgpt_user_id") != item.UserID || service.NormalizeOpenAIEmail(a.GetCredential("email")) != in.Email {
				return 0, service.ErrOpenAITOTPInvalid
			}
			matched = a.ID
		}
	}
	if matched != 0 {
		return matched, nil
	}
	no := false
	result, err := h.accounts.importCodexSessions(ctx, CodexSessionImportRequest{Name: in.Name, ProxyID: in.ProxyID, GroupIDs: in.GroupIDs, UpdateExisting: &no, ManagedOpenAILoginMarker: in.ImportMarker, Extra: map[string]any{service.ManagedOpenAILoginMarkerExtraKey: in.ImportMarker}}, []codexImportEntry{{Index: 1, Value: session}})
	if err != nil || result.Created != 1 || len(result.Items) != 1 || result.Items[0].AccountID <= 0 {
		return 0, service.ErrOpenAITOTPInvalid
	}
	return result.Items[0].AccountID, nil
}
func (h *OpenAITOTPHandler) Claim(c *gin.Context) {
	var in service.OpenAITOTPHeartbeat
	if !openAITOTPBind(c, &in) {
		return
	}
	out, err := h.svc.Claim(c.Request.Context(), in)
	openAITOTPRespond(c, out, err)
}
func (h *OpenAITOTPHandler) Phase(c *gin.Context) {
	id, ok := openAITOTPParam(c, "task_id")
	if !ok {
		return
	}
	var in struct {
		ClaimToken string `json:"claim_token"`
		Phase      string `json:"phase"`
		Secret     string `json:"secret"`
	}
	if !openAITOTPBind(c, &in) {
		return
	}
	err := h.svc.Phase(c.Request.Context(), id, in.ClaimToken, in.Phase, in.Secret)
	openAITOTPRespond(c, gin.H{"ok": true}, err)
}
func (h *OpenAITOTPHandler) Finish(c *gin.Context) {
	id, ok := openAITOTPParam(c, "task_id")
	if !ok {
		return
	}
	var in struct {
		ClaimToken string         `json:"claim_token"`
		Success    bool           `json:"success"`
		ErrorCode  string         `json:"error_code"`
		Session    map[string]any `json:"session"`
	}
	if !openAITOTPBind(c, &in) {
		return
	}
	err := h.svc.Finish(c.Request.Context(), id, in.ClaimToken, in.Success, in.ErrorCode, in.Session, validateManagedOpenAISession)
	openAITOTPRespond(c, gin.H{"ok": true}, err)
}
func (h *OpenAITOTPHandler) Recover(c *gin.Context) {
	var in struct {
		TaskID int64  `json:"task_id"`
		Secret string `json:"secret"`
	}
	if !openAITOTPBind(c, &in) {
		return
	}
	err := h.svc.Recover(c.Request.Context(), in.TaskID, in.Secret)
	openAITOTPRespond(c, gin.H{"ok": true}, err)
}

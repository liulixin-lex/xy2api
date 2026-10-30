package admin

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAITOTPManagedSessionIdentityValidation(t *testing.T) {
	validToken := buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{"email": "test@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"}})
	valid := func() map[string]any {
		return map[string]any{"accessToken": validToken, "user": map[string]any{"email": "TEST@example.com", "id": "user-1"}, "account": map[string]any{"id": "acct-1"}}
	}
	require.NoError(t, validateManagedOpenAISession(valid(), "test@example.com"))
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong user", func(v map[string]any) { v["user"] = map[string]any{"email": "other@example.com"} }},
		{"wrong jwt", func(v map[string]any) {
			v["accessToken"] = buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{"email": "other@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}})
		}},
		{"wrong account", func(v map[string]any) { v["account"] = map[string]any{"id": "acct-other"} }},
		{"opaque", func(v map[string]any) { v["accessToken"] = "opaque-token" }},
		{"expired", func(v map[string]any) {
			v["accessToken"] = buildCodexImportTestJWT(t, time.Now().Add(-time.Minute), map[string]any{"email": "test@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}})
		}},
		{"agent identity", func(v map[string]any) { v["auth_mode"] = "agentIdentity" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			v := valid()
			test.change(v)
			require.ErrorIs(t, validateManagedOpenAISession(v, "test@example.com"), service.ErrOpenAITOTPInvalid)
		})
	}
	profileToken := buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{"https://api.openai.com/profile": map[string]any{"email": "test@example.com"}, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}})
	v := valid()
	v["accessToken"] = profileToken
	require.NoError(t, validateManagedOpenAISession(v, "test@example.com"))
}
func TestOpenAITOTPGuardsNoStoreAndStepUpAlways(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAITOTPHandler(nil, nil, nil, nil, &config.Config{OpenAITOTP: config.OpenAITOTPConfig{WorkerSecret: strings.Repeat("w", 32)}})
	for _, role := range []string{service.RoleOperator, service.RoleUser, ""} {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role) }, h.AdminGuard)
		r.GET("/", func(c *gin.Context) { t.Error("unauthorized reached handler") })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		require.Equal(t, 403, w.Code)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin) }, h.AdminGuard, h.StepUp)
	r.POST("/", func(c *gin.Context) { t.Error("stepup bypass") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	require.Equal(t, 403, w.Code)
	for _, auth := range []string{"", "Bearer " + strings.Repeat("x", 32), "Bearer " + strings.Repeat("w", 32)} {
		worker := gin.New()
		worker.Use(h.WorkerGuard)
		worker.POST("/", func(c *gin.Context) { require.Empty(t, c.GetHeader("Authorization")); openAITOTPRespond(c, nil, nil) })
		request := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		request.Header.Set("Authorization", auth)
		w = httptest.NewRecorder()
		worker.ServeHTTP(w, request)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		if strings.HasSuffix(auth, strings.Repeat("w", 32)) {
			require.Equal(t, 200, w.Code)
			require.Contains(t, w.Body.String(), `"data":null`)
		} else {
			require.Equal(t, 401, w.Code)
		}
	}
}
func TestOpenAITOTPBoundedBodyAndSafeErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/", func(c *gin.Context) {
		var in service.OpenAITOTPCredentials
		if openAITOTPBind(c, &in) {
			openAITOTPRespond(c, nil, nil)
		}
	})
	for _, body := range []string{`{"unknown":"sensitive-value"}`, `{} {}`, `{"password":"` + strings.Repeat("s", 129*1024) + `"}`} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		require.Equal(t, 400, w.Code)
		require.NotContains(t, w.Body.String(), "sensitive-value")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)
	old := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(old)
	openAITOTPRespond(c, nil, errors.New("password private-key raw upstream exception"))
	require.Equal(t, 500, w.Code)
	require.NotContains(t, w.Body.String(), "private-key")
}
func TestOpenAITOTPImportSessionTaskMarkerIsIdempotent(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	h := &OpenAITOTPHandler{accounts: &AccountHandler{adminService: svc}}
	token := buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{"email": "test@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"}})
	session := map[string]any{"accessToken": token, "user": map[string]any{"email": "test@example.com", "id": "user-1"}}
	in := service.OpenAILoginInput{ImportMarker: strings.Repeat("a", 64), OpenAITOTPCredentials: service.OpenAITOTPCredentials{Email: "test@example.com"}}
	first, err := h.importSession(context.Background(), 7, in, session)
	require.NoError(t, err)
	again, err := h.importSession(context.Background(), 7, in, session)
	require.NoError(t, err)
	require.Equal(t, first, again)
	require.Len(t, svc.createdAccounts, 1)
	require.Empty(t, svc.updatedAccounts)
	require.Equal(t, in.ImportMarker, svc.createdAccounts[0].Extra[service.ManagedOpenAILoginMarkerExtraKey])
	// Even matching OpenAI identity in another task creates, never updates an account.
	in.ImportMarker = strings.Repeat("b", 64)
	_, err = h.importSession(context.Background(), 8, in, session)
	require.NoError(t, err)
	require.Len(t, svc.createdAccounts, 2)
	require.Empty(t, svc.updatedAccounts)
	require.NotContains(t, svc.createdAccounts[0].Credentials, "password")
	require.NotContains(t, svc.createdAccounts[0].Credentials, "totp_secret")
}

func TestOpenAITOTPImportRejectsForgedAndCopiedMarkers(t *testing.T) {
	token := buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{"email": "test@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"}})
	session := map[string]any{"accessToken": token, "user": map[string]any{"email": "test@example.com", "id": "user-1"}}
	in := service.OpenAILoginInput{ImportMarker: strings.Repeat("a", 64), OpenAITOTPCredentials: service.OpenAITOTPCredentials{Email: "test@example.com"}}
	account := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"email": "test@example.com", "chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"}, Extra: map[string]any{service.ManagedOpenAILoginMarkerExtraKey: in.ImportMarker}}
	for _, kind := range []string{"wrong account", "duplicate marker", "invalid session"} {
		t.Run(kind, func(t *testing.T) {
			svc := newCodexImportMemoryAdminService([]service.Account{account})
			h := &OpenAITOTPHandler{accounts: &AccountHandler{adminService: svc}}
			inputSession := session
			switch kind {
			case "wrong account":
				svc.accounts[0].Credentials = map[string]any{"email": "wrong@example.com", "chatgpt_account_id": "other"}
			case "duplicate marker":
				copy := account
				copy.ID = 2
				svc.accounts = append(svc.accounts, copy)
			case "invalid session":
				inputSession = map[string]any{"accessToken": "opaque", "user": map[string]any{"email": "test@example.com"}}
			}
			_, err := h.importSession(context.Background(), 1, in, inputSession)
			require.ErrorIs(t, err, service.ErrOpenAITOTPInvalid)
			require.Empty(t, svc.createdAccounts)
			require.Empty(t, svc.updatedAccounts)
		})
	}
}

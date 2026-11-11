//go:build unit

package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type campaignHandlerRepo struct {
	service.EmailCampaignRepository
	mutations  int
	readError  error
	writeError error
}

func (r *campaignHandlerRepo) ReadSecret(context.Context) (string, error) {
	return strings.Repeat("s", 64), r.readError
}
func (r *campaignHandlerRepo) Unsubscribe(context.Context, string, int64) error {
	r.mutations++
	return r.writeError
}
func TestEmailCampaignHandlerInvalidInputs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &campaignHandlerRepo{}
	h := NewEmailCampaignHandler(service.NewEmailCampaignService(repo, nil, nil, nil, nil))
	r := gin.New()
	r.POST("/settings", h.UpdateSettings)
	r.POST("/preview", h.Preview)
	r.GET("/:id/recipients", h.Recipients)
	r.GET("/unsubscribe", h.Unsubscribe)
	r.POST("/unsubscribe", h.Unsubscribe)
	for _, tc := range []struct{ method, path, body string }{{"POST", "/settings", `{"per_minute":0,"per_day":1}`}, {"POST", "/preview", `{"audience":{"timezone":"invalid"}}`}, {"POST", "/preview", `{"audience":`}, {"GET", "/bad/recipients", ""}, {"GET", "/unsubscribe?token=bad", ""}, {"POST", "/unsubscribe?token=bad", ""}} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code)
	}
	require.Zero(t, repo.mutations)
}

func TestEmailCampaignUnsubscribeFailures(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"e":"test@example.com","c":0}`))
	mac := hmac.New(sha256.New, []byte(strings.Repeat("s", 64)))
	_, err := mac.Write([]byte("email_campaign:" + payload))
	require.NoError(t, err)
	token := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	for _, tc := range []struct {
		name, method          string
		readError, writeError error
		status, mutations     int
	}{
		{"get is read only", "GET", nil, nil, 200, 0},
		{"post", "POST", nil, nil, 200, 1},
		{"secret unavailable", "GET", errors.New("database unavailable"), nil, 503, 0},
		{"suppression unavailable", "POST", nil, errors.New("database unavailable"), 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &campaignHandlerRepo{readError: tc.readError, writeError: tc.writeError}
			h := NewEmailCampaignHandler(service.NewEmailCampaignService(repo, nil, nil, nil, nil))
			router := gin.New()
			router.GET("/unsubscribe", h.Unsubscribe)
			router.POST("/unsubscribe", h.Unsubscribe)
			req := httptest.NewRequest(tc.method, "/unsubscribe?token="+token, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.mutations, repo.mutations)
		})
	}
}

type campaignDetailUsers struct{ service.UserRepository }

func (campaignDetailUsers) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{Email: "admin@example.com"}, nil
}
func (r *campaignHandlerRepo) Get(context.Context, int64) (*service.EmailCampaign, error) {
	return &service.EmailCampaign{ID: 7, Subject: "test", BodyMarkdown: "saved body", Status: "paused"}, nil
}

type campaignPageSettings struct{ service.SettingRepository }

func (campaignPageSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeySiteName {
		return `<img src=x onerror="bad"> & Site`, nil
	}
	return "http://panel.example.com", nil
}
func TestEmailCampaignDetailWithoutPreview(t *testing.T) {
	svc := service.NewEmailCampaignService(&campaignHandlerRepo{}, nil, nil, service.NewNotificationEmailService(campaignPageSettings{}, nil), campaignDetailUsers{})
	h := NewEmailCampaignHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1}) })
	router.GET("/:id", h.Detail)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/7", nil))
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `"preview_html":""`)
	require.Contains(t, response.Body.String(), `"body_markdown":"saved body"`)
	require.Contains(t, response.Body.String(), `"status":"paused"`)
}
func TestEmailCampaignUnsubscribePageStyles(t *testing.T) {
	svc := service.NewEmailCampaignService(&campaignHandlerRepo{}, nil, nil, service.NewNotificationEmailService(campaignPageSettings{}, nil), nil)
	h := NewEmailCampaignHandler(svc)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"e":"test@example.com","c":0}`))
	mac := hmac.New(sha256.New, []byte(strings.Repeat("s", 64)))
	_, err := mac.Write([]byte("email_campaign:" + payload))
	require.NoError(t, err)
	token := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	router := gin.New()
	router.GET("/unsubscribe", h.Unsubscribe)
	router.POST("/unsubscribe", h.Unsubscribe)
	for _, tc := range []struct{ method, token, expected string }{{"GET", token, "确认退订 / Unsubscribe"}, {"POST", token, "已退订 / Unsubscribed"}, {"GET", "bad", "Invalid unsubscribe link"}} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tc.method, "/unsubscribe?token="+tc.token, nil))
		body := response.Body.String()
		require.Contains(t, body, tc.expected)
		require.Contains(t, body, "&lt;img")
		require.NotContains(t, body, "<img")
		require.Contains(t, body, "<main>")
		require.NotContains(t, body, "<script")
		style := strings.SplitN(strings.SplitN(body, "<style>", 2)[1], "</style>", 2)[0]
		digest := sha256.Sum256([]byte(style))
		csp := response.Header().Get("Content-Security-Policy")
		require.Contains(t, csp, "'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'")
		require.Contains(t, csp, "default-src 'none'")
		require.NotContains(t, csp, "unsafe-inline")
	}
}

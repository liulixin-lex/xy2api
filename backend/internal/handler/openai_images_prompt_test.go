//go:build unit

package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestImagesRejectEmptyPromptBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"/v1/images/generations", "/v1/images/edits"} {
		for _, form := range []string{"json", "multipart"} {
			for _, prompt := range []string{"missing", "empty", "spaces", "valid"} {
				t.Run(endpoint+"/"+form+"/"+prompt, func(t *testing.T) {
					var body bytes.Buffer
					contentType := "application/json"
					value := map[string]string{"missing": "", "empty": "", "spaces": "   ", "valid": "draw"}[prompt]
					if form == "multipart" {
						w := multipart.NewWriter(&body)
						require.NoError(t, w.WriteField("model", "gpt-image-2"))
						if prompt != "missing" {
							require.NoError(t, w.WriteField("prompt", value))
						}
						if endpoint == "/v1/images/edits" {
							p, err := w.CreateFormFile("image", "input.png")
							require.NoError(t, err)
							_, err = p.Write([]byte("image"))
							require.NoError(t, err)
						}
						contentType = w.FormDataContentType()
						require.NoError(t, w.Close())
					} else {
						body.WriteString(`{"model":"gpt-image-2"`)
						if prompt != "missing" {
							body.WriteString(`,"prompt":"` + value + `"`)
						}
						if endpoint == "/v1/images/edits" {
							body.WriteString(`,"images":[{"image_url":"https://example.com/image.png"}]`)
						}
						body.WriteString(`}`)
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, endpoint, &body)
					c.Request.Header.Set("Content-Type", contentType)
					groupID := int64(111)
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 222, GroupID: &groupID, Group: &service.Group{ID: groupID, AllowImageGeneration: false}, User: &service.User{ID: 333}})
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 333, Concurrency: 1})
					h := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}, billingCacheService: &service.BillingCacheService{}, apiKeyService: &service.APIKeyService{}, concurrencyHelper: &ConcurrencyHelper{concurrencyService: &service.ConcurrencyService{}}}
					h.Images(c)
					if prompt == "valid" {
						require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
						return
					}
					require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
					require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
					require.Equal(t, "prompt is required", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
				})
			}
		}
	}
}

func TestImagesEmptyPromptRejectsBeforeAccountTypeSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		t.Run(accountType, func(t *testing.T) {
			account := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: accountType, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test", "api_key": "test"}}
			repo := openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
			upstream := &openAIImagesFailoverHTTPUpstream{}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			core, logs := observer.New(zap.DebugLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"model":"gpt-image-1","prompt":""}`)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			groupID := int64(3130)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID, Group: &service.Group{ID: groupID, AllowImageGeneration: true}, User: &service.User{ID: 100}})
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100})
			h.Images(c)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.Equal(t, "prompt is required", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
			require.Empty(t, logs.FilterMessage("openai.images.account_selecting").All())
			require.Empty(t, upstream.calls())
		})
	}
}

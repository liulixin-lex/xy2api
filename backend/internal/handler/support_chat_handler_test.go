//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type supportChatHandlerRepo struct {
	nextID   int64
	chat     service.SupportChat
	messages []service.SupportChatMessage
}

func (r *supportChatHandlerRepo) GetUserChat(context.Context, int64) (*service.SupportChat, error) {
	if r.chat.ID == 0 {
		return nil, service.ErrSupportChatNotFound
	}
	return &r.chat, nil
}
func (r *supportChatHandlerRepo) GetChat(context.Context, int64, bool) (*service.SupportChat, error) {
	return &r.chat, nil
}
func (r *supportChatHandlerRepo) ListChats(context.Context, service.SupportChatListFilter) ([]service.SupportChat, int64, error) {
	return []service.SupportChat{r.chat}, 1, nil
}
func (r *supportChatHandlerRepo) ListMessages(context.Context, service.SupportChatMessagePageInput) ([]service.SupportChatMessage, bool, *service.SupportChat, error) {
	return r.messages, false, &r.chat, nil
}
func (r *supportChatHandlerRepo) SendMessage(_ context.Context, in service.SupportChatSendInput) (*service.SupportChatMessage, bool, error) {
	r.nextID++
	if r.chat.ID == 0 {
		r.chat.ID = 1
		r.chat.UserID = in.UserID
	}
	m := &service.SupportChatMessage{ID: r.nextID, AuthorRole: in.AuthorRole, Body: in.Body, ClientMsgID: in.ClientMsgID, CreatedAt: time.Now()}
	r.messages = append(r.messages, *m)
	r.chat.LastMessageID = m.ID
	return m, false, nil
}
func (r *supportChatHandlerRepo) MarkRead(context.Context, int64, bool, int64) (int64, error) {
	return 0, nil
}
func (r *supportChatHandlerRepo) SetStatus(context.Context, service.SupportChatStatusInput) error {
	return nil
}
func (r *supportChatHandlerRepo) SetBlocked(context.Context, service.SupportChatBlockInput) error {
	return nil
}
func (r *supportChatHandlerRepo) DeleteMessage(context.Context, int64, int64) (*service.SupportChatMessage, error) {
	return &service.SupportChatMessage{}, nil
}
func (r *supportChatHandlerRepo) DeleteChat(context.Context, int64) error { return nil }
func (r *supportChatHandlerRepo) GetImage(context.Context, int64, int64, bool) (*service.SupportChatImageResult, error) {
	return nil, service.ErrSupportChatMessageNotFound
}
func (r *supportChatHandlerRepo) Summary(context.Context) (service.SupportChatSummary, error) {
	return service.SupportChatSummary{}, nil
}
func (r *supportChatHandlerRepo) UnreadCount(context.Context, int64, int64) (int64, error) {
	return 0, nil
}
func (r *supportChatHandlerRepo) PurgeImages(context.Context, int, time.Time) (int64, int64, error) {
	return 0, 0, nil
}

func TestSupportChatHandlerUserFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &supportTicketSettingRepo{values: map[string]string{service.SettingKeySupportTicketEnabled: "true", service.SettingKeySupportChatEnabled: "true", service.SettingKeySupportTicketConfig: `{"chat_welcome":"welcome"}`}}
	repo := &supportChatHandlerRepo{}
	h := NewSupportChatHandler(service.NewSupportChatService(repo, service.NewSettingService(settings, &config.Config{}), nil))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		c.Next()
	})
	r.POST("/api/v1/support-chat/messages", h.Send)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/support-chat/messages", bytes.NewBufferString(`{"body":"hello","client_msg_id":"client-01"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Message service.SupportChatMessage `json:"message"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Equal(t, 0, envelope.Code)
	require.Equal(t, "hello", envelope.Data.Message.Body)
}

func supportChatPNG(t *testing.T) []byte {
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	return buffer.Bytes()
}

func TestSupportChatHandlerMultipartAndRequestLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &supportTicketSettingRepo{values: map[string]string{service.SettingKeySupportTicketEnabled: "true", service.SettingKeySupportChatEnabled: "true"}}
	repo := &supportChatHandlerRepo{}
	h := NewSupportChatHandler(service.NewSupportChatService(repo, service.NewSettingService(settings, &config.Config{}), nil))
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		c.Next()
	})
	r.POST("/messages", h.Send)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("body", "image"))
	require.NoError(t, writer.WriteField("client_msg_id", "image-001"))
	part, err := writer.CreateFormFile("image", "x.png")
	require.NoError(t, err)
	_, err = part.Write(supportChatPNG(t))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/messages", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	req = httptest.NewRequest(http.MethodPost, "/messages", bytes.NewReader(bytes.Repeat([]byte{'x'}, service.SupportChatRequestMaxBytes+1)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

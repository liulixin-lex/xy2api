//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM9ChatSelectionReturnsResponsesFields(t *testing.T) {
	for _, field := range []string{"tools", "functions"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", field, stream), func(t *testing.T) {
				withHandlerResilience(t, nil)
				cfg := &config.Config{RunMode: config.RunModeSimple}
				group := &service.Group{ID: 709, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
				repo := &modelRejectAccountRepo{accounts: []service.Account{{ID: 1, Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{group.ID},
					Credentials: map[string]any{"api_protocol": service.APIProtocolChatCompletions, "model_mapping": map[string]any{"public-model": "gpt-6.1-sol"}},
					Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: false},
				}}}
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
					nil, nil, billing, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				function := map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}
				tool := any(function)
				if field == "tools" {
					tool = map[string]any{"type": "function", "function": function}
				}
				body, err := json.Marshal(map[string]any{"model": "public-model", "messages": []any{map[string]string{"role": "user", "content": "hello"}}, "stream": stream, field: []any{tool}})
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, EndpointChatCompletions, strings.NewReader(string(body)))
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, GroupID: &group.ID, Group: group, User: &service.User{ID: 2, Status: service.StatusActive}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 2})
				h.ChatCompletions(c)
				require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
				require.Equal(t, "invalid_request_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
				require.Equal(t, "responses_required", gjson.GetBytes(recorder.Body.Bytes(), "error.code").String())
				require.Equal(t, field, gjson.GetBytes(recorder.Body.Bytes(), "error.param").String())
				require.Contains(t, gjson.GetBytes(recorder.Body.Bytes(), "error.message").String(), `"`+field+`"`)
			})
		}
	}
}

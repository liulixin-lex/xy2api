//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeHTTPContinuationErrorContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, rejection := range []*service.HTTPContinuationError{
		service.NewHTTPContinuationOwnerMismatchError(), service.NewHTTPContinuationUnsupportedError(),
		service.NewToolOutputMissingCallIDError(2, "function_call_output"), service.NewHTTPContinuationExpiredError(),
	} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed_%t", rejection.Code, committed), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Header("Retry-After", "9")
				if committed {
					c.Header("Content-Type", "text/event-stream")
					c.Writer.WriteHeader(http.StatusOK)
					c.Writer.WriteHeaderNow()
				}
				require.True(t, service.WriteHTTPContinuationError(c, nil, rejection))
				before := rec.Body.String()
				require.False(t, service.WriteHTTPContinuationError(c, nil, rejection))
				require.Equal(t, before, rec.Body.String())
				require.Contains(t, before, rejection.Code)
				require.Contains(t, before, rejection.Param)
				require.Contains(t, before, rejection.Message)
				if committed {
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, 1, strings.Count(before, `"type":"response.failed"`))
				} else {
					require.Equal(t, rejection.Status, rec.Code)
					require.Equal(t, rejection.Code, gjson.Get(before, "error.code").String())
					require.Empty(t, rec.Header().Get("Retry-After"))
					require.Equal(t, "false", rec.Header().Get("x-should-retry"))
				}
			})
		}
	}
	for _, store := range []string{"", `,"store":false`, `,"store":true`} {
		for _, scenario := range []string{"other_user", "same_user_new_key", "missing_call_id", "named_output"} {
			t.Run(scenario+store, func(t *testing.T) {
				groupID := int64(2)
				body := `{"model":"gpt-5.1","stream":false,"previous_response_id":"resp_owned","input":"hello"` + store + `}`
				if scenario == "missing_call_id" {
					body = `{"model":"gpt-5.1","input":[{"type":"function_call_output","output":"ok"}]` + store + `}`
				} else if scenario == "named_output" {
					body = `{"model":"gpt-5.1","input":[{"type":"function_call_output","name":"task","output":"ok"}]` + store + `}`
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				userID := int64(1)
				if scenario == "other_user" {
					userID = 2
				}
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 202, UserID: userID, GroupID: &groupID, User: &service.User{ID: userID}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID, Concurrency: 1})
				h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
				require.NoError(t, h.gatewayService.BindOpenAIHTTPResponseOwner(context.Background(), groupID, "resp_owned", 1, 101))
				h.Responses(c)
				switch scenario {
				case "other_user":
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.Equal(t, "previous_response_owner_mismatch", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
				case "missing_call_id":
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.Equal(t, "tool_output_missing_call_id", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
					require.Equal(t, "input[0].call_id", gjson.GetBytes(rec.Body.Bytes(), "error.param").String())
				default:
					require.NotEqual(t, http.StatusBadRequest, rec.Code)
				}
			})
		}
	}
}

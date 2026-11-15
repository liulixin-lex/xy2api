//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
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

type continuationRoutingUpstream struct {
	service.HTTPUpstream
	accountIDs []int64
	bodies     [][]byte
	paths      []string
}

func (u *continuationRoutingUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	u.accountIDs, u.bodies, u.paths = append(u.accountIDs, accountID), append(u.bodies, body), append(u.paths, req.URL.Path)
	reply := `{"id":"resp_ok","object":"response","model":"gpt-5.1","status":"completed","output":[{"type":"message","id":"msg_ok","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	if strings.HasSuffix(req.URL.Path, "/chat/completions") {
		reply = `{"id":"chatcmpl_ok","object":"chat.completion","model":"gpt-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
}

// Review inputs for previous_response_id over HTTP through routing and
// Forward: requests that succeed on the baseline keep succeeding.
func TestNativeHTTPContinuationRoutesLikeBaseline(t *testing.T) {
	previous := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(previous) })
	gin.SetMode(gin.TestMode)
	apiKeyAccount := func(id int64, priority int, extra map[string]any) service.Account {
		return service.Account{ID: id, Name: "api-key", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: priority, Concurrency: 1,
			Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://compat.example"}, Extra: extra}
	}
	for _, tc := range []struct {
		name     string
		accounts []service.Account
		sentTo   int64
		pointer  bool
	}{
		// (b) API-key accounts whose upstream speaks Chat Completions convert the
		// request; the pointer does not reach the upstream.
		{"chat_protocol_api_key", []service.Account{apiKeyAccount(1, 0, map[string]any{"openai_responses_supported": false})}, 1, false},
	} {
		for _, store := range []string{"", `,"store":false`, `,"store":true`} {
			t.Run(tc.name+store, func(t *testing.T) {
				repo := &openAIImagesFailoverAccountRepo{accounts: tc.accounts}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				upstream := &continuationRoutingUpstream{}
				rl := service.NewRateLimitService(repo, nil, cfg, nil, nil)
				rl.SetResilienceService(service.NewResilienceService(nil, nil))
				gw := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, rl, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				h := NewOpenAIGatewayHandler(gw, service.NewConcurrencyService(nil), billing,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				h.maxAccountSwitches = 10
				groupID := int64(2)
				require.NoError(t, gw.BindOpenAIHTTPResponseOwner(context.Background(), groupID, "resp_owned", 1, 101))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","stream":false,"previous_response_id":"resp_owned","input":"hello"`+store+`}`))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 202, UserID: 1, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}, User: &service.User{ID: 1}})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 0})

				h.Responses(c)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, []int64{tc.sentTo}, upstream.accountIDs)
				wire := upstream.bodies[0]
				if !tc.pointer {
					require.True(t, strings.HasSuffix(upstream.paths[0], "/chat/completions"), upstream.paths[0])
					require.False(t, gjson.GetBytes(wire, "previous_response_id").Exists())
					return
				}
				require.Equal(t, "resp_owned", gjson.GetBytes(wire, "previous_response_id").String())
				require.Equal(t, gjson.Get(`{"x":0`+store+`}`, "store").Raw, gjson.GetBytes(wire, "store").Raw)
			})
		}
	}
}

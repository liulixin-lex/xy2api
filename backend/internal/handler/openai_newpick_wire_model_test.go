//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const newPickWireGroup = 4201

func newPickWireAccount(id int64, alias, mapped string) service.Account {
	return service.Account{
		ID: id, Status: service.StatusActive, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Schedulable: true, Concurrency: 2, Priority: 1, GroupIDs: []int64{newPickWireGroup},
		Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://capture.example", "model_mapping": map[string]any{alias: mapped}},
	}
}

func newPickWireFixture(t *testing.T, repo followHTTPAccounts, up *captureUpstream, scoreModel string) (*OpenAIGatewayHandler, *captureRecorder, *gin.Engine) {
	t.Helper()
	db := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: db.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	slots := &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
	h, rec, router := captureFixture(t, service.Account{}, up, captureOptions{repo: repo, cache: repository.NewGatewayCache(redisClient), slots: service.NewConcurrencyService(slots)})
	prev := service.Resilience()
	res := service.ProvideResilienceService(nil, repository.NewResilienceStore(redisClient), nil, nil, repo, nil, nil, &service.GatewayService{}, h.gatewayService)
	res.Stop()
	t.Cleanup(func() { service.SetResilienceInstance(prev) })
	service.InstallNewPickSnapshotForTest(h.gatewayService, newPickWireGroup, scoreModel)
	return h, rec, router
}

func TestOpenAINewPickWireModel(t *testing.T) {
	withHandlerResilience(t, nil)
	const model = "gpt-5.1"
	for _, entry := range []string{"responses", "chat/completions", "messages"} {
		for _, tc := range []struct {
			name                   string
			baseModel, targetModel string
		}{
			{"different mappings", "gpt-5.2", "gpt-5.4"},
			{"target mismatch only", model, "gpt-5.4"},
			{"same wire model but different score model", "gpt-5.2", "gpt-5.2"},
			{"wire and score models match", model, model},
		} {
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				repo := followHTTPAccounts{}
				for i, mapped := range []string{tc.baseModel, tc.targetModel} {
					account := newPickWireAccount(int64(i+1), model, mapped)
					account.Extra = map[string]any{"openai_passthrough": true}
					repo.accounts = append(repo.accounts, account)
				}
				up := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
					return captureResponse(http.StatusOK, captureSSE(), true), nil
				}}
				_, rec, router := newPickWireFixture(t, repo, up, model)
				logs, restore := captureHandlerStructuredLog(t)
				defer restore()

				resp := captureRequest(router, "/v1/"+entry, `{"model":"gpt-5.1","stream":true,"input":"hi","prompt_cache_key":"new-pick-wire","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
				require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
				require.Len(t, up.bodies, 1)
				var outbound struct{ Model string }
				require.NoError(t, json.Unmarshal(up.bodies[0], &outbound))
				require.Len(t, rec.starts, 1)
				require.Equal(t, model, rec.starts[0].Model)
				// Native Responses passthrough sends the unchanged body model. Chat and
				// Messages send the model selected by their forwarding adapter.
				picked := entry == "responses" || tc.baseModel == model && tc.targetModel == model
				if picked {
					require.EqualValues(t, 2, rec.starts[0].AccountID)
					require.Equal(t, model, outbound.Model)
					value, found := logs.FieldValueForMessage("sched_quality.new_pick", "result")
					require.True(t, found)
					require.Equal(t, "picked", value)
				} else {
					require.EqualValues(t, 1, rec.starts[0].AccountID)
					require.Equal(t, tc.baseModel, outbound.Model)
					require.False(t, logs.ContainsMessageAtLevel("sched_quality.new_pick", "info"))
				}
			})
		}
	}
}

func TestOpenAINewPickResponsesMappedImageModel(t *testing.T) {
	withHandlerResilience(t, nil)
	const alias = "draw-alias"
	for _, tc := range []struct {
		name, mappedModel string
		wantAccount       int64
		wantPath          string
	}{
		{"image stays on base", "gpt-image-1", 1, "/v1/chat/completions"},
		{"text moves to target", "gpt-5.1", 2, "/v1/responses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newPickWireAccount(1, alias, tc.mappedModel)
			base.Extra = map[string]any{"openai_responses_supported": false}
			target := newPickWireAccount(2, alias, tc.mappedModel)
			repo := followHTTPAccounts{accounts: []service.Account{base, target}}

			var path string
			up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
				path = req.URL.Path
				if path == "/v1/chat/completions" {
					const chunk = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`
					return captureResponse(http.StatusOK, chunk+"\n\ndata: [DONE]\n\n", true), nil
				}
				return captureResponse(http.StatusOK, captureSSE(), true), nil
			}}
			h, rec, initial := newPickWireFixture(t, repo, up, tc.mappedModel)
			router := gin.New()
			router.Use(initial.Handlers...)
			router.Use(func(c *gin.Context) {
				key := c.MustGet(string(middleware.ContextKeyAPIKey)).(*service.APIKey)
				key.Group = &service.Group{ID: newPickWireGroup, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowImageGeneration: true}
				c.Next()
			})
			router.POST("/v1/responses", h.Responses)
			logs, restore := captureHandlerStructuredLog(t)
			defer restore()
			resp := captureRequest(router, "/v1/responses", `{"model":"draw-alias","stream":true,"input":"hi","prompt_cache_key":"image-alias-new-pick"}`)
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			require.Len(t, rec.starts, 1)
			require.Equal(t, tc.wantAccount, rec.starts[0].AccountID)
			require.Equal(t, tc.mappedModel, rec.starts[0].Model)
			require.Equal(t, tc.wantPath, path)
			require.Len(t, up.bodies, 1)
			var outbound struct{ Model string }
			require.NoError(t, json.Unmarshal(up.bodies[0], &outbound))
			require.Equal(t, tc.mappedModel, outbound.Model)
			if tc.wantAccount == base.ID {
				require.False(t, logs.ContainsMessageAtLevel("sched_quality.new_pick", "info"))
			} else {
				value, found := logs.FieldValueForMessage("sched_quality.new_pick", "result")
				require.True(t, found)
				require.Equal(t, "picked", value)
			}
		})
	}
}

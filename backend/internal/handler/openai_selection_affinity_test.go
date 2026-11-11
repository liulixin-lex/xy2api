//go:build unit

package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type selectionAffinityCache struct {
	capturePinnedCache
	session string
}

func (c *selectionAffinityCache) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	if strings.HasPrefix(key, "openai:") {
		c.session = strings.TrimPrefix(key, "openai:")
	}
	return 0, nil
}

func TestOpenAISelectionHandlerFrozenAffinityModel(t *testing.T) {
	for _, name := range []string{"compact", "chat", "codex"} {
		t.Run(name, func(t *testing.T) {
			withHandlerResilience(t, nil)
			account := service.Account{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{4201}, Credentials: map[string]any{"api_key": "key", "base_url": "https://capture.example", "model_mapping": map[string]any{"public": "gpt-5.1"}}}
			path, model, evidence, wire := "/v1/responses/compact", "public", "gpt-5.1", "gpt-5.2"
			body := `{"model":"public","input":"hi","prompt_cache_key":"frozen-model"}`
			capability, compact := service.OpenAIEndpointCapabilityResponses, true
			switch name {
			case "compact":
				account.Extra = map[string]any{"openai_compact_supported": true}
				account.Credentials["compact_model_mapping"] = map[string]any{"public": "gpt-5.2"}
			case "chat":
				path, evidence, wire = "/v1/chat/completions", "public", "gpt-5.1"
				body = `{"model":"public","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"frozen-model"}`
				account.Extra = map[string]any{"openai_passthrough": true}
				capability, compact = service.OpenAIEndpointCapabilityChatCompletions, false
			case "codex":
				path, model, evidence, wire = "/v1/responses", "gpt-5.4-high", "gpt-5.4", "gpt-5.4"
				body = `{"model":"gpt-5.4-high","input":"hi","stream":true,"prompt_cache_key":"frozen-model"}`
				account.Type = service.AccountTypeOAuth
				account.Credentials = map[string]any{"access_token": "token", "chatgpt_account_id": "account"}
				compact = false
			}
			repo := &openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
			var state *resilience.RequestState
			up := &captureUpstream{reply: func(_ int, request *http.Request, _ []byte) (*http.Response, error) {
				state = resilience.StateFrom(request.Context())
				if name == "compact" {
					return captureResponse(200, captureResponseJSON, false), nil
				}
				return captureResponse(200, captureSSE(), true), nil
			}}
			cache := &selectionAffinityCache{}
			h, recorder, router := captureFixture(t, account, up, captureOptions{repo: repo, cache: cache})
			response := captureRequest(router, path, body)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Len(t, recorder.starts, 1)
			start := recorder.starts[0]
			require.Equal(t, evidence, start.Model)
			session := cache.session
			require.NotEmpty(t, session)
			require.Equal(t, wire, gjson.GetBytes(up.bodies[0], "model").String())
			require.NotNil(t, state.EndToEndFirstOutputMs(), response.Body.String())
			require.NotNil(t, state.RecoveryOutcome())
			require.Equal(t, "completed", *state.RecoveryOutcome())
			other := account
			other.ID = 1
			repo.accounts = append(repo.accounts, other)
			group := int64(4201)
			selectAccount := func(session string) int64 {
				selected, _, err := h.gatewayService.SelectAccountWithSchedulerForCapability(context.Background(), &group, "", session, model, nil, service.OpenAIUpstreamTransportHTTPSSE, capability, compact, false, true)
				require.NoError(t, err)
				require.NotNil(t, selected)
				if selected.ReleaseFunc != nil {
					selected.ReleaseFunc()
				}
				return selected.Account.ID
			}
			require.Equal(t, other.ID, selectAccount(""), "without affinity the ID tie-break selects the control account")
			require.Equal(t, account.ID, selectAccount(session), "a real handler success is found under the same frozen evidence key")
		})
	}
}

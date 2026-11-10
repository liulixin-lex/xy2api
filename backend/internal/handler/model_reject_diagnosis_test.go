//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The runtime pool is empty; the diagnosis separately reads persistent support.
type modelRejectAccountRepo struct {
	service.AccountRepository
	accounts     []service.Account
	diagnosisErr error
	platforms    []string
}

func (*modelRejectAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return nil, nil
}

func (*modelRejectAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]service.Account, error) {
	return nil, nil
}

func (*modelRejectAccountRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]service.Account, error) {
	return nil, nil
}

func (*modelRejectAccountRepo) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]service.Account, error) {
	return nil, nil
}

func (r *modelRejectAccountRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, platforms []string, _ bool) ([]service.Account, error) {
	r.platforms = append([]string(nil), platforms...)
	if r.diagnosisErr != nil {
		return nil, r.diagnosisErr
	}
	var result []service.Account
	for _, account := range r.accounts {
		for _, platform := range platforms {
			if account.Platform == platform && account.Status == service.StatusActive && account.Schedulable {
				result = append(result, account)
			}
		}
	}
	return result, nil
}

func modelRejectAccount(platform string, mapping map[string]any) service.Account {
	until := time.Now().Add(time.Hour)
	return service.Account{ID: 1, Platform: platform, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, TempUnschedulableUntil: &until,
		Credentials: map[string]any{"model_mapping": mapping}}
}

func runModelRejectHandler(t *testing.T, route, platform string, repo *modelRejectAccountRepo, mapping map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	group := &service.Group{ID: 7, Platform: platform, Status: service.StatusActive, Hydrated: true}
	key := &service.APIKey{ID: 1, GroupID: &group.ID, Group: group, User: &service.User{ID: 2, Status: service.StatusActive}}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := `{"model":"public-model","messages":[{"role":"user","content":"hello"}],"input":"hello","stream":false}`
	c.Request = httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 2})
	if route == "/v1/messages/count_tokens" {
		gateway := service.NewGatewayService(repo, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil,
			&sessionAnchorHandlerCache{}, cfg, nil, nil, nil, nil, billing, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		(&GatewayHandler{gatewayService: gateway, billingCacheService: billing, cfg: cfg}).CountTokens(c)
	} else {
		var channels *service.ChannelService
		if mapping != nil {
			channels = service.NewChannelService(&openAIWSUsageHandlerChannelRepoStub{
				channels: []service.Channel{{ID: 1, Status: service.StatusActive, GroupIDs: []int64{group.ID},
					ModelMapping: map[string]map[string]string{platform: mapping}}},
				groupPlatforms: map[int64]string{group.ID: platform},
			}, nil, nil, nil, nil)
		}
		gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
			nil, nil, billing, nil, nil, nil, nil, nil, channels, nil, nil, nil)
		h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
			service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
		if route == "/v1/chat/completions" {
			h.ChatCompletions(c)
		} else {
			h.Responses(c)
		}
	}
	return c, recorder
}

func TestModelRejectDiagnosisLocal404IsClient(t *testing.T) {
	withHandlerResilience(t, nil)
	repo := &modelRejectAccountRepo{accounts: []service.Account{modelRejectAccount(service.PlatformOpenAI, map[string]any{"other-model": "other-model"})}}
	c, response := runModelRejectHandler(t, "/v1/responses", service.PlatformOpenAI, repo, nil)
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Equal(t, `{"error":{"message":"Model \"public-model\" is not supported by any configured account in this group","type":"model_not_found"}}`, response.Body.String())
	parsed := parseOpsErrorResponse(response.Body.Bytes())
	phase, limited, owner, source := classifyOpsErrorLog(c, parsed.ErrorType, parsed.Message, parsed.Code, response.Code)
	require.Equal(t, "request", phase)
	require.Equal(t, "client", owner)
	require.Equal(t, "client_request", source)
	require.True(t, limited)
}

func TestModelRejectDiagnosisUsesOpenAIForwardModel(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, route := range []string{"/v1/chat/completions", "/v1/responses"} {
		t.Run(route, func(t *testing.T) {
			repo := &modelRejectAccountRepo{accounts: []service.Account{modelRejectAccount(service.PlatformOpenAI, map[string]any{"actual-model": "actual-model"})}}
			_, response := runModelRejectHandler(t, route, service.PlatformOpenAI, repo, map[string]string{"public-model": "actual-model"})
			require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
			require.Equal(t, `{"error":{"message":"Service temporarily unavailable. Please try again in 30s.","type":"api_error"}}`, response.Body.String())
		})
	}
}

func TestModelRejectDiagnosisUsesCountTokensPlatform(t *testing.T) {
	withHandlerResilience(t, nil)
	repo := &modelRejectAccountRepo{accounts: []service.Account{modelRejectAccount(service.PlatformGemini, map[string]any{"other-model": "other-model"})}}
	_, response := runModelRejectHandler(t, "/v1/messages/count_tokens", service.PlatformGemini, repo, nil)
	require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
	require.Equal(t, []string{service.PlatformGemini, service.PlatformAntigravity}, repo.platforms)
	require.Equal(t, `{"error":{"message":"Model \"public-model\" is not supported by any configured account in this group","type":"model_not_found"},"type":"error"}`, response.Body.String())
}

func TestModelRejectDiagnosis503BoundariesUnchanged(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name           string
		mapping        map[string]any
		empty, failure bool
	}{
		{name: "temporarily_unavailable", mapping: map[string]any{"public-model": "public-model", "actual-model": "actual-model"}},
		{name: "empty_pool", empty: true},
		{name: "diagnosis_failure", failure: true},
		{name: "empty_mapping"},
		{name: "wildcard_mapping", mapping: map[string]any{"*": "actual-model"}},
	} {
		for _, route := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages/count_tokens"} {
			t.Run(tc.name+route, func(t *testing.T) {
				platform := service.PlatformOpenAI
				if route == "/v1/messages/count_tokens" {
					platform = service.PlatformGemini
				}
				repo := &modelRejectAccountRepo{}
				if !tc.empty {
					repo.accounts = []service.Account{modelRejectAccount(platform, tc.mapping)}
				}
				if tc.failure {
					repo.diagnosisErr = errors.New("diagnosis query failed")
				}
				c, response := runModelRejectHandler(t, route, platform, repo, map[string]string{"public-model": "actual-model"})
				require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
				want := `{"error":{"message":"Service temporarily unavailable. Please try again in 30s.","type":"api_error"}}`
				if route == "/v1/messages/count_tokens" {
					want = `{"error":{"message":"Service temporarily unavailable. Please try again in 30s.","type":"api_error"},"type":"error"}`
				}
				require.Equal(t, want, response.Body.String())
				parsed := parseOpsErrorResponse(response.Body.Bytes())
				phase, limited, owner, source := classifyOpsErrorLog(c, parsed.ErrorType, parsed.Message, parsed.Code, response.Code)
				require.Equal(t, "routing", phase)
				require.Equal(t, "platform", owner)
				require.Equal(t, "gateway", source)
				require.False(t, limited)
				t.Logf("BOUNDARY status=%d headers=%v body=%s owner=%s phase=%s business=%v", response.Code, response.Header(), response.Body.String(), owner, phase, limited)
			})
		}
	}
}

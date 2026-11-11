package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayModels_ClaudeAPIKeyProfileIntersection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		mapping map[string]any
		profile string
		want    []string
	}{
		{"unmapped", nil, service.AnthropicCompatProfileClaudeCodeV1, []string{"claude-opus-4-8", "claude-opus-5-5"}},
		{"public aliases", map[string]any{"public-opus": "claude-opus-5-5", "unsupported": "claude-sonnet-4-6"}, service.AnthropicCompatProfileClaudeCodeV1, []string{"public-opus"}},
		{"empty intersection", map[string]any{"unsupported": "claude-sonnet-4-6"}, service.AnthropicCompatProfileClaudeCodeV1, []string{}},
		{"wildcard with exact override", map[string]any{"claude-opus-*": "claude-opus-5-5", "claude-opus-4-8": "unsupported"}, service.AnthropicCompatProfileClaudeCodeV1, nil},
		{"invalid profile", nil, "unknown-profile", []string{}},
	} {
		for _, platform := range []string{service.PlatformAnthropic, service.PlatformComposite} {
			for _, allowlist := range []bool{false, true} {
				t.Run(tc.name+"/"+platform+map[bool]string{false: "/plain", true: "/allowlist"}[allowlist], func(t *testing.T) {
					const groupID int64 = 912
					repo := &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{groupID: {{
						ID: 71, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
						Credentials: map[string]any{"model_mapping": tc.mapping},
						Extra:       map[string]any{service.AnthropicCompatProfileExtraKey: tc.profile},
					}}}}
					h := newGatewayModelsHandlerForTest(repo)
					group := &service.Group{ID: groupID, Platform: platform, ModelAllowlist: service.GroupModelAllowlist{Enabled: allowlist, Models: []string{"*"}}}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: group})
					h.Models(c)
					require.Equal(t, http.StatusOK, rec.Code)
					var got gatewayModelsResponseForTest
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
					ids := modelIDsForTest(got.Data)
					if tc.want == nil {
						require.Contains(t, ids, "claude-opus-5-5")
						require.NotContains(t, ids, "claude-opus-4-8")
						require.NotContains(t, ids, "claude-opus-*")
						for _, id := range ids {
							require.Contains(t, id, "claude-opus-")
						}
					} else {
						require.ElementsMatch(t, tc.want, ids)
					}
					require.ElementsMatch(t, ids, h.codexModelIDsForGroup(context.Background(), group, ""))
				})
			}
		}
	}
}

func TestGatewayModels_ClaudeAPIKeyInvalidationUpdatesRetrievalAndETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: 914, Platform: service.PlatformAnthropic}
	account := service.Account{ID: 71, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"public-opus": "claude-opus-5-5"}},
		Extra:       map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCodeV1}}
	repo := &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{group.ID: {account}}}
	h := newGatewayModelsHandlerForTest(repo)
	request := func(etag string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/models?client_version=0.147.0", nil)
		c.Request.Header.Set("If-None-Match", etag)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: group})
		h.CodexModels(c)
		return rec
	}
	first := request("")
	require.Equal(t, http.StatusOK, first.Code)
	require.Contains(t, first.Body.String(), "public-opus")
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Equal(t, http.StatusNotModified, request(etag).Code)
	require.Equal(t, http.StatusOK, requestModelForTest(h, group, "public-opus", "").Code)
	require.Equal(t, http.StatusNotFound, requestModelForTest(h, group, "claude-sonnet-4-6", "").Code)
	account.Credentials = map[string]any{"model_mapping": map[string]any{"public-opus": "claude-sonnet-4-6"}}
	repo.byGroup[group.ID] = []service.Account{account}
	h.gatewayService.InvalidateAvailableModelsCache(&group.ID, service.PlatformAnthropic)
	updated := request(etag)
	require.Equal(t, http.StatusOK, updated.Code)
	require.NotEqual(t, etag, updated.Header().Get("ETag"))
	var manifest codexModelsResponseForTest
	require.NoError(t, json.Unmarshal(updated.Body.Bytes(), &manifest))
	require.Empty(t, manifest.Models)
	require.Equal(t, http.StatusNotFound, requestModelForTest(h, group, "public-opus", "").Code)
}

func TestGatewayModels_ClaudeAPIKeyCompositeRouteAliases(t *testing.T) {
	group := &service.Group{ID: 915, Platform: service.PlatformComposite}
	repo := &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{group.ID: {{
		ID: 71, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"dispatch-opus": "claude-opus-5-5"}},
		Extra:       map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCodeV1},
	}}}}
	routes := &gatewayModelsRouteRepoStub{routes: []service.CompositeModelRoute{
		{PublicModel: "public-opus", UpstreamModel: "dispatch-opus", TargetPlatform: service.PlatformAnthropic, MatchType: service.CompositeRouteMatchExact, Enabled: true},
		{PublicModel: "unsupported", UpstreamModel: "claude-sonnet-4-6", TargetPlatform: service.PlatformAnthropic, MatchType: service.CompositeRouteMatchExact, Enabled: true},
	}}
	h := newGatewayModelsHandlerForTest(repo, routes)
	require.Equal(t, http.StatusOK, requestModelForTest(h, group, "public-opus", "").Code)
	require.Equal(t, http.StatusNotFound, requestModelForTest(h, group, "unsupported", "").Code)
	require.ElementsMatch(t, []string{"dispatch-opus", "public-opus"}, h.codexModelIDsForGroup(context.Background(), group, ""))
}

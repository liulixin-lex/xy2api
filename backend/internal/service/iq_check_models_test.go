package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type iqTargetModelsTransport struct {
	HTTPUpstream
	calls  int
	status int
	body   string
}

func (u *iqTargetModelsTransport) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: u.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(u.body))}, nil
}
func (u *iqTargetModelsTransport) DoWithTLS(req *http.Request, proxy string, id int64, retries int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, retries)
}

func TestIQTargetModelDiscoveryIsReadOnlyAndCacheIsIdentityScoped(t *testing.T) {
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(kind, func(t *testing.T) {
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: kind, Credentials: map[string]any{
				"api_key": "fixture", "access_token": "fixture", "base_url": "https://example.test",
				"model_mapping": map[string]any{"fake-default": "not-returned"},
			}, Extra: map[string]any{"fixture": "unchanged"}}
			before, err := json.Marshal(account)
			require.NoError(t, err)
			transport := &iqTargetModelsTransport{status: http.StatusOK, body: `{"data":[{"id":"gpt-6-astra"},{"id":"custom-model","supported_reasoning_levels":["ultra","none"]},{"id":"gpt-image-1"}]}`}
			service := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			catalog, err := service.Models(context.Background(), account.ID, false)
			require.NoError(t, err)
			require.Len(t, catalog.Models, 2)
			require.False(t, catalog.FromCache)
			cached, err := service.Models(context.Background(), account.ID, false)
			require.NoError(t, err)
			require.True(t, cached.FromCache)
			require.Equal(t, 1, transport.calls)
			transport.status = http.StatusInternalServerError
			stale, err := service.Models(context.Background(), account.ID, true)
			require.NoError(t, err)
			require.True(t, stale.Stale)
			require.NotEmpty(t, stale.Error)
			require.Equal(t, 2, transport.calls)
			after, err := json.Marshal(account)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}

func TestIQTargetModelDiscoveryCacheKeyTracksIdentityAndTransport(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "one", "model_mapping": map[string]any{"target": "gpt-6-astra"}},
		Extra:       map[string]any{"openai_responses_mode": "force_responses"}}
	original := iqModelDiscoveryCacheKey(account)
	account.Credentials["model_mapping"] = map[string]any{"target": "gpt-6.1-sol"}
	require.NotEqual(t, original, iqModelDiscoveryCacheKey(account))
	account.Credentials["model_mapping"] = map[string]any{"target": "gpt-6-astra"}
	account.Extra["openai_responses_mode"] = "force_chat_completions"
	require.NotEqual(t, original, iqModelDiscoveryCacheKey(account))
}

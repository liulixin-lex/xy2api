package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type iqModelsTransport struct {
	HTTPUpstream
	calls    int
	status   int
	body     string
	requests []*http.Request
}

func (u *iqModelsTransport) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	u.requests = append(u.requests, req)
	return &http.Response{StatusCode: u.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(u.body))}, nil
}
func TestIQCheckModelDiscoveryReadOnly(t *testing.T) {
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(kind, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: kind, Credentials: map[string]any{"base_url": "https://example.test", "api_key": "fixture", "access_token": "fixture", "model_mapping": map[string]any{"fake-default": "not-returned"}}, Extra: map[string]any{"fixture": "unchanged"}, IQCheck: domain.IQCheck{Enabled: true, Status: "degraded", Revision: "original"}}
			original, _ := json.Marshal(a)
			transport := &iqModelsTransport{status: 200, body: `{"data":[{"id":"gpt-6-astra"},{"id":"custom-model","supported_reasoning_levels":["ultra","none","custom_effort"]},{"id":"plain","reasoning":false},{"id":"gpt-image-1"}]}`}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			catalog, err := s.Models(context.Background(), 1, false)
			require.NoError(t, err)
			require.Empty(t, catalog.Error)
			require.Len(t, catalog.Models, 3)
			require.False(t, catalog.FromCache)
			for _, m := range catalog.Models {
				require.Equal(t, "upstream", m.Source)
				require.NotEqual(t, "fake-default", m.ID)
				if m.ID == "custom-model" {
					require.Equal(t, []string{"ultra", "none", "custom_effort"}, m.SupportedReasoningLevels)
					require.Nil(t, m.Reasoning)
					require.Empty(t, m.DefaultReasoningLevel)
					require.Equal(t, "upstream", m.CapabilitySources["supported_reasoning_levels"])
				}
				if m.ID == "gpt-6-astra" {
					require.Equal(t, "reference", m.CapabilitySources["supported_reasoning_levels"])
				}
			}
			cached, err := s.Models(context.Background(), 1, false)
			require.NoError(t, err)
			require.True(t, cached.FromCache)
			require.Equal(t, 1, transport.calls)
			transport.status = 500
			stale, err := s.Models(context.Background(), 1, true)
			require.NoError(t, err)
			require.True(t, stale.Stale)
			require.NotEmpty(t, stale.Error)
			require.Len(t, stale.Models, 3)
			a.IQCheck.Revision = "changed"
			failed, err := s.Models(context.Background(), 1, false)
			require.NoError(t, err)
			require.Empty(t, failed.Models)
			require.NotEmpty(t, failed.Error)
			a.IQCheck.Revision = "original"
			after, _ := json.Marshal(a)
			require.JSONEq(t, string(original), string(after))
			require.Equal(t, http.MethodGet, transport.requests[0].Method)
			// Expired cached data remains explicitly stale, never fabricated as successful discovery.
			s.modelsMu.Lock()
			for key, value := range s.modelsCache {
				past := time.Now().Add(-6 * time.Minute)
				value.FetchedAt = &past
				s.modelsCache[key] = value
			}
			s.modelsMu.Unlock()
			stale, err = s.Models(context.Background(), 1, false)
			require.NoError(t, err)
			require.True(t, stale.Stale)
		})
	}
}

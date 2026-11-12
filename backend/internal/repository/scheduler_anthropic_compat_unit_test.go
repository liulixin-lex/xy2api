//go:build unit

package repository

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type compatProjectedSnapshot struct {
	service.SchedulerCache
	full      *service.Account
	projected service.Account
	loads     int
}

func (s *compatProjectedSnapshot) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	return []*service.Account{&s.projected}, true, nil
}

func (s *compatProjectedSnapshot) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	s.loads++
	if id == s.full.ID {
		return s.full, nil
	}
	return nil, nil
}

type compatProjectionUpstream struct{}

func (compatProjectionUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req,
		Body: io.NopCloser(strings.NewReader(`{"type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"test","signature":"projected-history-signature"},{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}

func (u compatProjectionUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestAnthropicCompatBoundSelectionWithRealSchedulerProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _ := newCompatProvenanceTestCache(t)
	store.anthropicCompatSeed = bytes.Repeat([]byte{1}, 32)
	account := &service.Account{ID: 503, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "synthetic-original-key", "base_url": "https://custom-upstream.example"},
		Extra: map[string]any{service.AnthropicCompatProfileExtraKey: service.AnthropicCompatProfileClaudeCodeV2,
			service.AnthropicCompatIdentitySeedExtraKey: strings.Repeat("ab", 32), service.AnthropicCompatNewSessionsExtraKey: true}}
	snapshot := &compatProjectedSnapshot{full: account, projected: buildSchedulerMetadataAccount(*account)}
	// Assert that the fixture really traverses the production projection: its
	// endpoint is absent, so credential checks on it would use the wrong URL.
	require.NotContains(t, snapshot.projected.Credentials, "base_url")
	require.Equal(t, "https://api.anthropic.com", snapshot.projected.GetBaseURL())
	cfg := &config.Config{}
	gw := service.NewGatewayService(nil, nil, nil, nil, nil, nil, nil, store, cfg,
		service.NewSchedulerSnapshotService(snapshot, nil, nil, nil, cfg), nil, nil,
		service.NewRateLimitService(nil, nil, cfg, nil, nil), nil, nil, compatProjectionUpstream{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	// Produce and register provenance through the real adaptation/response path,
	// using the complete account and a synthetic HTTP port (no network request).
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	ctx, err := gw.PrepareAnthropicCompatibilityForSelection(context.Background(), 1, 2, body, "messages", "")
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)).WithContext(ctx)
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), "anthropic")
	require.NoError(t, err)
	_, err = gw.Forward(ctx, c, account, parsed)
	require.NoError(t, err)
	require.NotEmpty(t, store.rdb.Keys(ctx, "anthropic_compat:provenance:v1:*").Val())
	account.Extra[service.AnthropicCompatNewSessionsExtraKey] = false
	snapshot.projected = buildSchedulerMetadataAccount(*account)
	history := []byte(`{"model":"claude-opus-5-5","max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"thinking","thinking":"test","signature":"projected-history-signature"},{"type":"text","text":"ok"}]},{"role":"user","content":"continue"}]}`)
	selectBound := func(t *testing.T) (*service.AccountSelectionResult, error) {
		t.Helper()
		ctx, err := gw.PrepareAnthropicCompatibilityForSelection(context.Background(), 1, 2, history, "messages", "")
		require.NoError(t, err)
		snapshot.loads = 0
		return gw.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-opus-5-5", nil, "", 0)
	}
	t.Run("same_credentials", func(t *testing.T) {
		selection, err := selectBound(t)
		require.NoError(t, err)
		require.Same(t, account, selection.Account)
		require.Equal(t, 1, snapshot.loads, "all decisions and the result must use one hydrated account")
		selection.ReleaseFunc()
	})
	t.Run("changed_api_key", func(t *testing.T) {
		account.Credentials["api_key"] = "synthetic-rotated-key"
		selection, err := selectBound(t)
		require.Nil(t, selection)
		require.Equal(t, service.AnthropicCompatReasonCredential, service.AnthropicCompatErrorReason(err))
		require.Equal(t, 1, snapshot.loads, "credential rotation must be checked on the full account")
	})
	t.Run("full_account_unavailable", func(t *testing.T) {
		account.Credentials["api_key"] = "synthetic-original-key"
		until := time.Now().Add(time.Hour)
		account.OverloadUntil = &until
		selection, err := selectBound(t)
		require.Nil(t, selection)
		status, _, _, local := service.AnthropicCompatErrorDetails(err)
		require.True(t, local)
		require.Equal(t, http.StatusServiceUnavailable, status, "gates must use current full account state")
		require.Equal(t, 1, snapshot.loads)
	})
}

func TestSchedulerProjectionPreservesAnthropicCompatDrainSetting(t *testing.T) {
	for _, acceptsNew := range []bool{false, true} {
		metadata := buildSchedulerMetadataAccount(service.Account{Extra: map[string]any{service.AnthropicCompatNewSessionsExtraKey: acceptsNew}})
		require.Contains(t, metadata.Extra, service.AnthropicCompatNewSessionsExtraKey)
		require.Equal(t, acceptsNew, metadata.Extra[service.AnthropicCompatNewSessionsExtraKey])
	}
}

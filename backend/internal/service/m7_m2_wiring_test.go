//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/xai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM7ResponsesWirePreparesStop(t *testing.T) {
	withResilienceSettings(t, nil)
	body := []byte(`{"model":"grok","messages":[{"role":"user","content":"hi"}],"stop":["done"],"stream":false,"prompt_cache_key":"stable-session"}`)
	t.Run("legacy", func(t *testing.T) {
		var notices []apicompat.ConversionNotice
		opts := apicompat.ConversionOptions{Record: func(n apicompat.ConversionNotice) { notices = append(notices, n) }}
		normalized, changed, err := normalizeResponsesLegacyIngressForTarget(body, true, opts)
		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gjson.GetBytes(normalized, "stop").Exists())
		require.Contains(t, notices, apicompat.ConversionNotice{Field: "stop", Action: "omit"})
		intermediate, _, err := normalizeResponsesLegacyIngressForTarget(body, false, opts)
		require.NoError(t, err)
		require.Equal(t, `["done"]`, gjson.GetBytes(intermediate, "stop").Raw)
	})
	t.Run("grok", func(t *testing.T) {
		c, rec := compatStreamContext(grokChatRawEndpoint, string(body))
		c.Set("api_key", &APIKey{ID: 7101})
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		c.Request = c.Request.WithContext(ctx)
		account := grokChatBridgeTestAccount(71)
		repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
		upstream := &httpUpstreamRecorder{resp: grokChatBridgeCompletedResponse("resp_grok_stop", 10)}
		svc := &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: NewGrokTokenProvider(repo, nil), accountRepo: repo}
		result, err := svc.forwardGrokChatCompletionsViaResponses(ctx, c, account, body, "stable-session", "")
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, 200, rec.Code)
		require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
		require.False(t, gjson.GetBytes(upstream.lastBody, "stop").Exists())
		require.Contains(t, resilience.SnapshotConversionNotices(ctx), apicompat.ConversionNotice{Field: "stop", Action: "omit"})
	})
}

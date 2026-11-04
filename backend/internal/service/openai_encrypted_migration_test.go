package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestHasEncryptedState(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"agent message", `{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"anything"}]}]}`, true},
		{"tool output array", `{"input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}`, true},
		{"tool output string", `{"input":[{"type":"function_call_output","output":"encrypted_content"}]}`, false},
		{"reasoning", `{"input":[{"type":"reasoning","encrypted_content":"ciphertext"}]}`, false},
		{"compaction", `{"input":[{"type":"compaction","encrypted_content":"ciphertext"}]}`, false},
		{"no input", `{"model":"gpt-test"}`, false},
		{"invalid JSON", `{"input":[{"type":"agent_message","content":[{"type":"encrypted_content"}]}]`, false},
		{"plain agent message", `{"input":[{"type":"agent_message","content":[{"type":"input_text","text":"encrypted_content"}]}]}`, false},
		{"ordinary message", `{"input":[{"type":"message","content":[{"type":"encrypted_content"}]}]}`, false},
		{"input object", `{"input":{"type":"agent_message","content":[{"type":"encrypted_content"}]}}`, false},
		{"content object", `{"input":[{"type":"agent_message","content":{"type":"encrypted_content"}}]}`, false},
		{"output object", `{"input":[{"type":"function_call_output","output":{"type":"encrypted_content"}}]}`, false},
		{"nested tool output", `{"input":[{"type":"function_call_output","output":[{"type":"text","content":[{"type":"encrypted_content"}]}]}]}`, false},
		{"WS envelope", `{"type":"response.create","response":{"input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openAIRequestHasEncryptedState([]byte(tc.body)))
		})
	}
}

func TestWithOpenAIEncryptedContentMigrationGuard(t *testing.T) {
	base := context.Background()
	t.Run("HTTP Responses", func(t *testing.T) {
		ctx := WithOpenAIEncryptedContentMigrationGuard(base, []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content"}]}]}`))
		require.True(t, openAINoStickyMigration(ctx))
		require.False(t, openAINoStickyMigration(base))
		// The existing parent-thread affinity still only recognizes agent_message.
		require.False(t, openAIRequestHasEncryptedAgentMessage([]byte(`{"input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}`)))
	})
	t.Run("WebSocket connection first frames", func(t *testing.T) {
		for _, tc := range []struct {
			body string
			want bool
		}{
			{`{"type":"response.create","input":"first connection"}`, false},
			{`{"type":"response.create","response":{"input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}}`, true},
			{`{"type":"response.create","input":"third connection"}`, false},
		} {
			ctx := WithOpenAIEncryptedContentMigrationGuard(base, []byte(tc.body))
			require.Equal(t, tc.want, openAINoStickyMigration(ctx))
		}
	})
	t.Run("Chat Completions shapes", func(t *testing.T) {
		for _, tc := range []struct {
			body string
			want bool
		}{
			{`{"model":"gpt-test","input":[{"type":"agent_message","content":[{"type":"encrypted_content"}]}]}`, true},
			{`{"model":"gpt-test","messages":[{"role":"user","content":"encrypted_content"}]}`, false},
		} {
			require.Equal(t, tc.want, openAINoStickyMigration(WithOpenAIEncryptedContentMigrationGuard(base, []byte(tc.body))))
		}
	})
	t.Run("preserves continuation guard", func(t *testing.T) {
		ctx := withOpenAIStickyMigrationGuard(base, "resp-chain", OpenAIUpstreamTransportAny)
		require.True(t, openAINoStickyMigration(WithOpenAIEncryptedContentMigrationGuard(ctx, []byte(`{"input":"plain"}`))))
	})
}

func TestEncryptedStateKeepsStickyOwnerAcrossSchedulers(t *testing.T) {
	for _, mode := range []string{"legacy", "advanced", "nil scheduler fallback"} {
		for _, tc := range []struct {
			name    string
			body    string
			exclude bool
			want    int64
		}{
			{"plain migrates", `{"input":"plain"}`, false, 2},
			{"agent ciphertext keeps owner", `{"input":[{"type":"agent_message","content":[{"type":"encrypted_content"}]}]}`, false, 1},
			{"tool ciphertext keeps owner", `{"input":[{"type":"function_call_output","output":[{"type":"encrypted_content"}]}]}`, false, 1},
			{"failure exclusion still applies", `{"input":[{"type":"agent_message","content":[{"type":"encrypted_content"}]}]}`, true, 2},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
				const session = "v2:encrypted-migration"
				group := int64(7)
				cache := &gateV3RebindCache{atomicStickyTestCache: &atomicStickyTestCache{bindings: map[string]int64{"openai:" + session: 1}}}
				advanced := "false"
				if mode == "advanced" {
					advanced = "true"
				}
				svc := &OpenAIGatewayService{
					cache: cache, cfg: newSchedulerTestOpenAIWSV2Config(),
					concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
					accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{
						accounts: []Account{gateV3Account(1, 0), gateV3Account(2, 1)},
					}},
					rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced),
				}
				svc.SetGroupQualityGateView(&schedulingQualityView{enabled: true, tiers: map[int64]int{1: GroupQualityGateTierFail}, targets: map[int64]bool{2: true}})
				ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
				ctx = WithOpenAIEncryptedContentMigrationGuard(ctx, []byte(tc.body))
				var excluded map[int64]struct{}
				if tc.exclude {
					excluded = map[int64]struct{}{1: {}}
				}
				var result *AccountSelectionResult
				var err error
				if mode == "legacy" {
					result, err = svc.SelectAccountWithLoadAwareness(ctx, &group, session, "gpt-test", excluded)
				} else {
					result, _, err = svc.SelectAccountWithScheduler(ctx, &group, "", session, "gpt-test", excluded, OpenAIUpstreamTransportAny, false)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				t.Cleanup(result.ReleaseFunc)
				require.Equal(t, tc.want, result.Account.ID)
				if !tc.exclude {
					require.Equal(t, tc.want, cache.bindings["openai:"+session])
				}
			})
		}
	}
}

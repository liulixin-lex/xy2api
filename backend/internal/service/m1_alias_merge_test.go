//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestM1AliasMergeCapabilities(t *testing.T) {
	for _, variant := range []string{"unique", "conflict", "metadata", "live"} {
		t.Run(variant, func(t *testing.T) {
			accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"team-sol": "gpt-6.1-sol"}}}}
			if variant == "conflict" {
				accounts = append(accounts, Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"team-sol": "gpt-6-luna"}}})
			}
			if variant == "metadata" {
				accounts[0].SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{"gpt-6.1-sol": {
					ID: "gpt-6.1-sol", ContextWindow: 777000, DefaultReasoningLevel: "medium", SupportedReasoningLevels: []string{"low", "medium", "high"},
					CodexToolCapabilities: map[string]json.RawMessage{"supports_search_tool": json.RawMessage("true")},
				}}})
			}
			group := &Group{ID: 1, Platform: PlatformOpenAI}
			svc := &OpenAIGatewayService{accountRepo: codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{1: accounts}}}
			manifest := &OpenAIModelsResponse{Body: []byte(`{"models":[]}`)}
			if variant == "live" {
				manifest.Body = []byte(`{"models":[{"slug":"team-sol","context_window":12345,"unknown":"preserved"}]}`)
				before := string(manifest.Body)
				require.NoError(t, svc.MergeGroupConfiguredCodexModels(context.Background(), group, manifest, ""))
				require.Equal(t, before, string(manifest.Body))
				return
			}
			full, configured, err := svc.BuildGroupConfiguredCodexModelsManifest(context.Background(), group, "")
			require.NoError(t, err)
			require.True(t, configured)
			require.NoError(t, svc.MergeGroupConfiguredCodexModels(context.Background(), group, manifest, ""))
			got := decodeCodexManifestModels(t, manifest.Body)
			want := decodeCodexManifestModels(t, full.Body)
			require.Len(t, got, 1)
			require.Equal(t, "team-sol", got[0]["slug"])
			for _, field := range []string{"supported_reasoning_levels", "default_reasoning_level", "context_window", "max_context_window", "apply_patch_tool_type", "use_responses_lite", "supports_search_tool"} {
				require.Equal(t, want[0][field], got[0][field], field)
			}
		})
	}
}

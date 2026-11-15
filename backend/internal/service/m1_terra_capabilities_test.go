//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestM1TerraCapabilities(t *testing.T) {
	for _, id := range []string{"gpt-6.1-terra", "openai/GPT_6.1_TERRA", "gpt-6.1-terra-none", "gpt-6.1-terra-minimal", "gpt-6.1-terra-openai-compact"} {
		require.True(t, openai.IsGPT61TerraModelSpelling(id), id)
		require.Equal(t, "gpt-6.1-terra", normalizeKnownOpenAICodexModel(id))
		require.True(t, isOpenAIGPT6Model(id))
		require.True(t, openai.RequiresResponsesForToolCalls(id))
		require.True(t, shouldAutoInjectPromptCacheKeyForCompat(id))
		require.NoError(t, openai.ValidateGPT61TerraReasoningEffort(id, "none"))
		require.Error(t, openai.ValidateGPT61TerraReasoningEffort(id, "minimal"))
	}
	for _, id := range []string{"gpt-6.1-terrain", "gpt-6.1-terra-20261101", "gpt-6.1-terra-preview"} {
		require.False(t, openai.IsGPT61TerraModelSpelling(id), id)
		require.False(t, shouldAutoInjectPromptCacheKeyForCompat(id), id)
	}
	require.Contains(t, openai.DefaultModelIDs(), "gpt-6.1-terra")
	descriptor := newConfiguredCodexModelDescriptor("gpt-6.1-terra")
	require.Equal(t, "low", *descriptor.DefaultReasoningLevel)
	require.EqualValues(t, 500000, descriptor.ContextWindow)
	require.EqualValues(t, 872000, descriptor.MaxContextWindow)
	require.Equal(t, openai.CodexBaseInstructionsForModel("gpt-6.1-terra"), descriptor.ModelMessages.InstructionsTemplate)
	require.Equal(t, string(openai.CodexGPT61TerraMetadata), string(descriptor.officialMetadata))
	account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com", "model_mapping": map[string]any{"team-terra": "openai/gpt-6.1-terra-high", "gpt-6.1-terra": "gpt-6.1-terra"}}}
	caps := accountCodexToolCapabilities(&account, "gpt-6.1-terra")
	require.Equal(t, "false", string(caps["use_responses_lite"]))
	adjusted, err := adjustAPIKeyCodexModelsManifest([]byte(`{"models":[{"slug":"team-terra","use_responses_lite":true}]}`), &account)
	require.NoError(t, err)
	require.JSONEq(t, `{"models":[{"slug":"team-terra","use_responses_lite":false}]}`, string(adjusted))
	reasoning := true
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{"gpt-6.1-terra": {ID: "gpt-6.1-terra", ContextWindow: 600000, Reasoning: &reasoning, SupportedReasoningLevels: []string{"low", "medium", "high"}, DefaultReasoningLevel: "medium"}}})
	body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"gpt-6.1-terra"}, []Account{account}, &Group{Platform: PlatformOpenAI}, nil, true)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, body)
	require.EqualValues(t, 600000, models[0]["context_window"])
	require.Equal(t, "medium", models[0]["default_reasoning_level"])
	req := &apicompat.ChatCompletionsRequest{Messages: []apicompat.ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}}
	require.NotEqual(t, deriveCompatPromptCacheKey(req, "gpt-6.1-terra"), deriveCompatPromptCacheKey(req, "gpt-6.1-sol"))
	svc := &OpenAIGatewayService{accountRepo: codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{1: {account}}}}
	group := &Group{ID: 1, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{}}}
	manifest, configured, err := svc.BuildGroupConfiguredCodexModelsManifest(context.Background(), group, "")
	require.NoError(t, err)
	require.True(t, configured)
	require.Empty(t, decodeCodexManifestModels(t, manifest.Body))
}

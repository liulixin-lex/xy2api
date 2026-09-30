package service

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestGPT61SolAliasIdentityRemainsExact(t *testing.T) {
	for _, alias := range []string{"gpt-6.1-sol", "openai/gpt-6.1-sol-max", "GPT_6.1_SOL", "gpt-6.1-sol-openai-compact", " gpt 6.1 sol high "} {
		require.Equal(t, "gpt-6.1-sol", normalizeKnownOpenAICodexModel(alias), alias)
		require.Equal(t, "gpt-6.1-sol", normalizeCodexModel(alias), alias)
		require.Equal(t, "gpt-6.1-sol", NormalizeOpenAICompatRequestedModel(alias), alias)
	}
	for _, model := range []string{"gpt-6.1", "gpt-6.1-solitude", "gpt-6.1-sol-preview", "gpt-6.1-sol-2026-09-29", "gpt-6.1-sol-ultra"} {
		require.Empty(t, normalizeKnownOpenAICodexModel(model), model)
		require.Equal(t, model, normalizeCodexModel(model), model)
		require.False(t, openai.IsGPT61SolModelSpelling(model), model)
	}
}

func TestGPT61SolAPIKeyAliasesDisableLiteAndKeepMetadata(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public": "openai/GPT_6.1_SOL_MAX"}}}
	for _, slug := range []string{"gpt-6.1-sol", "openai/GPT_6.1_SOL_MAX", "public"} {
		body, err := json.Marshal(map[string]any{"models": []any{map[string]any{"slug": slug, "use_responses_lite": true, "supports_search_tool": false, "apply_patch_tool_type": nil, "supported_reasoning_levels": []any{}, "future_capability": map[string]any{"enabled": false}}}})
		require.NoError(t, err)
		out, err := adjustAPIKeyCodexModelsManifest(body, account)
		require.NoError(t, err)
		models := decodeCodexManifestModels(t, out)
		require.Equal(t, false, models[0]["use_responses_lite"], slug)
		require.Equal(t, false, models[0]["supports_search_tool"])
		require.Nil(t, models[0]["apply_patch_tool_type"])
		require.Empty(t, models[0]["supported_reasoning_levels"])
		require.Equal(t, map[string]any{"enabled": false}, models[0]["future_capability"])
	}
}

func TestGPT61SolOfficialDescriptorUnknownFieldsSurvive(t *testing.T) {
	var official map[string]any
	require.NoError(t, json.Unmarshal(openai.CodexGPT61SolMetadata, &official))
	body, err := BuildCodexModelsManifest([]string{"gpt-6.1-sol"})
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, body)
	require.Len(t, models, 1)
	for _, field := range []string{"guardian", "requires_sandboxed_review", "prefer_websockets", "minimal_client_version", "model_messages"} {
		require.Contains(t, models[0], field)
		require.Equal(t, official[field], models[0][field], field)
	}
}

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Follow the same generation, account metadata and client-completion paths as
// 6.1 Sol. These paths deliberately distinguish cached data from client output.
func TestGPT62SuffixCatalogAccountParity(t *testing.T) {
	for _, kind := range []string{"official", "relay", "oauth"} {
		t.Run(kind, func(t *testing.T) {
			ids := []string{"gpt-6.1-sol-high", "gpt-6.2-sol-high", "gpt-6.2-astra-openai-compact"}
			mapping := map[string]any{}
			for _, id := range ids {
				mapping[id] = id
				mapping["alias-"+id] = id
			}
			account := gpt62RelayAPIKeyAccount(mapping)
			if kind == "official" {
				account.Credentials["base_url"] = "https://api.openai.com/v1"
			}
			if kind == "oauth" {
				account.Type = AccountTypeOAuth
				delete(account.Credentials, "base_url")
			}
			for _, alias := range []bool{false, true} {
				slugs := append([]string(nil), ids...)
				if alias {
					for i := range slugs {
						slugs[i] = "alias-" + slugs[i]
					}
				}
				body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, slugs, []Account{*account}, &Group{Platform: PlatformOpenAI}, nil, true)
				require.NoError(t, err)
				models := gpt62ModelsBySlug(t, body)
				for _, slug := range slugs[1:] {
					requireGPT62ParityWith(t, kind+" "+slug, models[slugs[0]], models[slug])
					require.Equal(t, "low", models[slug]["default_reasoning_level"])
					require.EqualValues(t, 872000, models[slug]["max_context_window"])
					require.Equal(t, []any{"text", "image"}, models[slug]["input_modalities"])
					require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromManifestModel(t, models[slug]))
					tiers, ok := models[slug]["service_tiers"].([]any)
					require.True(t, ok)
					require.Len(t, tiers, 1)
					tier, ok := tiers[0].(map[string]any)
					require.True(t, ok)
					require.Equal(t, "priority", tier["id"])
				}
				if account.IsOpenAIApiKey() {
					body, err = adjustAPIKeyCodexModelsManifest(body, account)
					require.NoError(t, err)
					models = gpt62ModelsBySlug(t, body)
					for _, slug := range slugs {
						require.Equal(t, false, models[slug]["use_responses_lite"], slug)
					}
				}
			}
			// A live account snapshot overrides the bundled descriptor even for suffix
			// targets and public mapping aliases, exactly as for 6.1 Sol.
			reasoning := true
			metadata := map[string]UpstreamModelMetadata{}
			for _, id := range ids {
				metadata[id] = UpstreamModelMetadata{ID: id, Reasoning: &reasoning, ContextWindow: 600001, DefaultReasoningLevel: "high", SupportedReasoningLevels: []string{"low", "high"}, InputModalities: []string{"text"}, CodexToolCapabilities: map[string]json.RawMessage{"use_responses_lite": json.RawMessage("true"), "supports_search_tool": json.RawMessage("false")}}
			}
			account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: metadata})
			for _, id := range ids {
				for _, slug := range []string{id, "alias-" + id} {
					body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{slug}, []Account{*account}, &Group{Platform: PlatformOpenAI}, nil, true)
					require.NoError(t, err)
					model := gpt62ModelsBySlug(t, body)[slug]
					require.EqualValues(t, 600001, model["context_window"])
					require.Equal(t, "high", model["default_reasoning_level"])
					require.Equal(t, []string{"low", "high"}, effortsFromManifestModel(t, model))
					require.Equal(t, []any{"text"}, model["input_modalities"])
					require.Equal(t, account.IsOpenAIOAuth(), model["use_responses_lite"])
					require.Equal(t, false, model["supports_search_tool"])
				}
			}
		})
	}
}

//go:build unit

package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGPT62SuffixSnapshotsFollowActualCredentialModel(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		suffix := model + "-high"
		oauth := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"public": suffix, suffix: suffix}}}
		oauth.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{model: {ID: model, ContextWindow: 42000, InputModalities: []string{"text"}}}})
		api := gpt62RelayAPIKeyAccount(map[string]any{"public": suffix, suffix: suffix})
		api.SetUpstreamModelMetadataSnapshot(*oauth.GetUpstreamModelMetadataSnapshot())
		for _, slug := range []string{suffix, "public"} {
			for _, tc := range []struct {
				name       string
				accounts   []Account
				want       int64
				modalities []any
			}{
				{"oauth", []Account{*oauth}, 42000, []any{"text"}},
				{"api-key", []Account{*api}, newConfiguredCodexModelDescriptor(model).ContextWindow, []any{"text", "image"}},
			} {
				t.Run(model+slug+tc.name, func(t *testing.T) {
					body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{slug}, tc.accounts, &Group{Platform: PlatformOpenAI}, nil, true)
					require.NoError(t, err)
					m := gpt62ModelsBySlug(t, body)[slug]
					require.EqualValues(t, tc.want, m["context_window"])
					require.Equal(t, tc.modalities, m["input_modalities"])
				})
			}
		}
		// Literal suffix metadata is authoritative over the base snapshot.
		snapshot := oauth.GetUpstreamModelMetadataSnapshot()
		snapshot.Models[suffix] = UpstreamModelMetadata{ID: suffix, ContextWindow: 21000, InputModalities: []string{"text"}, CodexToolCapabilities: map[string]json.RawMessage{"supports_search_tool": json.RawMessage("false")}}
		oauth.SetUpstreamModelMetadataSnapshot(*snapshot)
		body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"public"}, []Account{*oauth}, &Group{Platform: PlatformOpenAI}, nil, true)
		require.NoError(t, err)
		m := gpt62ModelsBySlug(t, body)["public"]
		require.EqualValues(t, 21000, m["context_window"])
		require.Equal(t, false, m["supports_search_tool"])
	}
}

// Missing metadata keeps the established 6.1 intersection contract: a known
// narrow snapshot plus an unknown candidate falls back to the local descriptor.
func TestGPT62MissingSnapshotKeepsGPT61CatalogParity(t *testing.T) {
	for _, target := range []string{"gpt-6.1-sol", "gpt-6.1-sol-high", "gpt-6.2-sol", "gpt-6.2-astra", "gpt-6.2-sol-high", "gpt-6.2-astra-high"} {
		for _, slug := range []string{target, "public"} {
			for _, typ := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
				known := gpt62RelayAPIKeyAccount(map[string]any{slug: target})
				known.Type = typ
				reasoning := true
				known.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{target: {ID: target, ContextWindow: 42000, InputModalities: []string{"text"}, Reasoning: &reasoning, SupportedReasoningLevels: []string{"low", "high"}, DefaultReasoningLevel: "high"}}})
				missing := gpt62RelayAPIKeyAccount(map[string]any{slug: target})
				missing.ID = 2
				missing.Type = typ
				accounts := []Account{*known, *missing}
				metadata, ok := groupCodexModelMetadata(PlatformOpenAI, slug, accounts, &Group{Platform: PlatformOpenAI}, nil, true)
				require.True(t, ok)
				require.Zero(t, metadata.ContextWindow, target)
				require.Empty(t, metadata.InputModalities, target)
				require.Nil(t, metadata.Reasoning, target)
				body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{slug}, accounts, &Group{Platform: PlatformOpenAI}, nil, true)
				require.NoError(t, err)
				got := gpt62ModelsBySlug(t, body)[slug]
				fallback := newConfiguredCodexModelDescriptor(target)
				require.EqualValues(t, fallback.ContextWindow, got["context_window"], target)
				require.Equal(t, []any{"text", "image"}, got["input_modalities"], target)
				var levels []string
				for _, level := range fallback.SupportedReasoningLevels {
					levels = append(levels, level.Effort)
				}
				require.Equal(t, levels, effortsFromManifestModel(t, got), target)
			}
		}
	}
}

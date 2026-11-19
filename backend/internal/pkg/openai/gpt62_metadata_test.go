package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The embeds are byte-exact slices of openai/codex
// 6c0cee502d215e5df2dd9a303cf2e252229c9e12 codex-rs/models-manager/models.json.
func TestGPT62OfficialMetadataPinned(t *testing.T) {
	for _, tc := range []struct {
		slug   string
		raw    []byte
		length int
		sha256 string
	}{
		{"gpt-6.2-sol", CodexGPT62SolMetadata, 65911, "d3fa1d70564ad1cfe9376af1fde97362cebaefd573d51ed9751896cff41b4750"},
		{"gpt-6.2-astra", CodexGPT62AstraMetadata, 61148, "a9908dd5416ca2370d835d362b98da5f84f80f9207e19109fa873de504ba2f4b"},
	} {
		require.Len(t, tc.raw, tc.length, tc.slug)
		sum := sha256.Sum256(tc.raw)
		require.Equal(t, tc.sha256, hex.EncodeToString(sum[:]), tc.slug)
		var descriptor struct {
			Slug string `json:"slug"`
		}
		require.NoError(t, json.Unmarshal(tc.raw, &descriptor), tc.slug)
		require.Equal(t, tc.slug, descriptor.Slug)
	}
}

func TestGPT62ExactModelSpellings(t *testing.T) {
	for _, id := range []string{"gpt-6.2-sol", "GPT_6.2_SOL", "openai/gpt-6.2-sol", " gpt 6.2 sol "} {
		require.True(t, IsGPT62SolModelSpelling(id), id)
		require.False(t, IsGPT62AstraModelSpelling(id), id)
	}
	for _, id := range []string{"gpt-6.2-astra", "GPT_6.2_ASTRA", "openai/gpt-6.2-astra"} {
		require.True(t, IsGPT62AstraModelSpelling(id), id)
		require.False(t, IsGPT62SolModelSpelling(id), id)
	}
	for _, id := range []string{"", "gpt-6.2", "gpt-6.2-sol-high", "gpt-6.2-sol-max", "gpt-6.2-sol-openai-compact", "gpt-6.2-astra-pro", "gpt-6.2-astra-max", "gpt-6.2-solitude", "gpt-6.1-sol", "gpt-6-astra", "gpt-6.2-terra"} {
		require.False(t, IsGPT62SolModelSpelling(id), id)
		require.False(t, IsGPT62AstraModelSpelling(id), id)
	}
}

func TestCodexOfficialModelMetadata(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  []byte
	}{
		{"gpt-6.1-sol", CodexGPT61SolMetadata},
		{"gpt-6.1-sol-high", CodexGPT61SolMetadata},
		{"openai/GPT_6.1_SOL_MAX", CodexGPT61SolMetadata},
		{"gpt-6.1-terra", CodexGPT61TerraMetadata},
		{"gpt-6.1-terra-none", CodexGPT61TerraMetadata},
		{"gpt-6.2-sol", CodexGPT62SolMetadata},
		{"GPT_6.2_SOL", CodexGPT62SolMetadata},
		{"openai/gpt-6.2-sol", CodexGPT62SolMetadata},
		{"gpt-6.2-astra", CodexGPT62AstraMetadata},
		{"openai/gpt-6.2-astra", CodexGPT62AstraMetadata},
	} {
		got := CodexOfficialModelMetadata(tc.model)
		require.NotNil(t, got, tc.model)
		require.True(t, bytes.Equal(tc.want, got), tc.model)
	}
	for _, model := range []string{"", "gpt-6.2-sol-high", "gpt-6.2", "gpt-6-astra", "gpt-6.2-astra-pro", "gpt-6-sol", "gpt-5.5", "gpt-6.1-sol-ultra"} {
		require.Nil(t, CodexOfficialModelMetadata(model), model)
	}
}

// CodexBaseInstructionsForModel feeds request-side default instructions, so
// GPT-6.2 keeps the v0.6.9 fallback prompt while GPT-6.1 keeps its official
// template. The 6.2 template is advertised only in the models manifest.
func TestGPT62CodexBaseInstructionsUnchanged(t *testing.T) {
	template := func(raw []byte) string {
		var descriptor struct {
			ModelMessages struct {
				InstructionsTemplate string `json:"instructions_template"`
			} `json:"model_messages"`
		}
		require.NoError(t, json.Unmarshal(raw, &descriptor))
		require.NotEmpty(t, descriptor.ModelMessages.InstructionsTemplate)
		return descriptor.ModelMessages.InstructionsTemplate
	}
	for _, tc := range []struct {
		model string
		raw   []byte
	}{
		{"gpt-6.1-sol", CodexGPT61SolMetadata},
		{"gpt-6.1-sol-high", CodexGPT61SolMetadata},
		{"gpt-6.1-terra", CodexGPT61TerraMetadata},
	} {
		got := CodexBaseInstructionsForModel(tc.model)
		require.Equal(t, template(tc.raw), got, tc.model)
		require.True(t, strings.HasPrefix(got, "You are Codex, an agent based on GPT-6."), tc.model)
	}
	for _, raw := range [][]byte{CodexGPT62SolMetadata, CodexGPT62AstraMetadata} {
		require.True(t, strings.HasPrefix(template(raw), "You are Codex, an agent based on GPT-6."))
	}
	for _, model := range []string{"gpt-6.2-sol", "openai/GPT_6.2_SOL", "gpt-6.2-astra", "gpt-6.2", "gpt-6.2-sol-high", "gpt-6.2-astra-pro"} {
		got := CodexBaseInstructionsForModel(model)
		require.Equal(t, latestCodexInstructions(), got, model)
		require.NotEqual(t, template(CodexGPT62SolMetadata), got, model)
		require.NotEqual(t, template(CodexGPT62AstraMetadata), got, model)
	}
}

package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT62ModelID(t *testing.T) {
	for model, want := range map[string]string{
		"gpt-6.2-sol":                  "gpt-6.2-sol",
		"openai/GPT_6.2_Astra":         "gpt-6.2-astra",
		"gpt-6.2-sol-none":             "gpt-6.2-sol",
		"gpt-6.2-sol-minimal":          "gpt-6.2-sol",
		"gpt-6.2-astra-max":            "gpt-6.2-astra",
		"gpt-6.2-astra-openai-compact": "gpt-6.2-astra",
		"gpt-6.2":                      "",
		"gpt-6.2-terra":                "",
		"gpt-6.2-sol-ultra":            "",
		"gpt-6.2-sol-2026-11-17":       "",
		"gpt-6.1-sol":                  "",
		"gpt-6-astra":                  "",
		"":                             "",
	} {
		require.Equal(t, want, GPT62ModelID(model), model)
	}
}

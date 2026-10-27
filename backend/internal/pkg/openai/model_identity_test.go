package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, base, snapshot, effort string }{
		{" OPENAI/GPT-6-ASTRA ", "gpt-6-astra", "", ""},
		{"gpt6-astra", "gpt-6-astra", "", ""},
		{"gpt-6", "gpt-6-astra", "", ""},
		{"gpt-5.6", "gpt-5.6-sol", "", ""},
		{"GPT_6--ASTRA high", "gpt-6-astra", "", "high"},
		{"gpt-6-astra-2026-09-30", "gpt-6-astra", "2026-09-30", ""},
		{"gpt-6-astra-20260930", "gpt-6-astra", "2026-09-30", ""},
		{"gpt-6-astra-high-20260930", "gpt-6-astra", "2026-09-30", "high"},
		{"gpt-6-astra-20260930-high", "gpt-6-astra", "2026-09-30", "high"},
		{"gpt-6-astra-2024-02-29", "gpt-6-astra", "2024-02-29", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			require.Equal(t, ModelID{tc.base, tc.snapshot, tc.effort, true}, ModelIdentity(tc.raw))
		})
	}
	for _, raw := range []string{"", "gpt-6.1", "gpt-5.5-sol", "gpt-4-sol", "gpt-6-astra-2026-02-30", "gpt-6-astra-20260229", "gpt-6-astra-20261301", "gpt-6-astra-0000-01-01", "gpt-6-astra-2026-0930", "gpt-6-astra-low-high", "gpt-6-astra-low-20260930-high", "unknown-provider/gpt-6-astra", "openai/openai/gpt-6-astra", "deployment-a", "claude-opus-4.6", strings.Repeat("a", 201)} {
		require.False(t, ModelIdentity(raw).Known, raw)
	}
}

func TestClassifyDeclaredModelVectors(t *testing.T) {
	const a, s, l = "gpt-6-astra", "gpt-6.1-sol", "gpt-5.6-luna"
	for _, tc := range []struct {
		sent, declared string
		want           ModelClass
	}{
		{a, a, ModelSame}, {a, " OPENAI/GPT-6-ASTRA ", ModelEquivalent}, {a, "gpt6-astra", ModelEquivalent},
		{a, "unknown-provider/gpt-6-astra", ModelUnrecognized}, {a, "gpt-6", ModelEquivalent},
		{s, "gpt-6.1-sol-high", ModelEquivalent}, {a, "gpt-6-astra-2026-09-30", ModelEquivalent},
		{a, "gpt-6-astra-20260930", ModelEquivalent}, {a, "gpt-6-astra-2026-02-30", ModelUnrecognized},
		{"gpt-6-astra-2026-09-30", "gpt-6-astra-2026-10-01", ModelSnapshotDrift},
		{"gpt-6-astra-2026-09-30", a, ModelSnapshotDrift},
		{"gpt-6-sol", s, ModelVersionUp}, {"gpt-5.6-sol", "gpt-6-sol", ModelVersionUp},
		{a, l, ModelDowngrade}, {s, l, ModelDowngrade}, {"gpt-6-sol", l, ModelDowngrade},
		{"gpt-5.6-sol", l, ModelDowngrade}, {"gpt-5.6-terra", l, ModelDowngrade},
		{s, "gpt-6-sol", ModelVersionDown}, {a, s, ModelDifferent}, {s, "gpt-6.1", ModelUnrecognized},
		{a, "claude-opus-4.6", ModelUnrecognized}, {"codex-auto-review", l, ModelUnrecognized},
		{"deployment-a", a, ModelUnrecognized}, {a, "", ModelUndeclared},
		{"gemini-3.8-flash", "gemini-3.8-flash-high", ModelUnrecognized},
		{"deployment-a", " DEPLOYMENT-A ", ModelSame},
	} {
		t.Run(tc.sent+"/"+tc.declared, func(t *testing.T) {
			require.Equal(t, tc.want, ClassifyDeclaredModel(tc.sent, tc.declared, nil))
		})
	}
	require.Equal(t, ModelDowngrade, ClassifyDeclaredModel(s, "gpt-6-sol", []DowngradeRule{{"openai/gpt-6.1-sol-high", "gpt-6-sol-20260930"}}))
}

func TestClassifyDeclarations(t *testing.T) {
	const a, s, l = "gpt-6-astra", "gpt-6.1-sol", "gpt-5.6-luna"
	for _, tc := range []struct {
		name             string
		body, header     []string
		prefer           bool
		selected         DeclarationSelection
		class            ModelClass
		reported, source string
		wrong, unknown   bool
		flags            []string
	}{
		{"undeclared", nil, nil, false, DeclarationSelection{}, ModelUndeclared, "", "body", false, false, nil},
		{"equivalent terminal", []string{a, "gpt-6-astra-2026-09-30"}, nil, false, DeclarationSelection{Body: "gpt-6-astra-2026-09-30"}, ModelEquivalent, "gpt-6-astra-2026-09-30", "body", false, false, nil},
		{"created downgrade", []string{l, a}, nil, false, DeclarationSelection{Body: a}, ModelEventConflict, a, "body", false, true, []string{"event_conflict"}},
		{"usage tail downgrade", []string{a, a, l}, nil, false, DeclarationSelection{Body: l}, ModelEventConflict, l, "body", false, true, []string{"event_conflict"}},
		{"oauth header", []string{a}, []string{l}, true, DeclarationSelection{}, ModelDowngrade, l, "header", true, false, []string{"source_disagreement"}},
		{"oauth changing header", []string{a}, []string{a, l}, true, DeclarationSelection{Header: l}, ModelEventConflict, l, "header", false, true, []string{"source_disagreement", "event_conflict"}},
		{"api body", []string{a}, []string{l}, false, DeclarationSelection{}, ModelSame, a, "body", false, false, []string{"source_disagreement"}},
		{"oauth fallback", []string{a}, nil, true, DeclarationSelection{}, ModelSame, a, "body", false, false, nil},
		{"harmless conflict", []string{a, s}, nil, false, DeclarationSelection{Body: s}, ModelEventConflict, s, "body", false, false, []string{"event_conflict"}},
		{"all downgrade", []string{l, "gpt-5.6-luna-20260930", "gpt-5.6-luna-20261001"}, nil, false, DeclarationSelection{}, ModelDowngrade, l, "body", true, false, nil},
		{"blank ignored", []string{a, " "}, nil, false, DeclarationSelection{}, ModelSame, a, "body", false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyDeclarations(a, tc.body, tc.header, tc.prefer, nil, tc.selected)
			require.Equal(t, DeclarationResult{Class: tc.class, ReportedModel: tc.reported, Source: tc.source, Wrong: tc.wrong, Unknown: tc.unknown, Flags: tc.flags}, got)
		})
	}
	got := ClassifyDeclarations(a, []string{l, s}, nil, false, []DowngradeRule{{a, s}}, DeclarationSelection{})
	require.True(t, got.Wrong)
	require.Equal(t, ModelDowngrade, got.Class)
	require.True(t, EquivalentModels(a, "openai/gpt-6-astra-20260930-high"))
	require.False(t, EquivalentModels("gpt-6-astra-20260930", "gpt-6-astra-20261001"))
}

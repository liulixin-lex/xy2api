package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIFCOCompatSnapshotCapturesSettingsAndIngressBody(t *testing.T) {
	cfg := resilience.Defaults()
	cfg.Enabled = true
	cfg.FCOCompat.Mode = resilience.FCOCompatModeProactive
	cfg.FCOCompat.AdminNames = []string{"custom_name"}

	clean := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x"}]}`)
	snapshot := NewOpenAIFCOCompatSnapshot(&cfg, clean)
	require.Equal(t, resilience.FCOCompatModeProactive, snapshot.GlobalMode())
	require.Equal(t, []string{"custom_name"}, snapshot.Rules.AdminNames)
	require.False(t, snapshot.IngressHasDuplicateMembers)
	require.True(t, snapshot.IngressMayCarryCandidates)

	duplicate := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x","output":"y"}]}`)
	require.True(t, NewOpenAIFCOCompatSnapshot(&cfg, duplicate).IngressHasDuplicateMembers)

	// A body that cannot be rewritten is not scanned: no function_call_output
	// text, no "agent" string, or the global mode is off.
	unrelated := []byte(`{"model":"m","model":"n","input":"hi"}`)
	require.False(t, NewOpenAIFCOCompatSnapshot(&cfg, unrelated).IngressHasDuplicateMembers)
	paired := []byte(`{"model":"m","model":"n","input":[{"type":"function_call_output","call_id":"c","output":"the \"agent\" ran"}]}`)
	require.False(t, NewOpenAIFCOCompatSnapshot(&cfg, paired).IngressHasDuplicateMembers)
	require.False(t, NewOpenAIFCOCompatSnapshot(&cfg, paired).IngressMayCarryCandidates)
	cfg.FCOCompat.Mode = resilience.FCOCompatModeOff
	require.False(t, NewOpenAIFCOCompatSnapshot(&cfg, duplicate).IngressHasDuplicateMembers)
	cfg.FCOCompat.Mode = resilience.FCOCompatModeProactive
	cfg.Enabled = false
	require.False(t, NewOpenAIFCOCompatSnapshot(&cfg, duplicate).IngressHasDuplicateMembers)
}

func TestOpenAIFCOCompatSnapshotModeForAccountOverride(t *testing.T) {
	cfg := resilience.Defaults()
	cfg.Enabled = true
	cfg.FCOCompat.Mode = resilience.FCOCompatModeReactive
	snapshot := NewOpenAIFCOCompatSnapshot(&cfg, nil)

	override := &Account{Extra: map[string]any{OpenAIFCOCompatExtraKey: resilience.FCOCompatModeProactive}}
	require.Equal(t, resilience.FCOCompatModeProactive, snapshot.ModeFor(override))
	require.Equal(t, resilience.FCOCompatModeReactive, snapshot.ModeFor(&Account{}))

	cfg.FCOCompat.Mode = resilience.FCOCompatModeOff
	require.Equal(t, resilience.FCOCompatModeOff, NewOpenAIFCOCompatSnapshot(&cfg, nil).ModeFor(override))

	require.Equal(t, resilience.FCOCompatModeOff, NewOpenAIFCOCompatSnapshot(nil, nil).GlobalMode())
}

func TestOpenAIFCOCompatSnapshotContextRoundTrip(t *testing.T) {
	_, ok := OpenAIFCOCompatSnapshotFromContext(context.Background())
	require.False(t, ok)

	stored := OpenAIFCOCompatSnapshot{IngressHasDuplicateMembers: true}
	ctx := WithOpenAIFCOCompatSnapshot(context.Background(), stored)
	got, ok := OpenAIFCOCompatSnapshotFromContext(ctx)
	require.True(t, ok)
	require.True(t, got.IngressHasDuplicateMembers)
	require.True(t, OpenAIFCOCompatSnapshotOrCurrent(ctx, nil).IngressHasDuplicateMembers)
}

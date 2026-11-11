package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeAPIKeyCompatV2EnvelopeNormalizesOnlyLeadingMarkers(t *testing.T) {
	profile := &claudeCodeV2CompatProfile
	identity := map[string]any{"type": "text", "text": profile.SystemIdentity, "cache_control": map[string]string{"type": "ephemeral"}}
	billing := map[string]string{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.1.abc; cc_entrypoint=cli; cch=abcd;"}
	business := map[string]any{"type": "text", "text": "Preserve this quoted identity: " + profile.SystemIdentity, "cache_control": map[string]string{"type": "ephemeral"}}
	for _, system := range [][]any{{billing, identity, business}, {identity, billing, identity, billing, business}, {business}} {
		body, err := json.Marshal(map[string]any{"system": system, "metadata": map[string]string{"trace": "keep"}, "messages": []any{map[string]string{"role": "user", "content": "hello"}}})
		require.NoError(t, err)
		wire, err := compatInjectBody(body, profile, "derived")
		require.NoError(t, err)
		blocks := gjson.GetBytes(wire, "system").Array()
		require.Len(t, blocks, 3)
		require.Contains(t, blocks[0].Get("text").String(), "cc_version=2.1.114.")
		require.NotContains(t, blocks[0].Get("text").String(), "cch=")
		require.Equal(t, profile.SystemIdentity, blocks[1].Get("text").String())
		require.Equal(t, gjson.GetBytes(body, "system").Array()[len(system)-1].Raw, blocks[2].Raw)
		require.Equal(t, "keep", gjson.GetBytes(wire, "metadata.trace").String())
		require.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(wire, "messages").Raw)
		retry, err := compatInjectBody(wire, profile, "derived")
		require.NoError(t, err)
		require.Equal(t, wire, retry)
	}
	for _, system := range [][]any{
		{business, identity},
		{map[string]string{"type": "text", "text": "x-anthropic-billing-header: unknown=1;"}},
		{map[string]any{"type": "text", "text": billing["text"], "cache_control": map[string]string{"type": "ephemeral"}}},
		{identity, map[string]string{"type": "text", "text": profile.SystemIdentity}},
	} {
		body, err := json.Marshal(map[string]any{"system": system})
		require.NoError(t, err)
		_, err = compatInjectBody(body, profile, "derived")
		require.Error(t, err)
	}
	legacy, err := json.Marshal(map[string]any{"system": []any{billing, identity}})
	require.NoError(t, err)
	_, err = compatInjectBody(legacy, &claudeCodeV1CompatProfile, "derived")
	require.ErrorContains(t, err, "exactly once at the start", "v1 wire behavior remains frozen")
}

func TestClaudeAPIKeyCompatV2NativeRecognitionMatchesBuilder(t *testing.T) {
	a := compatTestAccount()
	a.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileClaudeCodeV2
	s := compatTestService(newCompatMemoryStore())
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "messages", "")
	require.NoError(t, err)
	req, wire, _ := compatBuild(t, s, compatTestContext(), a, ctx, body)
	headers := make(http.Header)
	for key, values := range req.Header {
		for _, value := range values {
			headers.Add(key, value)
		}
	}
	require.True(t, compatCompleteNative(wire, headers, &claudeCodeV2CompatProfile))
	ctx, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, wire, "messages", "", headers)
	require.NoError(t, err)
	require.True(t, compatScope(ctx).NativeCompatibleV2)
	require.NoError(t, s.anthropicCompatCandidateEligibility(ctx, a, "claude-opus-5-5"))

	withoutBilling, err := sjson.DeleteBytes(wire, "system.0")
	require.NoError(t, err)
	require.False(t, compatCompleteNativeV2(withoutBilling, headers, &claudeCodeV2CompatProfile), "V2 native recognition requires the canonical billing block")
	ctx, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, withoutBilling, "messages", "", headers)
	require.NoError(t, err)
	require.False(t, compatScope(ctx).NativeCompatibleV2)
	_, repairedWire, _ := compatBuild(t, s, compatTestContext(), a, ctx, withoutBilling)
	require.True(t, compatIsBillingHeaderBlock(gjson.GetBytes(repairedWire, "system.0")))
}

func compatV2NativeRequestFixture(t *testing.T) (*GatewayService, *Account, context.Context, []byte, http.Header) {
	t.Helper()
	a := compatTestAccount()
	a.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileClaudeCodeV2
	s := compatTestService(newCompatMemoryStore())
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, body, "messages", "")
	require.NoError(t, err)
	req, wire, _ := compatBuild(t, s, compatTestContext(), a, ctx, body)
	headers := make(http.Header)
	for key, values := range req.Header {
		for _, value := range values {
			headers.Add(key, value)
		}
	}
	require.True(t, compatCompleteNativeV2(wire, headers, &claudeCodeV2CompatProfile))
	ctx, err = s.PrepareAnthropicCompatibility(context.Background(), 11, 22, wire, "messages", "", headers)
	require.NoError(t, err)
	require.True(t, compatScope(ctx).NativeCompatibleV2)
	return s, a, ctx, wire, headers
}

func TestClaudeAPIKeyCompatV2NativeCandidateChecksModel(t *testing.T) {
	s, a, _, wire, headers := compatV2NativeRequestFixture(t)
	unsupportedWire, err := sjson.SetBytes(wire, "model", "claude-opus-4-6")
	require.NoError(t, err)
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, unsupportedWire, "messages", "", headers)
	require.NoError(t, err)
	require.True(t, compatScope(ctx).NativeCompatibleV2)
	require.ErrorContains(t, s.anthropicCompatCandidateEligibility(ctx, a, "claude-opus-4-6"), "outside the compatibility profile")
}

func TestClaudeAPIKeyCompatV2NativePlanChecksModel(t *testing.T) {
	s, a, _, wire, headers := compatV2NativeRequestFixture(t)
	unsupportedWire, err := sjson.SetBytes(wire, "model", "claude-opus-4-6")
	require.NoError(t, err)
	ctx, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, unsupportedWire, "messages", "", headers)
	require.NoError(t, err)
	nativeContext := compatTestContext()
	nativeContext.Request.Header = headers.Clone()
	_, err = s.newAnthropicCompatPlan(ctx, nativeContext, a, unsupportedWire, "claude-opus-4-6")
	require.ErrorContains(t, err, "outside the compatibility profile")
}

func TestClaudeAPIKeyCompatV2NativeCandidateChecksRequiredBetas(t *testing.T) {
	s, a, ctx, _, _ := compatV2NativeRequestFixture(t)
	settings := &BetaPolicySettings{Rules: []BetaPolicyRule{{BetaToken: claudeCodeV2CompatProfile.RequiredBetas[0], Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll}}}
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	s.settingService = NewSettingService(&compatPolicySettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{SettingKeyBetaPolicySettings: string(raw)}}}, &config.Config{})
	require.ErrorContains(t, s.anthropicCompatCandidateEligibility(ctx, a, "claude-opus-5-5"), "filters a required compatibility")
}

func TestClaudeAPIKeyCompatV2NativePlanChecksRequiredBetas(t *testing.T) {
	s, a, ctx, wire, headers := compatV2NativeRequestFixture(t)
	settings := &BetaPolicySettings{Rules: []BetaPolicyRule{{BetaToken: claudeCodeV2CompatProfile.RequiredBetas[0], Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll}}}
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	s.settingService = NewSettingService(&compatPolicySettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{SettingKeyBetaPolicySettings: string(raw)}}}, &config.Config{})
	nativeContext := compatTestContext()
	nativeContext.Request.Header = headers.Clone()
	_, err = s.newAnthropicCompatPlan(ctx, nativeContext, a, wire, "claude-opus-5-5")
	require.ErrorContains(t, err, "filters a required compatibility")
}

func TestClaudeAPIKeyCompatProfileMigrationDrainsBoundRevision(t *testing.T) {
	store := newCompatMemoryStore()
	s, plan, ctx := compatObserverFixture(t, store)
	require.NoError(t, s.registerAnthropicCompat(ctx, plan, []anthropicCompatOpaque{{"thinking", "migration-signature"}}))
	a := compatTestAccount()
	a.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileClaudeCodeV2
	a.Extra[AnthropicCompatNewSessionsExtraKey] = false
	history := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"thinking","signature":"migration-signature"}]},{"role":"user","content":"next"}]}`)
	bound, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, history, "messages", "")
	require.NoError(t, err)
	require.NoError(t, s.anthropicCompatCandidateEligibility(bound, a, "claude-opus-5-5"))
	_, wire, continued := compatBuild(t, s, compatTestContext(), a, bound, history)
	require.Equal(t, claudeCodeV1CompatProfile.Revision, continued.Record.ProfileRevision)
	require.Equal(t, plan.Record, continued.Record)
	require.Equal(t, claudeCodeSystemPrompt, gjson.GetBytes(wire, "system.0.text").String())
	newBody := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"new"}]}`)
	unbound, err := s.PrepareAnthropicCompatibility(context.Background(), 11, 22, newBody, "messages", "")
	require.NoError(t, err)
	require.ErrorContains(t, s.anthropicCompatCandidateEligibility(unbound, a, "claude-opus-5-5"), "draining")
	_, err = s.newAnthropicCompatPlan(unbound, compatTestContext(), a, newBody, "claude-opus-5-5")
	require.ErrorContains(t, err, "draining")
	a.Extra[AnthropicCompatNewSessionsExtraKey] = true
	_, wire, fresh := compatBuild(t, s, compatTestContext(), a, unbound, newBody)
	require.Equal(t, claudeCodeV2CompatProfile.Revision, fresh.Record.ProfileRevision)
	require.True(t, strings.HasPrefix(gjson.GetBytes(wire, "system.0.text").String(), "x-anthropic-billing-header:"))
	a.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileOff
	require.Error(t, s.guardAnthropicCompatAccount(bound, a))
}

func TestAnthropicCompatCapabilityDoesNotDependOnHeaderReadiness(t *testing.T) {
	a := compatTestAccount()
	a.Extra[AnthropicCompatProfileExtraKey] = AnthropicCompatProfileClaudeCodeV2
	a.Credentials[credKeyHeaderOverrideEnabled] = true
	a.Credentials[credKeyHeaderOverrides] = map[string]any{"user-agent": "conflicting-client"}
	require.True(t, anthropicCompatModelSupported(a, "claude-opus-5-5"))
	_, err := ResolveAnthropicCompatProfile(a)
	require.Error(t, err)
	require.Error(t, compatTestService(newCompatMemoryStore()).anthropicCompatCandidateEligibility(context.Background(), a, "claude-opus-5-5"))
}

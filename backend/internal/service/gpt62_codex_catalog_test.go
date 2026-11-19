package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

var gpt62CodexCatalogCases = []struct {
	model         string
	raw           []byte
	contextWindow int64
}{
	{"gpt-6.2-sol", openai.CodexGPT62SolMetadata, 500000},
	{"gpt-6.2-astra", openai.CodexGPT62AstraMetadata, 272000},
}

// gpt62ParityFields are the routing-sensitive Codex fields that must match
// gpt-6.1-sol on every manifest path.
var gpt62ParityFields = []string{"use_responses_lite", "tool_mode", "apply_patch_tool_type", "supports_search_tool"}

func gpt62OfficialInstructionsTemplate(t *testing.T, raw []byte) string {
	t.Helper()
	var descriptor struct {
		ModelMessages struct {
			InstructionsTemplate string `json:"instructions_template"`
		} `json:"model_messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &descriptor))
	require.NotEmpty(t, descriptor.ModelMessages.InstructionsTemplate)
	return descriptor.ModelMessages.InstructionsTemplate
}

func gpt62RelayAPIKeyAccount(mapping map[string]any) *Account {
	credentials := map[string]any{"api_key": "sk-upstream", "base_url": "https://relay.example.com/v1"}
	if mapping != nil {
		credentials["model_mapping"] = mapping
	}
	return &Account{ID: 62, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: credentials, Concurrency: 1}
}

func gpt62ModelsBySlug(t *testing.T, body []byte) map[string]map[string]any {
	t.Helper()
	bySlug := make(map[string]map[string]any)
	for _, model := range decodeCodexManifestModels(t, body) {
		slug, ok := model["slug"].(string)
		require.True(t, ok)
		bySlug[slug] = model
	}
	return bySlug
}

func requireGPT62ParityWith(t *testing.T, path string, reference, model map[string]any) {
	t.Helper()
	require.NotNil(t, reference, path)
	require.NotNil(t, model, path)
	for _, field := range gpt62ParityFields {
		require.Contains(t, reference, field, "%s: %s", path, field)
		require.Contains(t, model, field, "%s: %s", path, field)
		require.Equal(t, reference[field], model[field], "%s: %s", path, field)
	}
}

func TestGPT62OfflineCodexCatalog(t *testing.T) {
	for _, tc := range gpt62CodexCatalogCases {
		body, err := BuildCodexModelsManifest([]string{tc.model})
		require.NoError(t, err)
		models := decodeCodexManifestModels(t, body)
		require.Len(t, models, 1)
		m := models[0]
		require.Equal(t, tc.model, m["slug"])
		require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromManifestModel(t, m), tc.model)
		require.Equal(t, "low", m["default_reasoning_level"], tc.model)
		require.EqualValues(t, tc.contextWindow, m["context_window"], tc.model)
		require.EqualValues(t, 872000, m["max_context_window"], tc.model)
		require.Equal(t, []any{"text", "image"}, m["input_modalities"], tc.model)
		require.Equal(t, "xhigh", m["multi_agent_reasoning_effort"], tc.model)
		require.Equal(t, "v2", m["multi_agent_version"], tc.model)
		require.Nil(t, m["default_service_tier"], tc.model)
		tiers, ok := m["service_tiers"].([]any)
		require.True(t, ok, tc.model)
		require.Len(t, tiers, 1, tc.model)
		tier, ok := tiers[0].(map[string]any)
		require.True(t, ok, tc.model)
		require.Equal(t, OpenAIFastTierPriority, tier["id"], tc.model)
	}
}

func TestGPT62DescriptorUsesOfficialMetadata(t *testing.T) {
	for _, tc := range gpt62CodexCatalogCases {
		descriptor := newConfiguredCodexModelDescriptor(tc.model)
		require.Equal(t, tc.model, descriptor.Slug)
		require.Equal(t, "low", *descriptor.DefaultReasoningLevel)
		require.EqualValues(t, tc.contextWindow, descriptor.ContextWindow)
		require.EqualValues(t, 872000, descriptor.MaxContextWindow)
		require.Equal(t, gpt62OfficialInstructionsTemplate(t, tc.raw), descriptor.ModelMessages.InstructionsTemplate)
		require.True(t, strings.HasPrefix(descriptor.ModelMessages.InstructionsTemplate, "You are Codex, an agent based on GPT-6."))
		require.Equal(t, string(tc.raw), string(descriptor.officialMetadata))
	}
	// Provider-prefixed and separator spellings keep their own slug.
	descriptor := newConfiguredCodexModelDescriptor("openai/GPT_6.2_SOL")
	require.Equal(t, "openai/GPT_6.2_SOL", descriptor.Slug)
	require.Equal(t, string(openai.CodexGPT62SolMetadata), string(descriptor.officialMetadata))
	// Suffix spellings have no local alias and keep the generic descriptor.
	for _, model := range []string{"gpt-6.2-sol-high", "gpt-6.2-astra-max", "gpt-6.2"} {
		descriptor := newConfiguredCodexModelDescriptor(model)
		require.Empty(t, descriptor.officialMetadata, model)
		require.Equal(t, "none", *descriptor.DefaultReasoningLevel, model)
	}
}

func TestGPT62OfficialDescriptorUnknownFieldsSurvive(t *testing.T) {
	for _, tc := range gpt62CodexCatalogCases {
		var official map[string]any
		require.NoError(t, json.Unmarshal(tc.raw, &official))
		body, err := BuildCodexModelsManifest([]string{tc.model})
		require.NoError(t, err)
		models := decodeCodexManifestModels(t, body)
		require.Len(t, models, 1)
		for _, field := range []string{"allow_only_codex_cloud_agent_orchestrator", "allow_only_chatgpt_workspace_executor", "minimal_client_version", "model_messages"} {
			require.Contains(t, official, field, tc.model)
			require.Contains(t, models[0], field, tc.model)
			require.Equal(t, official[field], models[0][field], "%s: %s", tc.model, field)
		}
		messages, ok := models[0]["model_messages"].(map[string]any)
		require.True(t, ok)
		require.Contains(t, messages, "apps", tc.model)
		// Same as gpt-6.1-sol: only the account-derived search capability
		// differs from the official descriptor in the offline catalog.
		for field, value := range official {
			if field == "supports_search_tool" {
				continue
			}
			require.Equal(t, value, models[0][field], "%s: %s", tc.model, field)
		}
	}
}

func TestGPT62APIKeyCatalogUsesFullResponses(t *testing.T) {
	for _, base := range []string{"https://api.openai.com", "https://relay.example.com/v1"} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": base}}
		for _, tc := range gpt62CodexCatalogCases {
			body, err := completeAPIKeyCodexModelsManifestMetadata([]byte(`{"models":[{"slug":"`+tc.model+`"}]}`), true, account)
			require.NoError(t, err)
			body, err = adjustAPIKeyCodexModelsManifest(body, account)
			require.NoError(t, err)
			models := decodeCodexManifestModels(t, body)
			require.Len(t, models, 1)
			require.Equal(t, false, models[0]["use_responses_lite"], "%s %s", base, tc.model)
			require.Equal(t, "low", models[0]["default_reasoning_level"], "%s %s", base, tc.model)
			require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromManifestModel(t, models[0]))
		}
	}
}

func TestGPT62APIKeyAliasesDisableLiteAndKeepMetadata(t *testing.T) {
	for _, target := range []string{"openai/GPT_6.2_SOL", "gpt-6.2-astra"} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public": target}}}
		for _, slug := range []string{"gpt-6.2-sol", "gpt-6.2-astra", target, "public"} {
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
	// Suffix spellings are not 6.2 aliases and are left untouched.
	account := gpt62RelayAPIKeyAccount(map[string]any{"public": "gpt-6.2-sol-high"})
	body := []byte(`{"models":[{"slug":"public","use_responses_lite":true}]}`)
	out, err := adjustAPIKeyCodexModelsManifest(body, account)
	require.NoError(t, err)
	require.Equal(t, string(body), string(out))
}

func TestGPT62GroupConfiguredCatalogMatchesGPT61Sol(t *testing.T) {
	ids := []string{"gpt-6.1-sol", "gpt-6.2-sol", "gpt-6.2-astra"}
	mapping := map[string]any{}
	for _, id := range ids {
		mapping[id] = id
	}
	account := gpt62RelayAPIKeyAccount(mapping)
	svc := &OpenAIGatewayService{accountRepo: codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{1: {*account}}}}
	manifest, configured, err := svc.BuildGroupConfiguredCodexModelsManifest(context.Background(), &Group{ID: 1, Platform: PlatformOpenAI}, "")
	require.NoError(t, err)
	require.True(t, configured)
	models := gpt62ModelsBySlug(t, manifest.Body)
	require.Len(t, models, len(ids))
	// The production baseline for gpt-6.1-sol on a relay API key account.
	require.Equal(t, true, models["gpt-6.1-sol"]["use_responses_lite"])
	require.Equal(t, "code_mode_only", models["gpt-6.1-sol"]["tool_mode"])
	require.Equal(t, "freeform", models["gpt-6.1-sol"]["apply_patch_tool_type"])
	for _, tc := range gpt62CodexCatalogCases {
		requireGPT62ParityWith(t, "group configured "+tc.model, models["gpt-6.1-sol"], models[tc.model])
		require.Equal(t, "low", models[tc.model]["default_reasoning_level"])
		require.EqualValues(t, tc.contextWindow, models[tc.model]["context_window"])
		require.Equal(t, []any{"text", "image"}, models[tc.model]["input_modalities"])
	}

	// buildCodexModelsManifestForAccounts directly, for every account kind.
	for _, candidate := range []*Account{
		gpt62RelayAPIKeyAccount(nil),
		{ID: 63, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-upstream", "base_url": "https://api.openai.com/v1"}},
		{ID: 64, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "token"}},
	} {
		body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, ids, []Account{*candidate}, &Group{Platform: PlatformOpenAI}, nil, true)
		require.NoError(t, err)
		models := gpt62ModelsBySlug(t, body)
		for _, tc := range gpt62CodexCatalogCases {
			requireGPT62ParityWith(t, candidate.Type+" "+candidate.GetCredential("base_url")+" "+tc.model, models["gpt-6.1-sol"], models[tc.model])
		}
	}
}

func TestGPT62UpstreamSnapshotRemainsAuthoritative(t *testing.T) {
	account := gpt62RelayAPIKeyAccount(map[string]any{"gpt-6.2-sol": "gpt-6.2-sol"})
	reasoning := true
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{"gpt-6.2-sol": {ID: "gpt-6.2-sol", ContextWindow: 600000, Reasoning: &reasoning, SupportedReasoningLevels: []string{"low", "medium", "high"}, DefaultReasoningLevel: "medium"}}})
	body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"gpt-6.2-sol"}, []Account{*account}, &Group{Platform: PlatformOpenAI}, nil, true)
	require.NoError(t, err)
	models := decodeCodexManifestModels(t, body)
	require.EqualValues(t, 600000, models[0]["context_window"])
	require.Equal(t, "medium", models[0]["default_reasoning_level"])
	require.Equal(t, []string{"low", "medium", "high"}, effortsFromManifestModel(t, models[0]))
}

func TestGPT62APIKeyFetchCatalogMatchesGPT61Sol(t *testing.T) {
	upstreams := map[string]string{
		"openai list":           `{"object":"list","data":[{"id":"gpt-6.1-sol","object":"model"},{"id":"gpt-6.2-sol","object":"model"},{"id":"gpt-6.2-astra","object":"model"}]}`,
		"slug-only manifest":    `{"models":[{"slug":"gpt-6.1-sol"},{"slug":"gpt-6.2-sol"},{"slug":"gpt-6.2-astra"}]}`,
		"official-shape models": `{"models":[{"slug":"gpt-6.1-sol","use_responses_lite":true,"tool_mode":"code_mode_only","apply_patch_tool_type":"freeform","supports_search_tool":true},{"slug":"gpt-6.2-sol","use_responses_lite":true,"tool_mode":"code_mode_only","apply_patch_tool_type":"freeform","supports_search_tool":true},{"slug":"gpt-6.2-astra","use_responses_lite":true,"tool_mode":"code_mode_only","apply_patch_tool_type":"freeform","supports_search_tool":true}]}`,
	}
	for name, upstreamBody := range upstreams {
		for _, base := range []string{"https://relay.example.com/v1", "https://api.openai.com/v1"} {
			upstream := &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(upstreamBody))}, nil
			}}
			account := newCodexModelsAPIKeyTestAccount(base)
			svc := newCodexModelsAPIKeyTestService(upstream)
			manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.166.0", "")
			require.NoError(t, err, name)
			fetched := gpt62ModelsBySlug(t, manifest.Body)
			require.NoError(t, svc.CompleteAPIKeyCodexModelsManifestForClient(manifest, account), name)
			completed := gpt62ModelsBySlug(t, manifest.Body)
			require.Equal(t, false, completed["gpt-6.1-sol"]["use_responses_lite"], name)
			for _, tc := range gpt62CodexCatalogCases {
				path := name + " " + base + " " + tc.model
				// Clients receive the completed body. The intermediate cached body
				// is compared only on relay hosts, where no official image
				// forcing applies.
				if _, ok := fetched["gpt-6.1-sol"]["use_responses_lite"]; ok && !isOfficialOpenAIModelsBaseURL(base) {
					requireGPT62ParityWith(t, "fetch "+path, fetched["gpt-6.1-sol"], fetched[tc.model])
				}
				requireGPT62ParityWith(t, "complete "+path, completed["gpt-6.1-sol"], completed[tc.model])
				require.Equal(t, "low", completed[tc.model]["default_reasoning_level"], path)
			}
		}
	}
}

func TestGPT62AccountToolCapabilitiesMatchGPT61Sol(t *testing.T) {
	for _, account := range []*Account{
		gpt62RelayAPIKeyAccount(nil),
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com"}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": "https://chatgpt.com"}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}},
	} {
		reference := accountCodexToolCapabilities(account, "gpt-6.1-sol")
		for _, tc := range gpt62CodexCatalogCases {
			require.Equal(t, reference, accountCodexToolCapabilities(account, tc.model), "%s %v %s", account.Type, account.Credentials, tc.model)
		}
		// Live upstream metadata that advertises Lite is disabled for API key accounts exactly like 6.1.
		if account.IsOpenAIApiKey() {
			// Provider-prefixed and mixed-case spellings must reach the same Lite-off key.
			for _, model := range []string{"gpt-6.1-sol", "gpt-6.2-sol", "gpt-6.2-astra", "openai/GPT_6.1_SOL", "openai/GPT_6.2_SOL", "openai/gpt-6.2-astra"} {
				withLite := &Account{Platform: account.Platform, Type: account.Type, Credentials: account.Credentials}
				withLite.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{model: {ID: model, CodexToolCapabilities: map[string]json.RawMessage{"use_responses_lite": json.RawMessage("true")}}}})
				require.Equal(t, "false", string(accountCodexToolCapabilities(withLite, model)["use_responses_lite"]), model)
			}
		}
	}
	// Official hosts receive the same defaults as gpt-6.1-sol.
	official := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com"}}
	caps := accountCodexToolCapabilities(official, "gpt-6.2-sol")
	require.Equal(t, `"freeform"`, string(caps["apply_patch_tool_type"]))
	require.Equal(t, "false", string(caps["use_responses_lite"]))
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	caps = accountCodexToolCapabilities(oauth, "gpt-6.2-astra")
	require.Equal(t, `"code_mode_only"`, string(caps["tool_mode"]))
	require.Equal(t, "true", string(caps["use_responses_lite"]))
	// Ultrafast stays exclusive to gpt-6-astra on the official API host.
	require.NotContains(t, string(accountCodexToolCapabilities(official, "gpt-6.2-astra")["service_tiers"]), OpenAIFastTierUltrafast)
}

func TestGPT62AliasCatalogMatchesGPT61SolAlias(t *testing.T) {
	account := gpt62RelayAPIKeyAccount(map[string]any{"team-61": "gpt-6.1-sol", "team-62-sol": "gpt-6.2-sol", "team-62-astra": "openai/gpt-6.2-astra"})
	body := []byte(`{"models":[{"slug":"team-61","use_responses_lite":true},{"slug":"team-62-sol","use_responses_lite":true},{"slug":"team-62-astra","use_responses_lite":true}]}`)
	out, err := adjustAPIKeyCodexModelsManifest(body, account)
	require.NoError(t, err)
	adjusted := gpt62ModelsBySlug(t, out)
	require.Equal(t, false, adjusted["team-61"]["use_responses_lite"])
	require.Equal(t, adjusted["team-61"], map[string]any{"slug": "team-61", "use_responses_lite": false})
	require.Equal(t, adjusted["team-62-sol"], map[string]any{"slug": "team-62-sol", "use_responses_lite": false})
	require.Equal(t, adjusted["team-62-astra"], map[string]any{"slug": "team-62-astra", "use_responses_lite": false})

	group, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"team-61", "team-62-sol", "team-62-astra"}, []Account{*account}, &Group{Platform: PlatformOpenAI}, nil, true)
	require.NoError(t, err)
	models := gpt62ModelsBySlug(t, group)
	requireGPT62ParityWith(t, "alias group sol", models["team-61"], models["team-62-sol"])
	requireGPT62ParityWith(t, "alias group astra", models["team-61"], models["team-62-astra"])
	require.Equal(t, "low", models["team-62-sol"]["default_reasoning_level"])
	require.EqualValues(t, 500000, models["team-62-sol"]["context_window"])
	require.EqualValues(t, 272000, models["team-62-astra"]["context_window"])

	completed, err := completeAPIKeyCodexModelsManifestMetadata(body, true, account)
	require.NoError(t, err)
	completed, err = adjustAPIKeyCodexModelsManifest(completed, account)
	require.NoError(t, err)
	models = gpt62ModelsBySlug(t, completed)
	requireGPT62ParityWith(t, "alias complete sol", models["team-61"], models["team-62-sol"])
	requireGPT62ParityWith(t, "alias complete astra", models["team-61"], models["team-62-astra"])
}

// The request path is unchanged: GPT-6.2 IDs stay outside every request-side
// GPT-6 family predicate, alias normalizer, and reasoning-effort clamp.
func TestGPT62RequestPathPredicatesUnchanged(t *testing.T) {
	account := gpt62RelayAPIKeyAccount(nil)
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra", "GPT_6.2_SOL", "openai/gpt-6.2-astra", "gpt-6.2-sol-high", "gpt-6.2-astra-max"} {
		require.False(t, isOpenAIGPT6Model(model), model)
		require.False(t, isOpenAIGPT6AstraModel(model), model)
		require.False(t, openai.IsGPT61SolModelSpelling(model), model)
		require.False(t, openai.IsGPT61TerraModelSpelling(model), model)
		require.False(t, openai.IsGPT6SolOrLunaModelSpelling(model), model)
		require.False(t, openai.RequiresResponsesForToolCalls(model), model)
		require.Empty(t, normalizeKnownOpenAICodexModel(model), model)
		supported, reliable := supportedInferenceEffortsForAccount(account, model)
		require.False(t, reliable, model)
		require.Empty(t, supported, model)
		for _, effort := range []string{"none", "minimal", "low", "xhigh", "max"} {
			require.NoError(t, openai.ValidateGPT61SolReasoningEffort(model, effort), model)
			require.NoError(t, openai.ValidateGPT61TerraReasoningEffort(model, effort), model)
		}
	}
	for _, id := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		require.NotContains(t, openai.DefaultModelIDs(), id)
	}
}

// Request-side default instructions (OAuth/SetupToken forward, passthrough, and
// the OAuth transform) keep the v0.6.9 GPT-5.5 fallback for GPT-6.2; the
// official 6.2 template is advertised only in the models manifest.
func TestGPT62RequestInstructionsUnchanged(t *testing.T) {
	fallback := defaultCodexSynthInstructions("gpt-5.5")
	require.NotEmpty(t, fallback)
	officialTemplates := []string{
		strings.TrimSpace(gpt62OfficialInstructionsTemplate(t, openai.CodexGPT62SolMetadata)),
		strings.TrimSpace(gpt62OfficialInstructionsTemplate(t, openai.CodexGPT62AstraMetadata)),
	}
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra", "GPT_6.2_SOL", "openai/gpt-6.2-astra", "gpt-6.2-sol-high"} {
		require.Equal(t, openai.CodexBaseInstructionsForModel("gpt-5.5"), openai.CodexBaseInstructionsForModel(model), model)
		require.Equal(t, fallback, defaultCodexSynthInstructions(model), model)
		reqBody := map[string]any{"model": model, "input": "hi"}
		applyCodexOAuthTransform(reqBody, false, false)
		require.Equal(t, fallback, reqBody["instructions"], model)
		require.NotContains(t, officialTemplates, reqBody["instructions"], model)
	}
	// GPT-6.1 keeps its official template on the request path.
	require.Equal(t, strings.TrimSpace(gpt62OfficialInstructionsTemplate(t, openai.CodexGPT61SolMetadata)), defaultCodexSynthInstructions("gpt-6.1-sol"))
	require.Equal(t, strings.TrimSpace(gpt62OfficialInstructionsTemplate(t, openai.CodexGPT61TerraMetadata)), defaultCodexSynthInstructions("gpt-6.1-terra"))
}

// Models outside the 6.1/6.2 official embeds keep generated descriptors.
func TestGPT62OfficialMetadataScope(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.5", "gpt-6.2", "gpt-6.2-terra", "claude-opus-5-5", "deepseek-v4-pro"} {
		require.Nil(t, openai.CodexOfficialModelMetadata(model), model)
		require.Empty(t, newConfiguredCodexModelDescriptor(model).officialMetadata, model)
	}
	require.Equal(t, string(openai.CodexGPT61SolMetadata), string(newConfiguredCodexModelDescriptor("gpt-6.1-sol-high").officialMetadata))
	require.Equal(t, string(openai.CodexGPT61TerraMetadata), string(newConfiguredCodexModelDescriptor("gpt-6.1-terra").officialMetadata))
	for _, tc := range []struct{ model, want string }{
		{"gpt-6-astra", "gpt-6-astra"},
		{"gpt-6.1-terra-high", "gpt-6.1-terra"},
		{"openai/GPT_6.1_SOL_MAX", "gpt-6.1-sol"},
		{"gpt-6.2-sol", "gpt-6.2-sol"},
		{"openai/GPT_6.2_ASTRA", "gpt-6.2-astra"},
		{"gpt-6.2-sol-high", "gpt-6.2-sol-high"},
		{"gpt-5.6-sol", "gpt-5.6-sol"},
	} {
		require.Equal(t, tc.want, apiKeyCodexResponsesLiteTarget(tc.model), tc.model)
	}
}

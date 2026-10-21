//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProbeModelFamilies(t *testing.T) {
	for _, tc := range []struct {
		provider  string
		protocol  probeProtocol
		model     string
		reasoning probeReasoningKind
		expects   bool
	}{
		{MonitorProviderOpenAI, probeProtocolChat, "gpt-4o-mini", probeReasoningNone, false},
		{MonitorProviderOpenAI, probeProtocolChat, "gpt-4.1", probeReasoningNone, false},
		{MonitorProviderOpenAI, probeProtocolChat, "gpt-5.5", probeReasoningChatEffort, true},
		{MonitorProviderOpenAI, probeProtocolResponses, "gpt-6.1-sol", probeReasoningResponsesEffort, true},
		{MonitorProviderOpenAI, probeProtocolResponses, "o3-mini", probeReasoningResponsesEffort, true},
		{MonitorProviderOpenAI, probeProtocolResponses, "o4", probeReasoningResponsesEffort, true},
		{MonitorProviderOpenAI, probeProtocolResponses, "gpt-5-codex", probeReasoningResponsesEffort, true},
		{MonitorProviderOpenAI, probeProtocolChat, "openai/gpt-5", probeReasoningChatEffort, true},
		{MonitorProviderOpenAI, probeProtocolChat, "omni-moderation", probeReasoningNone, false},
		{MonitorProviderOpenAI, probeProtocolChat, "deepseek-r1", probeReasoningNone, true},
		{MonitorProviderOpenAI, probeProtocolChat, "glm-4.6", probeReasoningNone, true},
		{MonitorProviderGrok, probeProtocolChat, "grok-4.5", probeReasoningChatEffort, true},
		{MonitorProviderGrok, probeProtocolResponses, "grok-4.6", probeReasoningResponsesEffort, true},
		{MonitorProviderGrok, probeProtocolChat, "grok-3-mini", probeReasoningChatEffort, true},
		{MonitorProviderGrok, probeProtocolChat, "grok-3", probeReasoningNone, false},
		{MonitorProviderGrok, probeProtocolChat, "grok-4-fast-non-reasoning", probeReasoningNone, false},
		{MonitorProviderGemini, probeProtocolGemini, "gemini-2.5-flash", probeReasoningGeminiNoThinking, true},
		{MonitorProviderGemini, probeProtocolGemini, "gemini-2.5-flash-lite", probeReasoningGeminiNoThinking, true},
		{MonitorProviderGemini, probeProtocolGemini, "gemini-3.6-flash", probeReasoningGeminiNoThinking, true},
		{MonitorProviderGemini, probeProtocolGemini, "gemini-2.5-pro", probeReasoningGeminiMinThinking, true},
		{MonitorProviderGemini, probeProtocolGemini, "gemini-3-pro-preview", probeReasoningGeminiMinThinking, true},
		{MonitorProviderGemini, probeProtocolGemini, "gemini-2.0-flash", probeReasoningNone, false},
		{MonitorProviderAnthropic, probeProtocolAnthropic, "claude-opus-5-5", probeReasoningNone, false},
		{MonitorProviderAnthropic, probeProtocolAnthropic, "claude-sonnet-4-5-thinking", probeReasoningNone, true},
		{MonitorProviderKimi, probeProtocolChat, "kimi-k2.5", probeReasoningNone, true},
		{MonitorProviderZhipu, probeProtocolChat, "glm-5.3-flash", probeReasoningNone, true},
		{MonitorProviderDeepseek, probeProtocolChat, "deepseek-chat", probeReasoningNone, true},
		{MonitorProviderMiniMax, probeProtocolChat, "MiniMax-M2", probeReasoningNone, true},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			require.Equal(t, tc.reasoning, probeReasoningParam(tc.protocol, tc.provider, tc.model))
			require.Equal(t, tc.expects, probeExpectsReasoning(tc.provider, tc.model))
		})
	}
}

func buildProbeTestRequest(t *testing.T, provider, model string, opts *CheckOptions, fix probeShapeFix) (*probeRequest, map[string]any) {
	t.Helper()
	plan, err := newProbePlan(provider, "https://relay.example/base", "sk-test", model, opts)
	require.NoError(t, err)
	req, err := plan.build(fix)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &body))
	return req, body
}

// TestBuildProbeBody_MinimalSafeShapes 每种协议的最小安全请求：流式、输出上限按模型族，
// 不带 temperature、top_p、stream_options。
func TestBuildProbeBody_MinimalSafeShapes(t *testing.T) {
	responses := &CheckOptions{APIMode: MonitorAPIModeResponses}
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		opts     *CheckOptions
		url      string
		want     map[string]any
	}{
		{"openai chat", MonitorProviderOpenAI, "gpt-4o-mini", nil, "https://relay.example/base/v1/chat/completions",
			map[string]any{"model": "gpt-4o-mini", "max_tokens": float64(16), "stream": true}},
		{"openai chat reasoning", MonitorProviderOpenAI, "gpt-5.5", nil, "https://relay.example/base/v1/chat/completions",
			map[string]any{"model": "gpt-5.5", "max_tokens": float64(1024), "stream": true, "reasoning_effort": "low"}},
		{"openai responses reasoning", MonitorProviderOpenAI, "gpt-6.1-sol", responses, "https://relay.example/base/v1/responses",
			map[string]any{"model": "gpt-6.1-sol", "max_output_tokens": float64(1024), "stream": true,
				"instructions": "Reply with only the number.", "reasoning": map[string]any{"effort": "low"}}},
		{"openai responses", MonitorProviderOpenAI, "gpt-4.1", responses, "https://relay.example/base/v1/responses",
			map[string]any{"model": "gpt-4.1", "max_output_tokens": float64(16), "stream": true, "instructions": "Reply with only the number."}},
		{"grok chat", MonitorProviderGrok, "grok-4.5", nil, "https://relay.example/base/v1/chat/completions",
			map[string]any{"model": "grok-4.5", "max_tokens": float64(1024), "stream": true, "reasoning_effort": "low"}},
		{"grok responses", MonitorProviderGrok, "grok-4.6", responses, "https://relay.example/base/v1/responses",
			map[string]any{"model": "grok-4.6", "max_output_tokens": float64(1024), "stream": true,
				"instructions": "Reply with only the number.", "reasoning": map[string]any{"effort": "low"}}},
		{"anthropic", MonitorProviderAnthropic, "claude-opus-5-5", nil, "https://relay.example/base/v1/messages",
			map[string]any{"model": "claude-opus-5-5", "max_tokens": float64(16), "stream": true, "system": "Reply with only the number."}},
		{"gemini flash", MonitorProviderGemini, "gemini-2.5-flash", nil,
			"https://relay.example/base/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse",
			map[string]any{"generationConfig": map[string]any{"maxOutputTokens": float64(1024), "thinkingConfig": map[string]any{"thinkingBudget": float64(0)}}}},
		{"gemini pro", MonitorProviderGemini, "gemini-2.5-pro", nil,
			"https://relay.example/base/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
			map[string]any{"generationConfig": map[string]any{"maxOutputTokens": float64(1024),
				"thinkingConfig": map[string]any{"thinkingBudget": float64(128)}}}},
		{"kimi", MonitorProviderKimi, "kimi-k2.5", nil, "https://relay.example/base/v1/chat/completions",
			map[string]any{"model": "kimi-k2.5", "max_tokens": float64(1024), "stream": true}},
		{"zhipu", MonitorProviderZhipu, "glm-5.3-flash", nil, "https://relay.example/base/v1/chat/completions",
			map[string]any{"model": "glm-5.3-flash", "max_tokens": float64(1024), "stream": true}},
		{"deepseek", MonitorProviderDeepseek, "deepseek-chat", nil, "https://relay.example/base/v1/chat/completions",
			map[string]any{"model": "deepseek-chat", "max_tokens": float64(1024), "stream": true}},
		{"minimax", MonitorProviderMiniMax, "MiniMax-M2", nil, "https://relay.example/base/v1/chat/completions",
			map[string]any{"model": "MiniMax-M2", "max_tokens": float64(1024), "stream": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, body := buildProbeTestRequest(t, tc.provider, tc.model, tc.opts, probeShapeFix{})
			require.Equal(t, tc.url, req.URL)
			require.True(t, req.Stream)
			require.False(t, req.Replace)
			for k, v := range tc.want {
				require.Equal(t, v, body[k], k)
			}
			for _, banned := range []string{"temperature", "top_p", "stream_options", "thinking"} {
				require.NotContains(t, body, banned)
			}
			if _, ok := tc.want["reasoning_effort"]; !ok {
				require.NotContains(t, body, "reasoning_effort")
			}
			if _, ok := tc.want["reasoning"]; !ok {
				require.NotContains(t, body, "reasoning")
			}
			_, hasEffort := tc.want["reasoning_effort"]
			_, hasReasoning := tc.want["reasoning"]
			gen, _ := tc.want["generationConfig"].(map[string]any)
			_, hasThinking := gen["thinkingConfig"]
			require.Equal(t, hasEffort || hasReasoning || hasThinking, req.InjectedReasoning)
			prompt := probeTestPrompt(t, tc.provider, body)
			require.Regexp(t, `^Reply with only the number\. \d+ \+ \d+ = \?$`, prompt)
		})
	}
}

func probeTestPrompt(t *testing.T, provider string, body map[string]any) string {
	t.Helper()
	if input, ok := body["input"].(string); ok {
		return input
	}
	if provider == MonitorProviderGemini {
		contents := body["contents"].([]any)
		first := contents[0].(map[string]any)
		require.Equal(t, "user", first["role"])
		return first["parts"].([]any)[0].(map[string]any)["text"].(string)
	}
	messages := body["messages"].([]any)
	require.Len(t, messages, 1)
	msg := messages[0].(map[string]any)
	require.Equal(t, "user", msg["role"])
	return msg["content"].(string)
}

func TestBuildProbeBody_MergeRespectsUserChoices(t *testing.T) {
	merge := func(apiMode string, override map[string]any) *CheckOptions {
		return &CheckOptions{APIMode: apiMode, BodyOverrideMode: MonitorBodyOverrideModeMerge, BodyOverride: override}
	}

	req, body := buildProbeTestRequest(t, MonitorProviderOpenAI, "gpt-5.5", merge("", map[string]any{
		"reasoning_effort": "high", "max_tokens": 77, "stream": false, "model": "other", "temperature": 0.2,
	}), probeShapeFix{})
	require.Equal(t, "high", body["reasoning_effort"])
	require.False(t, req.InjectedReasoning)
	require.Equal(t, float64(77), body["max_tokens"])
	require.Equal(t, true, body["stream"], "stream is protected by the deny list")
	require.Equal(t, "gpt-5.5", body["model"])
	require.Equal(t, 0.2, body["temperature"], "user fields still win")

	_, body = buildProbeTestRequest(t, MonitorProviderOpenAI, "gpt-5.5", merge("", map[string]any{
		"max_completion_tokens": 300,
	}), probeShapeFix{})
	require.Equal(t, float64(300), body["max_completion_tokens"])
	require.NotContains(t, body, "max_tokens", "user's max_completion_tokens replaces ours")

	req, body = buildProbeTestRequest(t, MonitorProviderOpenAI, "gpt-6.1-sol", merge(MonitorAPIModeResponses, map[string]any{
		"reasoning": map[string]any{"effort": "medium"}, "max_output_tokens": 80, "stream": false, "input": "x", "instructions": "y",
	}), probeShapeFix{})
	require.Equal(t, map[string]any{"effort": "medium"}, body["reasoning"])
	require.False(t, req.InjectedReasoning)
	require.Equal(t, float64(80), body["max_output_tokens"])
	require.Equal(t, true, body["stream"])
	require.Equal(t, "Reply with only the number.", body["instructions"])
	require.NotEqual(t, "x", body["input"])

	req, body = buildProbeTestRequest(t, MonitorProviderAnthropic, "claude-x", merge("", map[string]any{"stream": false}), probeShapeFix{})
	require.Equal(t, true, body["stream"], "anthropic stream is protected now")
	require.True(t, req.Stream)

	req, body = buildProbeTestRequest(t, MonitorProviderGemini, "gemini-2.5-flash", merge("", map[string]any{
		"stream": true, "generationConfig": map[string]any{"maxOutputTokens": 64},
	}), probeShapeFix{})
	require.NotContains(t, body, "stream", "gemini has no stream body field")
	require.Equal(t, map[string]any{"maxOutputTokens": float64(64)}, body["generationConfig"])
	require.False(t, req.InjectedReasoning, "user's generationConfig replaced ours")
}

func TestBuildProbeBody_ReplaceSendsUserBodyUntouched(t *testing.T) {
	for _, stream := range []bool{false, true} {
		userBody := map[string]any{"model": "gpt-5.5", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": stream}
		req, body := buildProbeTestRequest(t, MonitorProviderOpenAI, "gpt-5.5", &CheckOptions{
			BodyOverrideMode: MonitorBodyOverrideModeReplace, BodyOverride: userBody,
		}, probeShapeFix{DropReasoning: true, UseMaxCompletionTokens: true})
		require.Equal(t, stream, req.Stream)
		require.True(t, req.Replace)
		require.False(t, req.InjectedReasoning)
		require.Equal(t, map[string]any{"model": "gpt-5.5", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "stream": stream}, body)
	}

	req, _ := buildProbeTestRequest(t, MonitorProviderGemini, "gemini-2.5-pro", &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeReplace,
		BodyOverride:     map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": "hi"}}}}},
	}, probeShapeFix{})
	require.False(t, req.Stream)
	require.Equal(t, "https://relay.example/base/v1beta/models/gemini-2.5-pro:generateContent", req.URL)
}

func TestBuildProbeBody_AppliesShapeFixes(t *testing.T) {
	req, body := buildProbeTestRequest(t, MonitorProviderOpenAI, "gpt-5.5", nil, probeShapeFix{DropReasoning: true, UseMaxCompletionTokens: true})
	require.NotContains(t, body, "reasoning_effort")
	require.False(t, req.InjectedReasoning)
	require.NotContains(t, body, "max_tokens")
	require.Equal(t, float64(1024), body["max_completion_tokens"])
	require.False(t, req.HasMaxTokens)

	req, body = buildProbeTestRequest(t, MonitorProviderOpenAI, "gpt-5.5", nil, probeShapeFix{NoStream: true})
	require.Equal(t, false, body["stream"])
	require.False(t, req.Stream)

	req, body = buildProbeTestRequest(t, MonitorProviderGemini, "gemini-2.5-flash", nil, probeShapeFix{NoStream: true, DropReasoning: true})
	require.False(t, req.Stream)
	require.Equal(t, "https://relay.example/base/v1beta/models/gemini-2.5-flash:generateContent", req.URL)
	require.Equal(t, map[string]any{"maxOutputTokens": float64(1024)}, body["generationConfig"])
}

// TestBuildProbeBody_ClaudeCodeTemplate 生产在用的 Claude Code 伪装模板：merge 模式加 system、
// metadata 与自定义头；model、messages、stream 受保护。
func TestBuildProbeBody_ClaudeCodeTemplate(t *testing.T) {
	system := []any{map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}}
	metadata := map[string]any{"user_id": "user_0000_account__session_00000000-0000-0000-0000-000000000000"}
	opts := &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeMerge,
		BodyOverride: map[string]any{
			"system": system, "metadata": metadata, "max_tokens": float64(512),
			"model": "hacked", "messages": []any{}, "stream": false,
		},
		ExtraHeaders: map[string]string{
			"User-Agent":     "claude-cli/1.0.83 (external, cli)",
			"x-app":          "cli",
			"anthropic-beta": "claude-code-20250219",
			"Content-Length": "999",
		},
	}
	req, body := buildProbeTestRequest(t, MonitorProviderAnthropic, "claude-opus-5-5", opts, probeShapeFix{})
	require.Equal(t, "https://relay.example/base/v1/messages", req.URL)
	require.Equal(t, system, body["system"])
	require.Equal(t, metadata, body["metadata"])
	require.Equal(t, float64(512), body["max_tokens"])
	require.Equal(t, "claude-opus-5-5", body["model"])
	require.Equal(t, true, body["stream"])
	require.Len(t, body["messages"], 1)
	require.NotContains(t, body, "thinking")
	require.Equal(t, "sk-test", req.Headers["x-api-key"])
	require.Equal(t, monitorAnthropicAPIVersion, req.Headers["anthropic-version"])
	require.Equal(t, "claude-cli/1.0.83 (external, cli)", req.Headers["User-Agent"])
	require.Equal(t, "cli", req.Headers["x-app"])
	require.Equal(t, "claude-code-20250219", req.Headers["anthropic-beta"])
	require.NotContains(t, req.Headers, "Content-Length")
}

func TestProbeRequestPaths(t *testing.T) {
	for _, tc := range []struct {
		provider string
		apiMode  string
		base     string
		want     string
	}{
		{MonitorProviderGemini, "", "https://g.example/relay/v1beta", "https://g.example/relay/v1beta/models/m:streamGenerateContent?alt=sse"},
		{MonitorProviderZhipu, "", "https://open.bigmodel.cn/api/coding/paas/v4", "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"},
		{MonitorProviderZhipu, "", "https://open.bigmodel.cn", "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{MonitorProviderGrok, MonitorAPIModeResponses, "https://relay.example/v1", "https://relay.example/v1/responses"},
	} {
		plan, err := newProbePlan(tc.provider, tc.base, "k", "m", &CheckOptions{APIMode: tc.apiMode})
		require.NoError(t, err)
		req, err := plan.build(probeShapeFix{})
		require.NoError(t, err)
		require.Equal(t, tc.want, req.URL)
	}
}

func TestProbeShapeFixCache(t *testing.T) {
	cache := &probeShapeFixCache{fixes: map[string]probeShapeFix{}}
	key := probeShapeKey(MonitorProviderOpenAI, "https://e", MonitorAPIModeChatCompletions, "gpt-5.5")
	require.Equal(t, probeShapeFix{}, cache.get(key))
	cache.learn(key, probeShapeFix{DropReasoning: true})
	cache.learn(key, probeShapeFix{NoStream: true})
	require.Equal(t, probeShapeFix{DropReasoning: true, NoStream: true}, cache.get(key))
	require.Equal(t, probeShapeFix{}, cache.get(strings.Replace(key, "gpt-5.5", "gpt-5.6", 1)))

	for i := 0; i < monitorProbeShapeFixMaxEntries+5; i++ {
		cache.learn(probeShapeKey("p", "e", "m", strings.Repeat("x", i+1)), probeShapeFix{NoStream: true})
	}
	require.LessOrEqual(t, len(cache.fixes), monitorProbeShapeFixMaxEntries)
}

func TestProbeGeminiProGetsMinimumThinkingBudget(t *testing.T) {
	require.Equal(t, probeReasoningGeminiMinThinking, probeReasoningParam(probeProtocolGemini, MonitorProviderGemini, "gemini-2.5-pro"))
	require.Equal(t, probeReasoningGeminiMinThinking, probeReasoningParam(probeProtocolGemini, MonitorProviderGemini, "gemini-3-pro-preview"))
	require.Equal(t, probeReasoningNone, probeReasoningParam(probeProtocolGemini, MonitorProviderGemini, "gemini-1.5-pro"))

	body := probeDefaultBody(probeProtocolGemini, "gemini-2.5-pro", "q", true, monitorChallengeReasoningMaxTokens, probeReasoningGeminiMinThinking)
	generation, _ := body["generationConfig"].(map[string]any)
	thinking, _ := generation["thinkingConfig"].(map[string]any)
	require.Equal(t, monitorProbeGeminiMinThinkingBudget, thinking["thinkingBudget"])
}

func TestProbeShapeFixCacheExpiresAndIsPerKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cache := &probeShapeFixCache{fixes: map[string]probeShapeFix{}, now: func() time.Time { return now }}
	key := probeShapeKey(MonitorProviderOpenAI, "https://e", MonitorAPIModeChatCompletions, "gpt-5.5") + "|" + probeKeyDigest("sk-a")
	cache.learn(key, probeShapeFix{DropReasoning: true})
	require.True(t, cache.get(key).DropReasoning)

	now = now.Add(monitorProbeShapeFixTTL + time.Second)
	require.Equal(t, probeShapeFix{}, cache.get(key), "a learned fix expires so a one-off rejection does not stick")

	require.NotEqual(t, probeKeyDigest("sk-a"), probeKeyDigest("sk-b"))
	planA, err := newProbePlan(MonitorProviderOpenAI, "https://e", "sk-a", "gpt-5.5", nil)
	require.NoError(t, err)
	planB, err := newProbePlan(MonitorProviderOpenAI, "https://e", "sk-b", "gpt-5.5", nil)
	require.NoError(t, err)
	require.NotEqual(t, planA.fixKey(), planB.fixKey(), "fixes learned for one key must not change another key's probes")
}

func TestProbeSameHostRedirect(t *testing.T) {
	from, _ := http.NewRequest(http.MethodPost, "https://gateway.example/v1/chat/completions", nil)
	same, _ := http.NewRequest(http.MethodPost, "https://gateway.example/v1/other", nil)
	other, _ := http.NewRequest(http.MethodPost, "https://elsewhere.example/v1/chat/completions", nil)
	require.NoError(t, probeSameHostRedirect(same, []*http.Request{from}))
	require.ErrorIs(t, probeSameHostRedirect(other, []*http.Request{from}), http.ErrUseLastResponse)
}

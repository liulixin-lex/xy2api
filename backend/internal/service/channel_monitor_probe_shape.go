package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// 探测请求形状：每种协议一个“最小安全请求”（不带 temperature、top_p、stream_options），
// 只对已知支持的模型族带最低推理强度，输出上限按模型族给（XY2API 渠道监控 v1 探测优化）。

// probeReasoningKind 注入的推理强度参数。
type probeReasoningKind int

const (
	probeReasoningNone probeReasoningKind = iota
	// probeReasoningChatEffort Chat Completions：reasoning_effort: "low"。
	probeReasoningChatEffort
	// probeReasoningResponsesEffort Responses：reasoning: {effort: "low"}。
	probeReasoningResponsesEffort
	// probeReasoningGeminiNoThinking Gemini flash：generationConfig.thinkingConfig.thinkingBudget: 0。
	probeReasoningGeminiNoThinking
)

//nolint:gochecknoglobals // 只读的模型族匹配规则。
var (
	// probeOpenAIReasoningRegex OpenAI 推理模型：gpt-5 及以后、o1–o9、codex。
	probeOpenAIReasoningRegex = regexp.MustCompile(`^(?:gpt-(?:[5-9]|\d{2,})(?:$|[-.])|o[1-9](?:$|[-.]))|codex`)
	// probeGrokReasoningRegex Grok 推理模型：grok-3-mini、grok-4 及以后、grok-code。
	probeGrokReasoningRegex = regexp.MustCompile(`^grok-(?:3-mini|[4-9]|\d{2,}|code)`)
	// probeGeminiFlashThinkingRegex Gemini 2.5/3 及以后的 flash 系（可以关闭思考）。
	probeGeminiFlashThinkingRegex = regexp.MustCompile(`^gemini-(?:2\.5|[3-9](?:\.\d+)?|\d{2,})-flash`)
	// probeGeminiLegacyRegex 不思考的旧 Gemini（1.x、2.0）。
	probeGeminiLegacyRegex = regexp.MustCompile(`^gemini-(?:1\.|2\.0)`)
	// probeReasoningNameRegex 按名字识别的推理模型（与 provider 无关，例如经中转的其他家模型）。
	probeReasoningNameRegex = regexp.MustCompile(`^(?:gpt-(?:[5-9]|\d{2,})(?:$|[-.])|o[1-9](?:$|[-.])|gpt-oss|` +
		`grok-(?:3-mini|[4-9]|\d{2,}|code)|gemini-(?:2\.5|[3-9]|\d{2,})|glm-(?:4\.[5-9]|[5-9])|minimax-m|qwen3|deepseek-reasoner)` +
		`|codex|think|reason|qwq|(?:^|[-_.])r1(?:$|[-_.])`)
	// probeNonReasoningNameRegex 名字里明确写着不推理。
	probeNonReasoningNameRegex = regexp.MustCompile(`non-?reasoning|non-?thinking|no-?think`)
)

// probeModelName 归一模型名：小写，去掉 "vendor/" 前缀。
func probeModelName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// probeExpectsReasoning 该模型是否可能先推理再回答，决定输出上限（推理 token 计在上限里，
// 上限太小会“推理吃光预算、正文为空”）。宁可多给：读到答案即停，上限给大不会变慢。
//   - Anthropic：缺省不开 thinking；
//   - Gemini：除 1.x、2.0 外都会思考；
//   - 国产四家：主力模型多为混合推理且默认开思考。
func probeExpectsReasoning(provider, model string) bool {
	name := probeModelName(model)
	if probeNonReasoningNameRegex.MatchString(name) {
		return false
	}
	if probeReasoningNameRegex.MatchString(name) {
		return true
	}
	switch provider {
	case MonitorProviderOpenAI, MonitorProviderGrok, MonitorProviderAnthropic:
		return false
	case MonitorProviderGemini:
		return !probeGeminiLegacyRegex.MatchString(name)
	default:
		return true
	}
}

// probeReasoningParam 该模型要注入的推理强度参数；只对已知支持的模型族注入。
// Anthropic（缺省即不思考）、国产四家不注入。
func probeReasoningParam(protocol probeProtocol, provider, model string) probeReasoningKind {
	name := probeModelName(model)
	if probeNonReasoningNameRegex.MatchString(name) {
		return probeReasoningNone
	}
	switch provider {
	case MonitorProviderOpenAI:
		if !probeOpenAIReasoningRegex.MatchString(name) {
			return probeReasoningNone
		}
	case MonitorProviderGrok:
		if !probeGrokReasoningRegex.MatchString(name) {
			return probeReasoningNone
		}
	case MonitorProviderGemini:
		if probeGeminiFlashThinkingRegex.MatchString(name) {
			return probeReasoningGeminiNoThinking
		}
		return probeReasoningNone
	default:
		return probeReasoningNone
	}
	if protocol == probeProtocolResponses {
		return probeReasoningResponsesEffort
	}
	return probeReasoningChatEffort
}

// probeReasoningKeys merge 模式下，用户 body 里出现这些字段即视为用户自己管推理参数。
//
//nolint:gochecknoglobals // 静态查表。
var probeReasoningKeys = map[probeProtocol][]string{
	probeProtocolChat:      {"reasoning_effort", "reasoning", "thinking", "enable_thinking"},
	probeProtocolResponses: {"reasoning"},
	probeProtocolAnthropic: {"thinking"},
}

func probeOverrideHasReasoning(protocol probeProtocol, override map[string]any) bool {
	if protocol == probeProtocolGemini {
		gen, ok := override["generationConfig"].(map[string]any)
		if !ok {
			return false
		}
		_, has := gen["thinkingConfig"]
		return has
	}
	for _, key := range probeReasoningKeys[protocol] {
		if _, ok := override[key]; ok {
			return true
		}
	}
	return false
}

// probeReasoningContainerKey 注入的推理参数所在的顶层字段。
func probeReasoningContainerKey(kind probeReasoningKind) string {
	switch kind {
	case probeReasoningChatEffort:
		return "reasoning_effort"
	case probeReasoningResponsesEffort:
		return "reasoning"
	case probeReasoningGeminiNoThinking:
		return "generationConfig"
	default:
		return ""
	}
}

// probeShapeFix 上游拒绝可选参数后学到的修正，按 provider|endpoint|apiMode|model 记在进程内。
type probeShapeFix struct {
	// DropReasoning 不再注入推理强度参数。
	DropReasoning bool
	// UseMaxCompletionTokens Chat 的 max_tokens 改名为 max_completion_tokens。
	UseMaxCompletionTokens bool
	// NoStream 改用非流式请求。
	NoStream bool
}

func (f probeShapeFix) merge(o probeShapeFix) probeShapeFix {
	return probeShapeFix{
		DropReasoning:          f.DropReasoning || o.DropReasoning,
		UseMaxCompletionTokens: f.UseMaxCompletionTokens || o.UseMaxCompletionTokens,
		NoStream:               f.NoStream || o.NoStream,
	}
}

// probeShapeFixCache 并发安全的修正表。条目极少（监控数 × 模型数）；超过上限整表清空、重新学习。
type probeShapeFixCache struct {
	mu    sync.Mutex
	fixes map[string]probeShapeFix
}

//nolint:gochecknoglobals // 进程内的参数修正表，重启后最多再学一次。
var monitorProbeShapeFixes = &probeShapeFixCache{fixes: map[string]probeShapeFix{}}

func probeShapeKey(provider, endpoint, apiMode, model string) string {
	return provider + "|" + endpoint + "|" + apiMode + "|" + model
}

func (c *probeShapeFixCache) get(key string) probeShapeFix {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fixes[key]
}

func (c *probeShapeFixCache) learn(key string, fix probeShapeFix) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.fixes[key]; !exists && len(c.fixes) >= monitorProbeShapeFixMaxEntries {
		c.fixes = map[string]probeShapeFix{}
	}
	c.fixes[key] = c.fixes[key].merge(fix)
}

// probeBodyInfo 构造出的请求体的几个关键事实，供读法与参数修正使用。
type probeBodyInfo struct {
	Stream            bool
	Replace           bool
	InjectedReasoning bool
	HasMaxTokens      bool
}

// buildProbeBody 按 body_override_mode 构造探测请求体。
//
//   - off：协议的最小安全请求（流式、输出上限、已知模型族的最低推理强度）；
//   - merge：在最小请求上浅合并用户字段，用户优先；命中 bodyMergeKeyDenyList 的字段丢弃；
//     用户自己写了推理参数或输出上限时以用户为准；
//   - replace：用户 body 原样发送，不注入任何参数；按其中的 stream 决定读法。
func buildProbeBody(protocol probeProtocol, provider, apiMode, model, prompt string, opts *CheckOptions, fix probeShapeFix) ([]byte, probeBodyInfo, error) {
	mode := bodyOverrideMode(opts)
	if mode == MonitorBodyOverrideModeReplace {
		if opts == nil || len(opts.BodyOverride) == 0 {
			return nil, probeBodyInfo{}, fmt.Errorf("replace mode: body_override is empty")
		}
		if err := validateReplaceRequestBody(provider, apiMode, opts.BodyOverride); err != nil {
			return nil, probeBodyInfo{}, err
		}
		body, err := json.Marshal(opts.BodyOverride)
		if err != nil {
			return nil, probeBodyInfo{}, fmt.Errorf("marshal body_override (replace): %w", err)
		}
		stream, _ := opts.BodyOverride["stream"].(bool)
		return body, probeBodyInfo{Stream: stream, Replace: true}, nil
	}

	var override map[string]any
	if mode == MonitorBodyOverrideModeMerge && opts != nil {
		override = opts.BodyOverride
	}
	userReasoning := probeOverrideHasReasoning(protocol, override)
	limit := monitorChallengeMaxTokens
	if userReasoning || probeExpectsReasoning(provider, model) {
		limit = monitorChallengeReasoningMaxTokens
	}
	reasoning := probeReasoningNone
	if !fix.DropReasoning && !userReasoning {
		reasoning = probeReasoningParam(protocol, provider, model)
	}
	stream := !fix.NoStream
	body := probeDefaultBody(protocol, model, prompt, stream, limit, reasoning)

	deny := bodyMergeKeyDenyList[bodyMergeDenyKey(provider, apiMode)]
	for k, v := range override {
		if deny[k] {
			continue
		}
		body[k] = v
	}
	if protocol == probeProtocolChat {
		_, userMaxTokens := override["max_tokens"]
		if _, userMaxCompletion := override["max_completion_tokens"]; userMaxCompletion && !userMaxTokens {
			delete(body, "max_tokens")
		}
		if v, ok := body["max_tokens"]; ok && fix.UseMaxCompletionTokens {
			if _, exists := body["max_completion_tokens"]; !exists {
				body["max_completion_tokens"] = v
			}
			delete(body, "max_tokens")
		}
	}

	_, overridden := override[probeReasoningContainerKey(reasoning)]
	info := probeBodyInfo{Stream: stream, InjectedReasoning: reasoning != probeReasoningNone && !overridden}
	if protocol == probeProtocolChat {
		_, info.HasMaxTokens = body["max_tokens"]
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, probeBodyInfo{}, fmt.Errorf("marshal probe body: %w", err)
	}
	return encoded, info, nil
}

// probeDefaultBody 各协议的最小安全请求。
func probeDefaultBody(protocol probeProtocol, model, prompt string, stream bool, limit int, reasoning probeReasoningKind) map[string]any {
	userMessages := []map[string]any{{"role": "user", "content": prompt}}
	switch protocol {
	case probeProtocolResponses:
		body := map[string]any{
			"model":             model,
			"instructions":      monitorChallengeInstruction,
			"input":             prompt,
			"max_output_tokens": limit,
			"stream":            stream,
		}
		if reasoning == probeReasoningResponsesEffort {
			body["reasoning"] = map[string]any{"effort": monitorProbeReasoningEffort}
		}
		return body
	case probeProtocolAnthropic:
		return map[string]any{
			"model":      model,
			"system":     monitorChallengeInstruction,
			"messages":   userMessages,
			"max_tokens": limit,
			"stream":     stream,
		}
	case probeProtocolGemini:
		generation := map[string]any{"maxOutputTokens": limit}
		if reasoning == probeReasoningGeminiNoThinking {
			generation["thinkingConfig"] = map[string]any{"thinkingBudget": 0}
		}
		return map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": prompt}}},
			},
			"generationConfig": generation,
		}
	default:
		body := map[string]any{
			"model":      model,
			"messages":   userMessages,
			"max_tokens": limit,
			"stream":     stream,
		}
		if reasoning == probeReasoningChatEffort {
			body["reasoning_effort"] = monitorProbeReasoningEffort
		}
		return body
	}
}

// probeRequest 一次探测 HTTP 请求。
type probeRequest struct {
	URL     string
	Body    []byte
	Headers map[string]string
	Fix     probeShapeFix
	probeBodyInfo
}

// probePlan 一个模型一轮探测中不变的部分。
type probePlan struct {
	provider  string
	endpoint  string
	apiKey    string
	model     string
	apiMode   string
	monitorID int64
	protocol  probeProtocol
	adapter   providerAdapter
	opts      *CheckOptions
	challenge monitorChallenge
}

// newProbePlan 校验 provider / api_mode 并出题。
func newProbePlan(provider, endpoint, apiKey, model string, opts *CheckOptions) (*probePlan, error) {
	requested := checkAPIMode(opts)
	if err := validateAPIMode(provider, requested); err != nil {
		return nil, err
	}
	adapter, apiMode, ok := providerAdapterFor(provider, requested)
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
	plan := &probePlan{
		provider: provider, endpoint: endpoint, apiKey: apiKey, model: model, apiMode: apiMode,
		protocol: adapter.protocol, adapter: adapter, opts: opts, challenge: generateChallenge(),
	}
	if opts != nil {
		plan.monitorID = opts.MonitorID
	}
	return plan, nil
}

// expected 校验用的期望答案；replace 模式没有题目，返回空串（正文非空即可）。
func (p *probePlan) expected() string {
	if bodyOverrideMode(p.opts) == MonitorBodyOverrideModeReplace {
		return ""
	}
	return p.challenge.Expected
}

func (p *probePlan) fixKey() string {
	return probeShapeKey(p.provider, p.endpoint, p.apiMode, p.model)
}

// build 按修正 fix 构造请求。
func (p *probePlan) build(fix probeShapeFix) (*probeRequest, error) {
	body, info, err := buildProbeBody(p.protocol, p.provider, p.apiMode, p.model, p.challenge.Prompt, p.opts, fix)
	if err != nil {
		return nil, err
	}
	url := joinURL(p.endpoint, probeRequestPath(p.provider, p.endpoint, p.adapter, p.model, info.Stream))
	if info.Stream && p.protocol == probeProtocolGemini {
		url += "?alt=sse"
	}
	return &probeRequest{
		URL:           url,
		Body:          body,
		Headers:       mergeHeaders(p.adapter.buildHeaders(p.apiKey), p.opts),
		Fix:           fix,
		probeBodyInfo: info,
	}, nil
}

// probeRequestPath 流式请求的路径（只有 Gemini 与非流式不同）。
func probeRequestPath(provider, endpoint string, adapter providerAdapter, model string, stream bool) string {
	if stream && adapter.buildStreamPath != nil {
		return adapter.buildStreamPath(model)
	}
	return monitorRequestPath(provider, endpoint, adapter, model)
}

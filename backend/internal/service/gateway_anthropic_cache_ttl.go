package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type anthropicCacheTTLKey struct{}
type bedrockCacheFinalizedKey struct{}

// This value belongs to the admitted user key, even when failover temporarily
// changes the serving key or group. Never resolve it again inside an attempt.
type anthropicCacheTTLPolicy struct {
	mode, ttl, source string
}

func (s *GatewayService) withAnthropicCacheTTLPolicy(ctx context.Context, key *APIKey) context.Context {
	if ctx.Value(anthropicCacheTTLKey{}) != nil || key == nil {
		return ctx
	}
	p := anthropicCacheTTLPolicy{mode: key.AnthropicCacheTTLMode, source: "key"}
	switch p.mode {
	case "5m", "1h", "off":
		p.ttl = p.mode
	default:
		p.mode, p.ttl, p.source = "inherit", "5m", "global"
		if s != nil && s.settingService != nil {
			p.ttl = s.settingService.GetAnthropicCacheTTLDefault(ctx)
		}
	}
	return context.WithValue(ctx, anthropicCacheTTLKey{}, p)
}

func anthropicCacheTTLPolicyFrom(ctx context.Context) anthropicCacheTTLPolicy {
	if p, ok := ctx.Value(anthropicCacheTTLKey{}).(anthropicCacheTTLPolicy); ok {
		return p
	}
	return anthropicCacheTTLPolicy{mode: "off", ttl: "off", source: "unrecorded"}
}

// Resolve the actual model, not a group/platform label or arbitrary substring.
// Bedrock IDs use a documented region/provider prefix; Vertex uses @versions.
func isActualClaudeModel(model string) bool {
	id := strings.ToLower(strings.TrimSpace(model))
	id = strings.TrimPrefix(id, "models/")
	id = strings.TrimPrefix(id, "anthropic/")
	for _, prefix := range bedrockCrossRegionPrefixes {
		id = strings.TrimPrefix(id, prefix)
	}
	id = strings.TrimPrefix(id, "anthropic.")
	if len(claude.EffortLevelsForModel(id)) > 0 {
		return true
	}
	if ResolveThinkingProtocol(id) != ThinkingProtocolAnthropicStrict {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	family := func(v string) bool { return v == "opus" || v == "sonnet" || v == "haiku" }
	numeric := func(v string) bool { return len(v) > 0 && v[0] >= '0' && v[0] <= '9' }
	if len(parts) < 2 {
		return false
	}
	if family(parts[0]) {
		return numeric(parts[1])
	}
	if numeric(parts[0]) {
		for _, part := range parts[1:min(len(parts), 4)] {
			if family(part) {
				return true
			}
		}
	}
	return false
}

func anthropicCacheTTLApplicable(account *Account, model string) bool {
	return account != nil && account.Platform == PlatformAnthropic && isActualClaudeModel(model)
}

func anthropicCacheControlPaths(body []byte) []string {
	_, messages, tools, system := collectCacheControlPaths(body)
	paths := append(append(tools, system...), messages...)
	if gjson.GetBytes(body, "cache_control").Exists() {
		paths = append(paths, "cache_control")
	}
	return paths
}

func anthropicCacheTTLApplied(body []byte, ttl string) bool {
	if ttl != "5m" && ttl != "1h" {
		return false
	}
	found := false
	for _, path := range anthropicCacheControlPaths(body) {
		cc := gjson.GetBytes(body, path)
		if cc.Get("type").String() != "ephemeral" {
			continue
		}
		actual := cc.Get("ttl")
		if !actual.Exists() {
			actual = gjson.Parse(`"5m"`)
		}
		if actual.Type != gjson.String || actual.String() != ttl {
			return false
		}
		found = true
	}
	return found
}

// All cache transforms are local to the final outbound attempt. The remembered
// input is the exact non-cache view, avoiding destructive "undo" by JSON paths.
func (s *GatewayService) finalizeAnthropicCache(ctx context.Context, account *Account, body []byte, model string) []byte {
	attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt)
	clean := body
	if attempt != nil {
		attempt.cleanBody, attempt.patchedBody = nil, nil
		attempt.ttlPolicy, attempt.ttlApplied = nil, nil
	}
	if anthropicCacheTTLApplicable(account, model) {
		body = s.applyClaudeCacheFallback(ctx, account, body, model)
		p := anthropicCacheTTLPolicyFrom(ctx)
		if p.ttl == "5m" || p.ttl == "1h" {
			body = forceEphemeralCacheControlTTL(body, p.ttl)
		}
		body = enforceCacheControlLimit(stripDeferredToolCacheControl(body))
		if account.IsBedrock() {
			body = sanitizeBedrockCacheControl(body, model)
			body = removeCustomFieldFromTools(body)
		}
		if attempt != nil {
			ttl, applied := p.ttl, anthropicCacheTTLApplied(body, p.ttl)
			attempt.ttlPolicy, attempt.ttlApplied = &ttl, &applied
		}
	}
	if attempt != nil {
		attempt.cleanBody, attempt.patchedBody = clean, body
	}
	return body
}

func finishAnthropicCacheTTLAttempt(ctx context.Context, result *ForwardResult) {
	if attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt); attempt != nil && result != nil {
		result.AnthropicCacheTTLPolicy, result.AnthropicCacheTTLApplied = attempt.ttlPolicy, attempt.ttlApplied
	}
}

// Before selection, consult the same account snapshot as reservation mappings.
// When the snapshot is unavailable, a Claude route may still select OAuth.
func (s *GatewayService) claudeCacheOAuthInjectionPossible(ctx context.Context, key *APIKey, model string) bool {
	if key == nil {
		return false
	}
	platform, forced, err := s.resolvePlatform(ctx, key.GroupID, key.Group, model)
	if err != nil || platform != PlatformAnthropic {
		return false
	}
	if s.schedulerSnapshot == nil {
		return isActualClaudeModel(model)
	}
	accounts, _, err := s.schedulerSnapshot.ListSchedulableAccounts(ctx, key.GroupID, platform, forced)
	if err != nil {
		return isActualClaudeModel(model)
	}
	for i := range accounts {
		a := &accounts[i]
		actual := claude.NormalizeModelID(model)
		if a.IsAnthropicOAuthOrSetupToken() && isActualClaudeModel(actual) {
			return true
		}
	}
	return false
}

// Automatic caching and an explicit marker on its tail must agree. Native
// Messages bodies keep a conflict for the provider's ordinary validation
// response; converted bodies reconcile first (enforceConvertedCacheControlLimit).
func anthropicAutomaticTailConflict(body []byte) bool {
	top := gjson.GetBytes(body, "cache_control")
	if top.Get("type").String() != "ephemeral" {
		return false
	}
	_, tail := anthropicAutomaticCacheTail(body)
	cc := tail.Get("cache_control")
	if cc.Get("type").String() != "ephemeral" {
		return false
	}
	return anthropicCacheMarkerTTL(top) != anthropicCacheMarkerTTL(cc)
}

// anthropicAutomaticCacheTail returns the block that automatic caching
// applies to: the last cacheable block across tools, system, then messages.
// A non-empty string message is an unmarked tail and has no block path.
func anthropicAutomaticCacheTail(body []byte) (string, gjson.Result) {
	root := gjson.ParseBytes(body)
	var path string
	var tail gjson.Result
	for _, field := range []string{"tools", "system"} {
		index := 0
		root.Get(field).ForEach(func(_, block gjson.Result) bool {
			if claudeCacheableBlock(block, field) {
				path, tail = fmt.Sprintf("%s.%d", field, index), block
			}
			index++
			return true
		})
	}
	msgIndex := 0
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.Type == gjson.String && content.String() != "" {
			path, tail = "", content
		}
		if content.IsArray() {
			blockIndex := 0
			content.ForEach(func(_, block gjson.Result) bool {
				if claudeCacheableBlock(block, "messages") {
					path, tail = fmt.Sprintf("messages.%d.content.%d", msgIndex, blockIndex), block
				}
				blockIndex++
				return true
			})
		}
		msgIndex++
		return true
	})
	return path, tail
}

func anthropicCacheMarkerTTL(cc gjson.Result) string {
	if v := cc.Get("ttl"); v.Exists() {
		return v.String()
	}
	return "5m"
}

// enforceConvertedCacheControlLimit is enforceCacheControlLimit for bodies the
// gateway built from Chat Completions or Responses. Those requests cannot
// express a tail TTL that differs from automatic caching on purpose, so a
// 5m/1h disagreement resolves toward 1h instead of reaching the provider as a
// 400. Native Messages bodies keep enforceCacheControlLimit and their conflict.
func enforceConvertedCacheControlLimit(body []byte) []byte {
	return normalizeCacheControlTTLOrder(reconcileAutomaticCacheTTL(enforceCacheControlBlockLimit(body)))
}

func reconcileAutomaticCacheTTL(body []byte) []byte {
	top := gjson.GetBytes(body, "cache_control")
	if top.Get("type").String() != "ephemeral" {
		return body
	}
	path, tail := anthropicAutomaticCacheTail(body)
	cc := tail.Get("cache_control")
	if path == "" || cc.Get("type").String() != "ephemeral" {
		return body
	}
	var target string
	switch topTTL, tailTTL := anthropicCacheMarkerTTL(top), anthropicCacheMarkerTTL(cc); {
	case topTTL == "5m" && tailTTL == cacheTTLTarget1h:
		target = "cache_control.ttl"
	case topTTL == cacheTTLTarget1h && tailTTL == "5m":
		target = path + ".cache_control.ttl"
	default:
		return body
	}
	if out, err := sjson.SetBytes(body, target, cacheTTLTarget1h); err == nil {
		return out
	}
	return body
}

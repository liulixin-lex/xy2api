package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const SettingKeyClaudeCacheFallbackPolicy = "claude_cache_fallback_policy"
const claudeCachePolicyTTL = time.Minute

// ClaudeCacheFallbackPolicy applies to every account, key and model in selected
// groups. It controls Anthropic wire declarations, never routing or settlement.
type ClaudeCacheFallbackPolicy struct {
	Enabled  bool    `json:"enabled"`
	GroupIDs []int64 `json:"group_ids"`
}

func emptyClaudeCachePolicy() ClaudeCacheFallbackPolicy {
	return ClaudeCacheFallbackPolicy{GroupIDs: []int64{}}
}

// ParseClaudeCacheFallbackPolicy validates the current write contract without
// contacting a provider. Old rule-shaped writes are rejected, not broadened.
func ParseClaudeCacheFallbackPolicy(raw []byte) (ClaudeCacheFallbackPolicy, error) {
	fail := func() (ClaudeCacheFallbackPolicy, error) {
		return emptyClaudeCachePolicy(), fmt.Errorf("invalid Claude cache fallback policy")
	}
	if len(raw) > 1024*1024 || !gjson.ValidBytes(raw) {
		return fail()
	}
	root := gjson.ParseBytes(raw)
	if !root.IsObject() || (root.Get("enabled").Type != gjson.True && root.Get("enabled").Type != gjson.False) || !root.Get("group_ids").IsArray() {
		return fail()
	}
	var p ClaudeCacheFallbackPolicy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || len(p.GroupIDs) > 1000 || (p.Enabled && len(p.GroupIDs) == 0) {
		return fail()
	}
	seen := map[int64]bool{}
	for _, id := range p.GroupIDs {
		if id <= 0 || seen[id] {
			return fail()
		}
		seen[id] = true
	}
	slices.Sort(p.GroupIDs)
	return p, nil
}

// Read legacy rules as disabled group selections. Upgrades must not silently
// turn an account/key/model allowlist into a group-wide enabled policy. An
// explicit save in the new UI opts into the new scope; no DB migration is needed.
func readClaudeCacheFallbackPolicy(raw []byte) ClaudeCacheFallbackPolicy {
	if p, err := ParseClaudeCacheFallbackPolicy(raw); err == nil {
		return p
	}
	p := emptyClaudeCachePolicy()
	root := gjson.ParseBytes(raw)
	if len(raw) > 1024*1024 || !gjson.ValidBytes(raw) || root.Get("group_ids").Exists() || !root.Get("rules").IsArray() {
		return p
	}
	var legacy struct {
		Rules []struct {
			GroupID int64 `json:"group_id"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil || len(legacy.Rules) > 100 {
		return p
	}
	for _, rule := range legacy.Rules {
		if rule.GroupID > 0 && !slices.Contains(p.GroupIDs, rule.GroupID) {
			p.GroupIDs = append(p.GroupIDs, rule.GroupID)
		}
	}
	slices.Sort(p.GroupIDs)
	return p
}

func (p ClaudeCacheFallbackPolicy) includesGroup(groupID int64) bool {
	return p.Enabled && groupID > 0 && slices.Contains(p.GroupIDs, groupID)
}

func (s *SettingService) validateClaudeCacheScope(ctx context.Context, p ClaudeCacheFallbackPolicy) error {
	if !p.Enabled {
		return nil
	}
	if s.claudeCacheGroupRepo == nil {
		return fmt.Errorf("claude cache group validation unavailable")
	}
	for _, id := range p.GroupIDs {
		group, err := s.claudeCacheGroupRepo.GetByID(ctx, id)
		if err != nil || group == nil || group.ID != id {
			return fmt.Errorf("invalid Claude cache group")
		}
	}
	return nil
}

// The group reader is only used on settings writes, never for each request.
func (s *SettingService) SetClaudeCacheGroupRepository(groups GroupRepository) {
	s.claudeCacheGroupRepo = groups
}

type cachedClaudeCachePolicy struct {
	policy    ClaudeCacheFallbackPolicy
	expiresAt time.Time
}

// The mutex serializes DB loads and writes so an old load cannot republish ON
// after a successful local disable. Expired failures always publish OFF.
func (s *SettingService) claudeCachePolicy(ctx context.Context) *cachedClaudeCachePolicy {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if p := s.claudeCacheSnapshot.Load(); p != nil && time.Now().Before(p.expiresAt) {
		return p
	}
	s.claudeCacheMu.Lock()
	defer s.claudeCacheMu.Unlock()
	if p := s.claudeCacheSnapshot.Load(); p != nil && time.Now().Before(p.expiresAt) {
		return p
	}
	dbctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbctx, SettingKeyClaudeCacheFallbackPolicy)
	p := emptyClaudeCachePolicy()
	ttl := claudeCachePolicyTTL
	if err == nil {
		p = readClaudeCacheFallbackPolicy([]byte(raw))
	}
	if err != nil {
		ttl = 5 * time.Second
		p = emptyClaudeCachePolicy()
	}
	entry := &cachedClaudeCachePolicy{policy: p, expiresAt: time.Now().Add(ttl)}
	s.claudeCacheSnapshot.Store(entry)
	return entry
}

func (s *SettingService) writeSettingsWithClaudeCache(ctx context.Context, updates map[string]string) error {
	raw, present := updates[SettingKeyClaudeCacheFallbackPolicy]
	if !present {
		return s.settingRepo.SetMultiple(ctx, updates)
	}
	p, err := ParseClaudeCacheFallbackPolicy([]byte(raw))
	if err != nil {
		return err
	}
	if err = s.validateClaudeCacheScope(ctx, p); err != nil {
		return infraerrors.BadRequest("INVALID_CLAUDE_CACHE_SCOPE", err.Error())
	}
	s.claudeCacheMu.Lock()
	defer s.claudeCacheMu.Unlock()
	if err = s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return err
	}
	s.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: p, expiresAt: time.Now().Add(claudeCachePolicyTTL)})
	return nil
}

func claudeCacheAccountSupported(a *Account) bool {
	return a != nil && a.Platform == PlatformAnthropic
}

// HasClaudeCacheControl checks only protocol locations; tool schema/input and
// strings with the same spelling are user data. Exists includes JSON null.
func HasClaudeCacheControl(body []byte) bool {
	root := gjson.ParseBytes(body)
	if root.Get("cache_control").Exists() {
		return true
	}
	found := false
	blocks := func(list gjson.Result) {
		if !list.IsArray() {
			return
		}
		list.ForEach(func(_, v gjson.Result) bool {
			if v.IsObject() && v.Get("cache_control").Exists() {
				found = true
			}
			return !found
		})
	}
	blocks(root.Get("system"))
	if found {
		return true
	}
	blocks(root.Get("tools"))
	if found {
		return true
	}
	messages := root.Get("messages")
	if messages.IsArray() {
		messages.ForEach(func(_, v gjson.Result) bool { blocks(v.Get("content")); return !found })
	}
	if found {
		return true
	}
	// Chat tools use function.cache_control; Responses messages use input.
	root.Get("tools").ForEach(func(_, v gjson.Result) bool {
		if v.Get("function.cache_control").Exists() {
			found = true
		}
		return !found
	})
	root.Get("input").ForEach(func(_, v gjson.Result) bool {
		if v.Get("type").String() == "message" || v.Get("role").Exists() {
			if v.Get("cache_control").Exists() {
				found = true
			}
			blocks(v.Get("content"))
		}
		return !found
	})
	return found
}

type claudeCacheRequestKey struct{}
type claudeCacheAttemptKey struct{}
type claudeCacheRequest struct {
	keyID, groupID int64
	declared       bool
	admitted       *cachedClaudeCachePolicy
}
type claudeCacheAttempt struct {
	request                *claudeCacheRequest
	groupID                int64
	account                *Account
	decision               *ClaudeCacheFallbackDecision
	cleanBody, patchedBody []byte
}

// ClaudeCacheFallbackDecision contains no request content or credentials.
type ClaudeCacheFallbackDecision struct {
	Reason           string      `json:"reason"`
	APIKeyID         int64       `json:"api_key_id"`
	GroupID          int64       `json:"group_id"`
	AccountID        int64       `json:"account_id"`
	RawUsage         ClaudeUsage `json:"raw_usage"`
	rawUsageObserved bool
}

// WithClaudeCacheFallbackRequest runs before any rewriting in Messages and
// Anthropic-compatible Chat/Responses handlers. Admission is frozen; a later enable cannot add costs to
// an already reserved request. Final attempts also consult the current policy.
func (s *GatewayService) WithClaudeCacheFallbackRequest(ctx context.Context, key *APIKey, body []byte) context.Context {
	if key == nil || key.GroupID == nil {
		return ctx
	}
	state := &claudeCacheRequest{keyID: key.ID, groupID: *key.GroupID, admitted: s.settingService.claudeCachePolicy(ctx)}
	if state.admitted != nil && state.admitted.policy.includesGroup(state.groupID) {
		state.declared = HasClaudeCacheControl(body)
	}
	return context.WithValue(ctx, claudeCacheRequestKey{}, state)
}

func (s *GatewayService) applyClaudeCacheFallback(ctx context.Context, a *Account, body []byte, _ string) []byte {
	attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt)
	if attempt == nil {
		return body
	}
	attempt.cleanBody, attempt.patchedBody = nil, nil
	d := attempt.decision
	d.Reason = "unsupported_route"
	if !claudeCacheAccountSupported(a) {
		return body
	}
	d.AccountID = a.ID
	state := attempt.request
	d.Reason = "disabled"
	current := s.settingService.claudeCachePolicy(ctx)
	if state.admitted == nil || !state.admitted.policy.Enabled || current == nil || !current.policy.Enabled {
		return body
	}
	d.Reason = "scope_mismatch"
	// A fallback group cannot acquire scope not reserved at admission.
	if state.groupID != attempt.groupID || !slices.Contains(a.GroupIDs, attempt.groupID) || !state.admitted.policy.includesGroup(attempt.groupID) || !current.policy.includesGroup(attempt.groupID) {
		return body
	}
	if state.declared {
		d.Reason = "client_declared"
		return body
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		d.Reason = "invalid_body"
		return body
	}
	if gjson.GetBytes(body, "cache_control").Exists() {
		d.Reason = "final_declared"
		return body
	}
	next := addClaudeCacheFallbackBreakpoints(body)
	if bytes.Equal(body, next) {
		d.Reason = "no_cacheable_content"
		return body
	}
	attempt.cleanBody, attempt.patchedBody = body, next
	d.Reason = "injected"
	return next
}

// Restore exactly the pre-injection wire body, including string content shape.
// Other account-specific transforms keep their previous synchronization semantics.
func withoutClaudeCacheFallback(ctx context.Context, wire []byte) []byte {
	attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt)
	if attempt != nil && attempt.patchedBody != nil && bytes.Equal(wire, attempt.patchedBody) {
		return attempt.cleanBody
	}
	return wire
}

// Place provider-side breakpoints on stable prefixes and the conversation tail.
// Like LiteLLM automatic caching we never replace a client strategy. Existing
// gateway/OAuth markers are counted and preserved; at most four blocks survive.
// The previous user anchor follows Sub2API's long-conversation strategy.
func addClaudeCacheFallbackBreakpoints(body []byte) []byte {
	invalid, messages, tools, system := collectCacheControlPaths(body)
	budget := maxCacheControlBlocks - len(invalid) - len(messages) - len(tools) - len(system)
	if budget <= 0 {
		return body
	}
	// Prefer the advancing tail, then static prefixes and the prior user turn.
	arr := gjson.GetBytes(body, "messages").Array()
	targets := []string{}
	if len(arr) > 0 {
		last := arr[len(arr)-1]
		if role := last.Get("role").String(); role == "user" || role == "assistant" {
			targets = append(targets, fmt.Sprintf("messages.%d.content", len(arr)-1))
		}
	}
	targets = append(targets, "system", "tools")
	if len(arr) >= 4 {
		users := 0
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Get("role").String() == "user" {
				users++
				if users == 2 {
					targets = append(targets, fmt.Sprintf("messages.%d.content", i))
					break
				}
			}
		}
	}
	for _, path := range targets {
		if budget <= 0 {
			break
		}
		content := gjson.GetBytes(body, path)
		var next []byte
		var err error
		if content.Type == gjson.String && content.String() != "" && path != "tools" {
			next, err = sjson.SetRawBytes(body, path, []byte(`[{"type":"text","text":`+mustJSONString(content.String())+`,"cache_control":{"type":"ephemeral"}}]`))
		} else if content.IsArray() {
			blocks := content.Array()
			for i := len(blocks) - 1; i >= 0; i-- {
				block := blocks[i]
				if !claudeCacheableBlock(block, path) {
					continue
				}
				// Do not walk past an existing checkpoint to add a redundant earlier one.
				if block.Get("cache_control").Exists() {
					break
				}
				next, err = sjson.SetRawBytes(body, fmt.Sprintf("%s.%d.cache_control", path, i), []byte(`{"type":"ephemeral"}`))
				break
			}
		}
		if err == nil && next != nil && claudeCacheTTLOrderValid(next) {
			body = next
			budget--
		}
	}
	return body
}

// Anthropic processes tools, system, then messages. One-hour checkpoints must
// precede five-minute checkpoints. Do not extend TTL (and cost) to fill a gap.
func claudeCacheTTLOrderValid(body []byte) bool {
	_, messages, tools, system := collectCacheControlPaths(body)
	paths := append(append(tools, system...), messages...)
	shortSeen := false
	for _, path := range paths {
		if gjson.GetBytes(body, path+".ttl").String() == "1h" {
			if shortSeen {
				return false
			}
		} else {
			shortSeen = true
		}
	}
	return true
}

func claudeCacheableBlock(block gjson.Result, path string) bool {
	if !block.IsObject() {
		return false
	}
	if path == "tools" {
		return block.Get("name").String() != "" && block.Get("defer_loading").Type != gjson.True
	}
	switch block.Get("type").String() {
	case "text":
		return block.Get("text").Type == gjson.String && block.Get("text").String() != ""
	case "image", "document", "tool_use", "tool_result":
		return path != "system"
	default:
		return false // Never mark thinking, redacted thinking or unknown blocks.
	}
}

func (s *GatewayService) beginClaudeCacheAttempt(ctx context.Context, parsed *ParsedRequest, a *Account) (context.Context, *ClaudeCacheFallbackDecision) {
	state, _ := ctx.Value(claudeCacheRequestKey{}).(*claudeCacheRequest)
	if state == nil {
		return ctx, nil
	}
	gid := int64(0)
	if parsed != nil && parsed.GroupID != nil {
		gid = *parsed.GroupID
	}
	d := &ClaudeCacheFallbackDecision{Reason: "unsupported_route", APIKeyID: state.keyID, GroupID: gid}
	if a != nil {
		d.AccountID = a.ID
	}
	return context.WithValue(ctx, claudeCacheAttemptKey{}, &claudeCacheAttempt{request: state, groupID: gid, account: a, decision: d}), d
}

// Capture numeric provider usage before existing TTL/force billing overrides.
func captureClaudeCacheRawUsage(ctx context.Context, usage ClaudeUsage) {
	attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt)
	if attempt != nil && attempt.decision.Reason == "injected" {
		attempt.decision.RawUsage = usage
		attempt.decision.rawUsageObserved = true
	}
}

func (s *GatewayService) captureClaudeCacheRawUsageEvent(ctx context.Context, event map[string]any) {
	attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt)
	if attempt != nil && attempt.decision.Reason == "injected" {
		patch := s.extractSSEUsagePatch(event)
		if patch == nil {
			return
		}
		mergeSSEUsagePatch(&attempt.decision.RawUsage, patch)
		attempt.decision.rawUsageObserved = true
	}
}

func finishClaudeCacheAttempt(d *ClaudeCacheFallbackDecision, result *ForwardResult) {
	if d == nil {
		return
	}
	requestID, model := "", ""
	if result != nil {
		if !d.rawUsageObserved {
			d.RawUsage = result.Usage
		}
		result.ClaudeCacheFallback = d
		requestID = result.RequestID
		model = result.UpstreamModel
		if model == "" {
			model = result.Model
		}
	}
	// Off is the default and should not generate a log line for every request.
	if d.Reason == "disabled" || d.Reason == "unsupported_route" {
		return
	}
	slog.Info("gateway.claude_cache_fallback", "request_id", requestID, "api_key_id", d.APIKeyID, "group_id", d.GroupID, "account_id", d.AccountID, "model", model, "reason", d.Reason)
}

func logClaudeCacheBilling(result *ForwardResult, usage *UsageLog, forced, overridden bool, outcome string) {
	d := result.ClaudeCacheFallback
	if d == nil || d.Reason != "injected" {
		return
	}
	raw := d.RawUsage
	ttlStatus := "consistent"
	if raw.CacheCreationInputTokens > 0 && raw.CacheCreation5mTokens+raw.CacheCreation1hTokens == 0 {
		ttlStatus = "missing"
	} else if raw.CacheCreationInputTokens != raw.CacheCreation5mTokens+raw.CacheCreation1hTokens {
		ttlStatus = "inconsistent"
	} else if raw.CacheCreation1hTokens > 0 {
		ttlStatus = "reported_1h"
	}
	slog.Info("gateway.claude_cache_fallback.billing", "request_id", usage.RequestID, "api_key_id", d.APIKeyID, "group_id", d.GroupID, "account_id", d.AccountID,
		"raw_input", raw.InputTokens, "raw_output", raw.OutputTokens, "raw_cache_creation", raw.CacheCreationInputTokens, "raw_cache_read", raw.CacheReadInputTokens, "raw_5m", raw.CacheCreation5mTokens, "raw_1h", raw.CacheCreation1hTokens, "ttl_status", ttlStatus,
		"billed_input", usage.InputTokens, "billed_output", usage.OutputTokens, "billed_cache_creation", usage.CacheCreationTokens, "billed_cache_read", usage.CacheReadTokens, "billed_5m", usage.CacheCreation5mTokens, "billed_1h", usage.CacheCreation1hTokens,
		"force_cache_billing", forced, "ttl_override", overridden, "usage_id", usage.ID, "total_cost", usage.TotalCost, "actual_cost", usage.ActualCost, "settlement", outcome)
}

func (s *GatewayService) applyClaudeCacheFallbackForAttempt(ctx context.Context, body []byte, model string) []byte {
	attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt)
	if attempt == nil {
		return body
	}
	return s.applyClaudeCacheFallback(ctx, attempt.account, body, model)
}

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/util/urlvalidator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const SettingKeyClaudeCacheFallbackPolicy = "claude_cache_fallback_policy"
const claudeCachePolicyTTL = time.Minute

// ClaudeCacheFallbackPolicy is an explicit allowlist, never a routing policy.
type ClaudeCacheFallbackPolicy struct {
	Enabled bool                      `json:"enabled"`
	Rules   []ClaudeCacheFallbackRule `json:"rules"`
}
type ClaudeCacheFallbackRule struct {
	ID        string   `json:"id"`
	GroupID   int64    `json:"group_id"`
	APIKeyIDs []int64  `json:"api_key_ids"`
	AccountID int64    `json:"account_id"`
	BaseURL   string   `json:"base_url"`
	Models    []string `json:"models"`
}

var claudeCacheRuleID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func emptyClaudeCachePolicy() ClaudeCacheFallbackPolicy {
	return ClaudeCacheFallbackPolicy{Rules: []ClaudeCacheFallbackRule{}}
}

// ParseClaudeCacheFallbackPolicy validates syntax without contacting any provider.
// Errors deliberately exclude submitted values (which may contain credentials).
func ParseClaudeCacheFallbackPolicy(raw []byte) (ClaudeCacheFallbackPolicy, error) {
	fail := func() (ClaudeCacheFallbackPolicy, error) {
		return emptyClaudeCachePolicy(), fmt.Errorf("invalid Claude cache fallback policy")
	}
	if len(raw) > 1024*1024 || !gjson.ValidBytes(raw) {
		return fail()
	}
	root := gjson.ParseBytes(raw)
	if !root.IsObject() || (root.Get("enabled").Type != gjson.True && root.Get("enabled").Type != gjson.False) || !root.Get("rules").IsArray() {
		return fail()
	}
	var p ClaudeCacheFallbackPolicy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || len(p.Rules) > 100 {
		return fail()
	}
	seen := map[string]bool{}
	for i := range p.Rules {
		r := &p.Rules[i]
		if !claudeCacheRuleID.MatchString(r.ID) || seen[r.ID] || r.GroupID <= 0 || r.AccountID <= 0 || len(r.APIKeyIDs) > 100 || len(r.Models) == 0 || len(r.Models) > 50 {
			return fail()
		}
		seen[r.ID] = true
		base, err := normalizeClaudeCacheBaseURL(r.BaseURL)
		if err != nil {
			return fail()
		}
		r.BaseURL = base
		keys := map[int64]bool{}
		for _, id := range r.APIKeyIDs {
			if id <= 0 || keys[id] {
				return fail()
			}
			keys[id] = true
		}
		if r.APIKeyIDs == nil {
			r.APIKeyIDs = []int64{}
		}
		models := map[string]bool{}
		for _, model := range r.Models {
			if model == "" || len(model) > 256 || strings.TrimSpace(model) != model || strings.ContainsAny(model, "*\r\n\t") || models[model] {
				return fail()
			}
			models[model] = true
		}
	}
	return p, nil
}

func normalizeClaudeCacheBaseURL(raw string) (string, error) {
	validated, err := urlvalidator.ValidateURLFormat(raw, true)
	if err != nil {
		return "", fmt.Errorf("invalid cache upstream URL")
	}
	u, err := url.Parse(validated)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery {
		return "", fmt.Errorf("invalid cache upstream URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	// Match the gateway's base path normalization; do not discard the path.
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *SettingService) validateClaudeCacheScope(ctx context.Context, p ClaudeCacheFallbackPolicy) error {
	if !p.Enabled || len(p.Rules) == 0 {
		return nil
	}
	if s.claudeCacheGroupRepo == nil || s.claudeCacheAccountRepo == nil || s.claudeCacheKeyRepo == nil {
		return fmt.Errorf("Claude cache scope validation unavailable")
	}
	for _, r := range p.Rules {
		group, err := s.claudeCacheGroupRepo.GetByID(ctx, r.GroupID)
		if err != nil || group == nil || (group.Platform != PlatformAnthropic && group.Platform != PlatformComposite) {
			return fmt.Errorf("Claude cache rule %s: invalid group", r.ID)
		}
		account, err := s.claudeCacheAccountRepo.GetByID(ctx, r.AccountID)
		if err != nil || !claudeCacheAccountSupported(account) || !slices.Contains(account.GroupIDs, r.GroupID) {
			return fmt.Errorf("Claude cache rule %s: account must be a native Anthropic API key in this group", r.ID)
		}
		base, err := normalizeClaudeCacheBaseURL(account.GetBaseURL())
		if err != nil || base != r.BaseURL {
			return fmt.Errorf("Claude cache rule %s: upstream URL does not match account", r.ID)
		}
		for _, id := range r.APIKeyIDs {
			key, err := s.claudeCacheKeyRepo.GetByID(ctx, id)
			if err != nil || key == nil || key.GroupID == nil || *key.GroupID != r.GroupID {
				return fmt.Errorf("Claude cache rule %s: API key must belong to this group", r.ID)
			}
		}
	}
	return nil
}

// Repo readers are used only on an explicit settings write, never per request.
func (s *SettingService) SetClaudeCacheScopeRepositories(groups GroupRepository, accounts AccountRepository, keys APIKeyRepository) {
	s.claudeCacheGroupRepo = groups
	s.claudeCacheAccountRepo = accounts
	s.claudeCacheKeyRepo = keys
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
		p, err = ParseClaudeCacheFallbackPolicy([]byte(raw))
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
	return a != nil && a.Platform == PlatformAnthropic && a.Type == AccountTypeAPIKey && !a.IsAnthropicAPIKeyPassthroughEnabled() && !a.IsBedrock()
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
	request  *claudeCacheRequest
	groupID  int64
	decision *ClaudeCacheFallbackDecision
}

// ClaudeCacheFallbackDecision contains no request content or credentials.
type ClaudeCacheFallbackDecision struct {
	RuleID    string      `json:"rule_id"`
	Reason    string      `json:"reason"`
	APIKeyID  int64       `json:"api_key_id"`
	GroupID   int64       `json:"group_id"`
	AccountID int64       `json:"account_id"`
	RawUsage  ClaudeUsage `json:"raw_usage"`
}

// WithClaudeCacheFallbackRequest must run only in the native Messages handler,
// before any rewriting. Admission is frozen; a later enable cannot add costs to
// an already reserved request. Final attempts also consult the current policy.
func (s *GatewayService) WithClaudeCacheFallbackRequest(ctx context.Context, key *APIKey, body []byte) context.Context {
	if key == nil || key.GroupID == nil {
		return ctx
	}
	state := &claudeCacheRequest{keyID: key.ID, groupID: *key.GroupID, admitted: s.settingService.claudeCachePolicy(ctx)}
	if state.admitted != nil && len(state.admitted.policy.scopeRules(state.groupID, state.keyID)) > 0 {
		state.declared = HasClaudeCacheControl(body)
	}
	return context.WithValue(ctx, claudeCacheRequestKey{}, state)
}

func (p ClaudeCacheFallbackPolicy) scopeRules(groupID, keyID int64) []ClaudeCacheFallbackRule {
	if !p.Enabled {
		return nil
	}
	var rules []ClaudeCacheFallbackRule
	for _, r := range p.Rules {
		if r.GroupID == groupID && (len(r.APIKeyIDs) == 0 || slices.Contains(r.APIKeyIDs, keyID)) {
			rules = append(rules, r)
		}
	}
	return rules
}

func claudeCacheMatchingRule(p *cachedClaudeCachePolicy, groupID, keyID int64, a *Account, base, model string) string {
	if p == nil || !p.policy.Enabled {
		return ""
	}
	for _, r := range p.policy.Rules {
		if r.GroupID == groupID && (len(r.APIKeyIDs) == 0 || slices.Contains(r.APIKeyIDs, keyID)) && r.AccountID == a.ID && r.BaseURL == base && slices.Contains(r.Models, model) {
			return r.ID
		}
	}
	return ""
}

func (s *GatewayService) applyClaudeCacheFallback(ctx context.Context, a *Account, body []byte, model string) []byte {
	attempt, _ := ctx.Value(claudeCacheAttemptKey{}).(*claudeCacheAttempt)
	if attempt == nil {
		return body
	}
	d := attempt.decision
	d.Reason = "unsupported_route"
	if !claudeCacheAccountSupported(a) {
		return body
	}
	d.AccountID = a.ID
	state := attempt.request
	if state.declared {
		d.Reason = "client_declared"
		return body
	}
	d.Reason = "disabled"
	current := s.settingService.claudeCachePolicy(ctx)
	if state.admitted == nil || !state.admitted.policy.Enabled || current == nil || !current.policy.Enabled {
		return body
	}
	d.Reason = "scope_mismatch"
	// A group fallback must never acquire a scope not priced at initial admission.
	if state.groupID != attempt.groupID || !slices.Contains(a.GroupIDs, attempt.groupID) {
		return body
	}
	base, err := normalizeClaudeCacheBaseURL(a.GetBaseURL())
	if err != nil {
		return body
	}
	if claudeCacheMatchingRule(state.admitted, attempt.groupID, state.keyID, a, base, model) == "" {
		return body
	}
	rule := claudeCacheMatchingRule(current, attempt.groupID, state.keyID, a, base, model)
	if rule == "" {
		return body
	}
	d.RuleID = rule
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		d.Reason = "invalid_body"
		return body
	}
	if HasClaudeCacheControl(body) {
		d.Reason = "final_declared"
		return body
	}
	next, err := sjson.SetRawBytes(body, "cache_control", []byte(`{"type":"ephemeral"}`))
	if err != nil {
		d.Reason = "invalid_body"
		return body
	}
	d.Reason = "injected"
	return next
}

func (s *GatewayService) beginClaudeCacheAttempt(ctx context.Context, parsed *ParsedRequest, a *Account) (context.Context, *ClaudeCacheFallbackDecision) {
	state, _ := ctx.Value(claudeCacheRequestKey{}).(*claudeCacheRequest)
	if state == nil {
		return ctx, nil
	}
	gid := int64(0)
	if parsed.GroupID != nil {
		gid = *parsed.GroupID
	}
	d := &ClaudeCacheFallbackDecision{Reason: "unsupported_route", APIKeyID: state.keyID, GroupID: gid}
	if a != nil {
		d.AccountID = a.ID
	}
	return context.WithValue(ctx, claudeCacheAttemptKey{}, &claudeCacheAttempt{request: state, groupID: gid, decision: d}), d
}

func finishClaudeCacheAttempt(d *ClaudeCacheFallbackDecision, result *ForwardResult) {
	if d == nil {
		return
	}
	requestID, model := "", ""
	if result != nil {
		d.RawUsage = result.Usage
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
	slog.Info("gateway.claude_cache_fallback", "request_id", requestID, "rule_id", d.RuleID, "api_key_id", d.APIKeyID, "group_id", d.GroupID, "account_id", d.AccountID, "model", model, "reason", d.Reason)
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
		ttlStatus = "unexpected_1h"
	}
	slog.Info("gateway.claude_cache_fallback.billing", "request_id", usage.RequestID, "rule_id", d.RuleID, "api_key_id", d.APIKeyID, "group_id", d.GroupID, "account_id", d.AccountID,
		"raw_input", raw.InputTokens, "raw_output", raw.OutputTokens, "raw_cache_creation", raw.CacheCreationInputTokens, "raw_cache_read", raw.CacheReadInputTokens, "raw_5m", raw.CacheCreation5mTokens, "raw_1h", raw.CacheCreation1hTokens, "ttl_status", ttlStatus,
		"billed_input", usage.InputTokens, "billed_output", usage.OutputTokens, "billed_cache_creation", usage.CacheCreationTokens, "billed_cache_read", usage.CacheReadTokens, "billed_5m", usage.CacheCreation5mTokens, "billed_1h", usage.CacheCreation1hTokens,
		"force_cache_billing", forced, "ttl_override", overridden, "usage_id", usage.ID, "total_cost", usage.TotalCost, "actual_cost", usage.ActualCost, "settlement", outcome)
}

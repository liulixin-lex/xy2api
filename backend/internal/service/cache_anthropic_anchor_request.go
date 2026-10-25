package service

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type cacheAnchorDecision struct {
	policy    cacheAnthropicAnchorPolicy
	arm, skip string
	groupID   int64
	trace     *cacheTrace
	lease     *cacheAnchorLease
}

// This predicate is shared with the selector's resolver. It has no IO and does
// not depend on metadata/session seeds or observation state.
func cacheClaudeCodeFallbackNeeded(group *Group, claudeCode bool) bool {
	return group != nil && group.ClaudeCodeOnly && !claudeCode
}

func (s *GatewayService) CacheAnchorEnabled(c *gin.Context) bool {
	return s != nil && s.providerTraceEngine != nil && s.providerTraceEngine.CaptureCachePolicy(c).cacheAnchorEnabled()
}

// PrepareCacheAnchor runs before sticky reads and scheduling. Off/capability
// false returns the same key and never parses, resolves, hashes or allocates.
// A resolved fallback key is a request-local clone; the authenticated ID is kept.
func (s *GatewayService) PrepareCacheAnchor(c *gin.Context, key *APIKey, parsed *ParsedRequest, platform string, countTokens bool) *APIKey {
	return s.prepareCacheAnchor(c, key, parsed, platform, countTokens, true)
}

// PrepareCompatCacheAnchor keeps compatibility ingress on its original hash.
// Native text provenance comes from the handler entry, never from body shape.
func (s *GatewayService) PrepareCompatCacheAnchor(c *gin.Context, key *APIKey, parsed *ParsedRequest, platform string) *APIKey {
	return s.prepareCacheAnchor(c, key, parsed, platform, false, false)
}

func (s *GatewayService) prepareCacheAnchor(c *gin.Context, key *APIKey, parsed *ParsedRequest, platform string, countTokens, native bool) *APIKey {
	if !s.CacheAnchorEnabled(c) {
		return key
	}
	p := s.providerTraceEngine.CaptureCachePolicy(c)
	if countTokens {
		if t := cacheTraceFromGin(c); t != nil {
			t.mu.Lock()
			t.data.L04Skip = "count_tokens"
			t.mu.Unlock()
		}
		return key
	}
	if parsed == nil {
		return key
	}
	s.ReleaseCacheAnchor(parsed)
	parsed.AnchorV2 = CacheAnchorV2{}
	d := &cacheAnchorDecision{policy: p.anchorPolicy, arm: "none", trace: cacheTraceFromGin(c)}
	parsed.cacheL04 = d
	if d.trace != nil {
		parsed.cacheTraceSource = &d.trace.data.IDSource
	}
	defer d.publish()
	if key == nil || key.ID <= 0 {
		d.skip = "no_api_key"
		return key
	}
	if key.Group == nil || key.GroupID == nil || key.Group.Platform != platform || (platform != PlatformAnthropic && platform != PlatformGemini) {
		d.skip = "not_anthropic_group"
		return key
	}
	group := key.Group
	// ForcePlatform deliberately bypasses the selector's Claude Code restriction.
	force, _ := c.Request.Context().Value(ctxkey.ForcePlatform).(string)
	if force == "" && cacheClaudeCodeFallbackNeeded(group, IsClaudeCodeClient(c.Request.Context())) {
		resolved, _, err := s.resolveGatewayGroup(c.Request.Context(), key.GroupID)
		if err != nil || resolved == nil {
			d.skip = "group_unresolved"
			return key
		}
		group = resolved
	}
	if group.ID <= 0 {
		d.skip = "group_unresolved"
		return key
	}
	if group.Platform != platform {
		d.skip = "not_anthropic_group"
		return key
	}
	if group.ID != *key.GroupID {
		clone := *key
		clone.Group = group
		clone.GroupID = &group.ID
		key = &clone
	}
	// Only routing/sticky consumers use this serving clone. Authentication,
	// billing and existing group-level conversions retain their original key.
	parsed.GroupID = key.GroupID
	d.groupID = group.ID
	if platform == PlatformGemini {
		if !d.policy.gemini {
			d.skip = "not_in_group"
			return key
		}
	} else {
		listed := false
		for _, id := range d.policy.groups {
			if id == group.ID {
				listed = true
				break
			}
		}
		if !listed {
			d.skip = "not_in_group"
			return key
		}
	}
	if s.shareScheduling.shareSchedulingActive(group.ID) {
		d.skip = "share_active"
		return key
	}
	if !native {
		d.skip = "empty_anchor"
		return key
	}
	secret := ""
	if s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	value := deriveCacheAnchorV2(secret, key.ID, group.ID, parsed.Model, platform, parsed.SystemRaw(), parsed.MessagesRaw())
	parsed.AnchorV2 = CacheAnchorV2{value: value, servingGroupID: group.ID, policyRev: d.policy.revision}
	if value == "" {
		d.skip = "empty_anchor"
		return key
	}
	d.arm = parsed.AnchorV2.armForGroup(group.ID, d.policy.rollout, d.policy.metadataRollout, d.policy.metadataAvailable)
	if d.arm == "off" {
		d.skip = "arm_off"
	}
	return key
}

func (d *cacheAnchorDecision) publish() {
	if d == nil || d.trace == nil {
		return
	}
	t := d.trace
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.L04Present = true
	t.data.L04Arm, t.data.L04Skip = d.arm, d.skip
	if t.data.L04Skip == "" {
		t.data.L04Skip = "earlier_tier"
	}
	t.data.L04Applied = d.skip == "none"
	t.data.L04PolicyRevision = d.policy.revision
	if d.groupID > 0 {
		t.data.GroupID = d.groupID
		t.providerServingGroupID = d.groupID
	}
}

func (s *GatewayService) cacheAnchorEarlierTier(parsed *ParsedRequest) {
	if d := parsed.cacheL04; d != nil && d.skip == "" {
		d.skip = "earlier_tier"
		d.publish()
	}
}

func (s *GatewayService) cacheAnchorSession(parsed *ParsedRequest) string {
	d := parsed.cacheL04
	if d == nil || (d.skip != "" && d.skip != "none") {
		return ""
	}
	groupID := derefGroupID(parsed.GroupID)
	value := parsed.AnchorV2.candidateForGroup(groupID)
	if value == "" || groupID != d.groupID {
		d.skip = "scope_mismatch"
		parsed.AnchorV2.armed = false
		d.publish()
		return ""
	}
	if d.lease == nil {
		s.anchorInflightOnce.Do(func() { s.anchorInflight.capacity = cacheAnchorInflightHardCap })
		lease, ok := s.anchorInflight.acquire(value, d.policy.inflight)
		if !ok {
			d.skip = "fanin_cap"
			d.publish()
			resilience.Count("l04_fanin_skip_total", groupID, 0, "")
			return ""
		}
		d.lease = lease
	}
	parsed.AnchorV2.armed = true
	d.skip = "none"
	d.publish()
	if parsed.cacheTraceSource != nil {
		*parsed.cacheTraceSource = "anchor_v2"
	}
	return parsed.AnchorV2.sessionForGroup(groupID)
}

func (s *GatewayService) ReleaseCacheAnchor(parsed *ParsedRequest) {
	if parsed != nil && parsed.cacheL04 != nil && parsed.cacheL04.lease != nil {
		parsed.cacheL04.lease.release()
		parsed.cacheL04.lease = nil
	}
}

func (s *GatewayService) CacheAnchorApplied(parsed *ParsedRequest) bool {
	return parsed != nil && parsed.cacheL04 != nil && parsed.cacheL04.skip == "none" &&
		parsed.AnchorV2.sessionForGroup(derefGroupID(parsed.GroupID)) != ""
}

func (s *GatewayService) CacheAnchorEarlierTier(parsed *ParsedRequest) {
	if parsed != nil {
		s.cacheAnchorEarlierTier(parsed)
	}
}

// GenerateCacheAnchorSession is used by the native Gemini handler after its
// CLI tier and before its pre-existing digest fallback.
func (s *GatewayService) GenerateCacheAnchorSession(parsed *ParsedRequest) string {
	if parsed == nil {
		return ""
	}
	return s.cacheAnchorSession(parsed)
}

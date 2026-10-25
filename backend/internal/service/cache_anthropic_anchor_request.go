package service

import (
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

type cacheAnchorDecision struct {
	policy                             cacheAnthropicAnchorPolicy
	arm, skip                          string
	groupID                            int64
	trace                              *cacheTrace
	lease                              *cacheAnchorLease
	platform                           string
	metadataEnabled                    bool
	clientUserID                       bool
	metadataAttempted, metadataApplied bool
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
// The serving group is private until an anchor is actually selected. Skipped
// requests retain their original routing/sticky namespace and authenticated key.
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
			if platform == PlatformAnthropic && p.anchorPolicy.metadataAvailable {
				t.data.L03Present = true
				t.data.L03Arm = "none"
				t.data.L03Skip = "count_tokens"
				t.data.L03Applied = false
			}
			t.mu.Unlock()
		}
		return key
	}
	if parsed == nil {
		return key
	}
	s.ReleaseCacheAnchor(parsed)
	parsed.AnchorV2 = CacheAnchorV2{}
	parsed.cacheL04 = nil
	d := cacheAnchorDecision{policy: p.anchorPolicy, arm: "none", trace: cacheTraceFromGin(c), platform: platform, metadataEnabled: p.cacheMetadataEnabled()}
	if d.trace != nil {
		parsed.cacheTraceSource = &d.trace.data.IDSource
	}
	if s.cacheAnchorScope(c, key, platform, native, &d) {
		s.deriveCacheAnchor(key, parsed, platform, &d)
	}
	// Every skip is final and is only recorded. A decision outlives preparation
	// (and is allocated) only while a later tier can still apply it.
	if d.skip != "" {
		d.publish()
		return key
	}
	// Only the L-03 arm reads the client value, still from the original body.
	if d.metadataEnabled && d.arm == "both" {
		d.clientUserID = cacheMetadataHasClientValue(gjson.GetBytes(parsed.Body.Bytes(), "metadata.user_id"))
	}
	retained := d
	parsed.cacheL04 = &retained
	retained.publish()
	return key
}

// CacheAnchorOutOfScope decides and records the skips that need no request
// body, so a caller that parses only for the anchor can return before parsing.
func (s *GatewayService) CacheAnchorOutOfScope(c *gin.Context, key *APIKey, platform string) bool {
	if !s.CacheAnchorEnabled(c) {
		return true
	}
	d := cacheAnchorDecision{policy: s.providerTraceEngine.CaptureCachePolicy(c).anchorPolicy, arm: "none", trace: cacheTraceFromGin(c), platform: platform}
	if s.cacheAnchorScope(c, key, platform, true, &d) {
		return false
	}
	d.publish()
	return true
}

// cacheAnchorScope makes the skip decisions that precede the anchor, in trace
// order. An out-of-scope request returns before any allocation or group IO.
func (s *GatewayService) cacheAnchorScope(c *gin.Context, key *APIKey, platform string, native bool, d *cacheAnchorDecision) bool {
	if key == nil || key.ID <= 0 {
		d.skip = "no_api_key"
		return false
	}
	if key.Group == nil || key.GroupID == nil || key.Group.Platform != platform || (platform != PlatformAnthropic && platform != PlatformGemini) {
		d.skip = "not_anthropic_group"
		return false
	}
	group := key.Group
	// ForcePlatform deliberately bypasses the selector's Claude Code restriction.
	force, _ := c.Request.Context().Value(ctxkey.ForcePlatform).(string)
	if force == "" && cacheClaudeCodeFallbackNeeded(group, IsClaudeCodeClient(c.Request.Context())) {
		// A fallback group cannot itself be claude_code_only (validateFallbackGroup),
		// so the direct fallback is the serving group; an unlisted one needs no lookup.
		if next := group.FallbackGroupID; next != nil && *next > 0 && !d.policy.servesGroup(platform, *next) {
			d.groupID, d.skip = *next, "not_in_group"
			return false
		}
		resolved, _, err := s.resolveGatewayGroup(c.Request.Context(), key.GroupID)
		if err != nil || resolved == nil {
			d.skip = "group_unresolved"
			return false
		}
		group = resolved
	}
	if group.ID <= 0 {
		d.skip = "group_unresolved"
		return false
	}
	if group.Platform != platform {
		d.skip = "not_anthropic_group"
		return false
	}
	// Resolution is needed for anchor scope, eligibility and trace only. Returning
	// a serving clone here would also change skipped/control requests' sticky IO.
	d.groupID = group.ID
	if !d.policy.servesGroup(platform, group.ID) {
		d.skip = "not_in_group"
		return false
	}
	if s.shareScheduling.shareSchedulingActive(group.ID) {
		d.skip = "share_active"
		return false
	}
	if !native {
		d.skip = "empty_anchor"
		return false
	}
	return true
}

func (s *GatewayService) deriveCacheAnchor(key *APIKey, parsed *ParsedRequest, platform string, d *cacheAnchorDecision) {
	secret := ""
	if s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	value := deriveCacheAnchorV2(secret, key.ID, d.groupID, parsed.Model, platform, parsed.SystemRaw(), parsed.MessagesRaw())
	parsed.AnchorV2 = CacheAnchorV2{value: value, servingGroupID: d.groupID, policyRev: d.policy.revision}
	if value == "" {
		d.skip = "empty_anchor"
		return
	}
	d.arm = parsed.AnchorV2.armForGroup(d.groupID, d.policy.rollout, d.policy.metadataRollout, d.policy.metadataAvailable && platform == PlatformAnthropic && slices.Contains(d.policy.metadataGroups, d.groupID))
	if d.arm == "off" {
		d.skip = "arm_off"
	}
}

func (d *cacheAnchorDecision) publish() {
	if d == nil || d.trace == nil {
		return
	}
	if !d.metadataAttempted {
		defer d.publishMetadata(d.metadataInitialSkip())
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
	groupID := d.groupID
	value := parsed.AnchorV2.candidateForGroup(groupID)
	if value == "" {
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
	// This is the only point where serving scope becomes visible to routing.
	// Metadata/cacheable/CLI winners, control arms and exhausted leases never
	// reach it, so their GroupID and sticky namespace remain the baseline ones.
	parsed.GroupID = &d.groupID
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

// CacheAnchorPending reports a prepared candidate that hashing may still apply.
func (s *GatewayService) CacheAnchorPending(parsed *ParsedRequest) bool {
	return parsed != nil && parsed.cacheL04 != nil && parsed.cacheL04.skip == ""
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

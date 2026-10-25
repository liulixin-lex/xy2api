package service

import "slices"

// Runtime policy is frozen at publication. The lists are private and never
// returned to request callers; observations are not inputs to this policy.
type cacheAnthropicAnchorPolicy struct {
	available, metadataAvailable, valid, gemini bool
	groups, metadataGroups, metadataAccounts    []int64
	rollout, metadataRollout, inflight          int
	revision                                    int64
}

func cacheAnthropicAnchorPolicyFromEntry(e cachePolicyEntry, caps CacheM4BuildCapabilities) cacheAnthropicAnchorPolicy {
	s := e.envelope.Spec
	groups, _ := s["anchor_group_ids"].([]int64)
	gemini, _ := s["gemini_anchor"].(bool)
	metadataGroups, _ := s["metadata_group_ids"].([]int64)
	metadataAccounts, _ := s["metadata_account_ids"].([]int64)
	return cacheAnthropicAnchorPolicy{available: caps.L04, metadataAvailable: caps.L03 && caps.L04,
		valid: !e.parseError, groups: groups, metadataGroups: metadataGroups, metadataAccounts: metadataAccounts, gemini: gemini, rollout: policyInt(s["rollout_percent"]),
		metadataRollout: policyInt(s["metadata_rollout_percent"]), inflight: policyInt(s["max_inflight_per_anchor"]), revision: e.envelope.Rev}
}

func (p *CachePolicySnapshot) cacheAnchorEnabled() bool {
	return p != nil && p.anchorPolicy.available && !p.behaviorStale() && p.MasterEnabled() &&
		p.anchorPolicy.valid && p.anchorPolicy.rollout > 0 && (len(p.anchorPolicy.groups) > 0 || p.anchorPolicy.gemini)
}

// Gemini anchors are scoped by gemini_anchor alone, Anthropic ones by group.
func (p *cacheAnthropicAnchorPolicy) servesGroup(platform string, groupID int64) bool {
	if platform == PlatformGemini {
		return p.gemini
	}
	return slices.Contains(p.groups, groupID)
}

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// affinity_mode values of the routing policy (cache_policies l09a §1).
// prev_host+quality belongs to L-10 and stays unsupported_in_build.
const (
	cacheAffinityScore    = "score"
	cacheAffinityPrevHost = "prev_host"
)

// CacheRoutingL09APolicy is the typed affinity policy of one snapshot (cache
// T3.1a). It is built once at publication; requests read it through the
// snapshot pointer and never allocate.
type CacheRoutingL09APolicy struct {
	RolloutPercent int
	revision       int64
	active         bool
	// since is when this process first published the policy active; AUTH
	// owners are host suspects until the 10-minute counter window filled.
	since time.Time
	// fallbackTargets are the groups that an availability_fallback_group_id
	// references, read with the settings refresh. L-09a never applies there.
	fallbackTargets map[int64]struct{}
}

// cacheL09AStaticReady is the static precondition: the existing static
// cache-aware routing with a positive rollout. Production has it off, so
// L-09a never runs there until B8 is decided.
func cacheL09AStaticReady(aware bool, rollout int) bool { return aware && rollout > 0 }

func cacheAffinityMode(spec map[string]any) string {
	mode, _ := spec["affinity_mode"].(string)
	if mode == "" {
		return cacheAffinityScore
	}
	return mode
}

// routingL09AFromEntry folds the build capability, the kill and master
// switches, the static precondition, the entry's validity and a known group
// role into active, so the request path has a single check.
func routingL09AFromEntry(entry cachePolicyEntry, master, capable, static bool, targets map[int64]struct{}, targetsKnown bool, prev *CachePolicySnapshot, now time.Time) CacheRoutingL09APolicy {
	spec := entry.envelope.Spec
	p := CacheRoutingL09APolicy{
		revision:        entry.envelope.Rev,
		RolloutPercent:  max(0, min(policyInt(spec["affinity_rollout_percent"]), 100)),
		fallbackTargets: targets,
	}
	// Without the group roles a fallback target cannot be excluded: fail closed.
	p.active = capable && master && static && targetsKnown && !entry.parseError &&
		cacheAffinityMode(spec) == cacheAffinityPrevHost && p.RolloutPercent > 0
	if p.active {
		p.since = now
		if prev != nil && prev.routingL09A.active && !prev.stale {
			p.since = prev.routingL09A.since
		}
	}
	return p
}

// RoutingL09A returns the request policy of L-09a when it is active, nil
// otherwise. Routing is a behavior key, so a stale-bounded snapshot turns it
// off (FX-POL-2).
func (p *CachePolicySnapshot) RoutingL09A() *CacheRoutingL09APolicy {
	if p == nil || !p.routingL09A.active || p.behaviorStale() {
		return nil
	}
	return &p.routingL09A
}

func (p *CacheRoutingL09APolicy) fallbackTarget(groupID int64) bool {
	if p == nil {
		return false
	}
	_, ok := p.fallbackTargets[groupID]
	return ok
}

func (p *CacheRoutingL09APolicy) Revision() int64 {
	if p == nil {
		return 0
	}
	return p.revision
}

// l09aWantsGroupRoles reports whether the raw routing setting selects
// prev_host in a build that can apply it. Only then does a refresh read the
// group roles; every other configuration reads nothing new.
func (s *CachePolicyService) l09aWantsGroupRoles(raw string) bool {
	if !s.m4Available || !s.m4Behaviors.L09A || !cacheL09AStaticReady(s.aware, s.rollout) || raw == "" {
		return false
	}
	var stored struct {
		Spec struct {
			AffinityMode string `json:"affinity_mode"`
		} `json:"spec"`
	}
	return json.Unmarshal([]byte(raw), &stored) == nil && stored.Spec.AffinityMode == cacheAffinityPrevHost
}

// l09aLoadGroupRoles reads the fallback targets with the administrator group
// catalog (one paged read per refresh, outside the request path). Only the
// target IDs are kept; the catalog itself is discarded.
func (s *CachePolicyService) l09aLoadGroupRoles(ctx context.Context) (map[int64]struct{}, bool) {
	catalog, err := s.cachePolicyGroups(ctx)
	if err != nil {
		slog.Warn("cache.l09a_group_roles_unavailable")
		return nil, false
	}
	targets := make(map[int64]struct{}, len(catalog.fallback))
	for id := range catalog.fallback {
		targets[id] = struct{}{}
	}
	return targets, true
}

// l09aStaticBlocked reports the static_aware_routing_disabled blocker: the
// affinity is selected while the static cache-aware routing cannot run.
func (s *CachePolicyService) l09aStaticBlocked(spec map[string]any) bool {
	return cacheAffinityMode(spec) != cacheAffinityScore && !cacheL09AStaticReady(s.aware, s.rollout)
}

// l09aRolloutZero reports prev_host selected in a build that implements it,
// with affinity_rollout_percent 0: stored, but without effect.
func (s *CachePolicyService) l09aRolloutZero(spec map[string]any) bool {
	return s.m4Behaviors.L09A && cacheAffinityMode(spec) != cacheAffinityScore && policyInt(spec["affinity_rollout_percent"]) == 0
}

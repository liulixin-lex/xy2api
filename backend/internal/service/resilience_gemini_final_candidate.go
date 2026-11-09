package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type geminiCandidateUniverseKey struct{}
type geminiCandidateUniverse struct {
	group      *Group
	allowMixed bool
}

// WithGeminiCandidateUniverse carries the authenticated route's static group.
func WithGeminiCandidateUniverse(ctx context.Context, group *Group, allowMixed bool) context.Context {
	return context.WithValue(ctx, geminiCandidateUniverseKey{}, geminiCandidateUniverse{group, allowMixed})
}

// Only a complete, singleton candidate universe may commit an early heartbeat.
// Dynamic penalties, model limits, exclusions and retry counts never shrink it.
func geminiSoleCandidateUniverse(ctx context.Context, account *Account) bool {
	st, r := resilience.StateFrom(ctx), Resilience()
	route, known := ctx.Value(geminiCandidateUniverseKey{}).(geminiCandidateUniverse)
	if !known {
		route.group, known = ctx.Value(ctxkey.Group).(*Group)
		route.allowMixed = true
	}
	if account == nil || r == nil || r.fallbackAccounts == nil || !known || route.group == nil || route.group.ID != st.GroupID() {
		return false
	}
	// Reuse the same cached group list that 7797dcf4d's soleCandidate read.
	accounts, err := r.groupAccountsForLastResort(ctx, r.fallbackAccounts, st.GroupID())
	if err != nil {
		return false
	}
	groups := []*Group{route.group}
	if origin := st.OriginGroupID(); origin != 0 && origin != route.group.ID {
		key, ok := st.FallbackOrigin().(*APIKey)
		if !ok || key.Group == nil || key.Group.ID != origin {
			return false
		}
		groups = append(groups, key.Group)
	}
	seen, found, now := map[int64]bool{}, false, time.Now()
	for len(groups) > 0 {
		group := groups[0]
		groups = groups[1:]
		if seen[group.ID] {
			continue
		}
		seen[group.ID] = true
		if group.Platform == "" {
			return false
		}
		members := accounts
		if group.ID != st.GroupID() {
			// Other sources must already have a scheduling snapshot. Do not
			// fetch fallback groups/accounts merely to enable a heartbeat.
			r.lastResortGroups.mu.Lock()
			entry, ok := r.lastResortGroups.entries[group.ID]
			r.lastResortGroups.mu.Unlock()
			if !ok || !now.Before(entry.expires) {
				return false
			}
			members = entry.accounts
		}
		for i := range members {
			a := &members[i]
			mixed := route.allowMixed && (group.Platform == PlatformGemini || group.Platform == PlatformAnthropic)
			eligible := a.Platform == group.Platform || (mixed && a.Platform == PlatformAntigravity && a.IsMixedSchedulingEnabled())
			if !eligible {
				continue
			}
			if a.ID != account.ID {
				return false
			}
			found = true
		}
		for _, next := range []*int64{group.FallbackGroupID, group.FallbackGroupIDOnInvalidRequest, group.AvailabilityFallbackGroupID} {
			if next == nil || seen[*next] {
				continue
			}
			r.fallbackGroupCache.mu.Lock()
			entry, ok := r.fallbackGroupCache.entries[*next]
			r.fallbackGroupCache.mu.Unlock()
			if !ok || entry.group == nil || !now.Before(entry.expires) {
				return false
			}
			groups = append(groups, entry.group)
		}
	}
	return found
}

type geminiFinalKeepaliveKey struct{}
type geminiFinalKeepalive struct {
	accountID int64
	heartbeat *openAICompactSSEKeepalive
}

// GeminiFinalKeepaliveCommitted prevents later selection changes (including
// revived candidates and cross-platform fallbacks) from switching a committed request.
func GeminiFinalKeepaliveCommitted(ctx context.Context, accountID int64) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(geminiFinalKeepaliveKey{}).(geminiFinalKeepalive)
	if !ok || v.accountID != accountID {
		return false
	}
	v.heartbeat.mu.Lock()
	defer v.heartbeat.mu.Unlock()
	return v.heartbeat.started
}

// GeminiFinalKeepaliveBlocksAccount also checks the next selection: a permitted
// same-account retry must not dispatch to a different account or platform.
func GeminiFinalKeepaliveBlocksAccount(ctx context.Context, account *Account) bool {
	if ctx == nil || account == nil {
		return false
	}
	v, ok := ctx.Value(geminiFinalKeepaliveKey{}).(geminiFinalKeepalive)
	return ok && (v.accountID != account.ID || account.Platform != PlatformGemini) &&
		GeminiFinalKeepaliveCommitted(ctx, v.accountID)
}

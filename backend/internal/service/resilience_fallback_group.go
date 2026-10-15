package service

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// validateAvailabilityFallbackGroup checks an availability fallback group
// before it is saved (design §5): no self reference, no cycle, the same
// platform, and balance billing on both sides (the first version does not
// move requests between subscription groups).
func (s *adminServiceImpl) validateAvailabilityFallbackGroup(ctx context.Context, currentGroupID int64, platform, subscriptionType string, fallbackGroupID int64) error {
	if currentGroupID > 0 && currentGroupID == fallbackGroupID {
		return infraerrors.BadRequest("AVAILABILITY_FALLBACK_SELF", "a group cannot be its own availability fallback")
	}
	if subscriptionType == SubscriptionTypeSubscription {
		return infraerrors.BadRequest("AVAILABILITY_FALLBACK_SUBSCRIPTION", "subscription groups cannot set an availability fallback group")
	}
	fallback, err := s.groupRepo.GetByIDLite(ctx, fallbackGroupID)
	if err != nil || fallback == nil {
		return infraerrors.BadRequest("AVAILABILITY_FALLBACK_NOT_FOUND", "availability fallback group not found")
	}
	if fallback.Platform != platform {
		return infraerrors.BadRequest("AVAILABILITY_FALLBACK_PLATFORM", "availability fallback group must use the same platform")
	}
	if fallback.SubscriptionType == SubscriptionTypeSubscription {
		return infraerrors.BadRequest("AVAILABILITY_FALLBACK_SUBSCRIPTION", "availability fallback group cannot be a subscription group")
	}
	// Requests never chain fallbacks, but a cycle would still be confusing to
	// operate: reject any chain that leads back to the current group.
	seen := map[int64]bool{fallbackGroupID: true}
	next := fallback.AvailabilityFallbackGroupID
	for i := 0; next != nil && i < 32; i++ {
		if currentGroupID > 0 && *next == currentGroupID {
			return infraerrors.BadRequest("AVAILABILITY_FALLBACK_CYCLE", "availability fallback groups must not form a cycle")
		}
		if seen[*next] {
			break
		}
		seen[*next] = true
		g, err := s.groupRepo.GetByIDLite(ctx, *next)
		if err != nil || g == nil {
			break
		}
		next = g.AvailabilityFallbackGroupID
	}
	return nil
}

// Runtime availability fallback (design §5).

const fallbackGroupCacheTTL = 10 * time.Second

type fallbackGroupCache struct {
	mu      sync.Mutex
	entries map[int64]fallbackGroupEntry
}

type fallbackGroupEntry struct {
	group   *Group
	expires time.Time
}

// ConfigureFallbackGroups attaches the repositories the availability
// fallback needs.
func (r *ResilienceService) ConfigureFallbackGroups(accounts AccountRepository, groups GroupRepository) {
	if r == nil {
		return
	}
	r.fallbackAccounts = accounts
	r.fallbackGroups = groups
}

// AvailabilityFallbackGroup returns the group that must serve a request of
// apiKey instead of its own group, or nil. Every request evaluates its own
// group first:
//   - no account of the group is free of penalties: the fallback group serves
//     (when it has such an account) and the key is remembered;
//   - the group has been healthy for return_stable_seconds: the key returns;
//   - the group recovered only recently: remembered keys stay in the
//     fallback group, so a flapping group does not move them back and forth.
func (r *ResilienceService) AvailabilityFallbackGroup(ctx context.Context, apiKey *APIKey) *Group {
	cfg := resilience.Current()
	if r == nil || r.store == nil || r.fallbackAccounts == nil || r.fallbackGroups == nil || !cfg.FallbackGroupOn() ||
		apiKey == nil || apiKey.Group == nil || apiKey.GroupID == nil || apiKey.Group.AvailabilityFallbackGroupID == nil ||
		apiKey.Group.SubscriptionType == SubscriptionTypeSubscription {
		return nil
	}
	primary := apiKey.Group
	now := time.Now()
	remembered, _ := r.store.FallbackKey(ctx, primary.ID, apiKey.ID)
	if r.groupHasHealthyAccount(ctx, primary.ID, now) {
		since, err := r.store.MarkGroupStable(ctx, primary.ID, now)
		if !remembered {
			return nil
		}
		if err == nil && now.Sub(since) >= time.Duration(cfg.FallbackGroup.ReturnStableSeconds)*time.Second {
			_ = r.store.ClearFallbackKey(ctx, primary.ID, apiKey.ID)
			resilience.Count(resilience.MetricFallbackReturn, primary.ID, 0, "")
			slog.Warn("resilience.fallback_return", "group_id", primary.ID, "api_key_id", apiKey.ID, "reason", "fallback_return")
			return nil
		}
	} else {
		_ = r.store.ClearGroupStable(ctx, primary.ID)
	}
	fallback := r.loadFallbackGroup(ctx, *primary.AvailabilityFallbackGroupID, now)
	if fallback == nil || fallback.Status != StatusActive || fallback.Platform != primary.Platform ||
		fallback.SubscriptionType == SubscriptionTypeSubscription || !r.groupHasHealthyAccount(ctx, fallback.ID, now) {
		return nil
	}
	ttl := time.Duration(cfg.FallbackGroup.BindingTTLSeconds) * time.Second
	if err := r.store.MarkFallbackKey(ctx, primary.ID, apiKey.ID, ttl); err != nil {
		slog.Warn("resilience.fallback_mark_failed", "group_id", primary.ID, "error", err)
	}
	resilience.Count(resilience.MetricFallbackGroup, primary.ID, 0, strconv.FormatInt(fallback.ID, 10))
	if !remembered {
		slog.Warn("resilience.fallback_group", "group_id", primary.ID, "fallback_group_id", fallback.ID, "api_key_id", apiKey.ID)
	}
	return fallback
}

// groupHasHealthyAccount reports whether a group has an active, schedulable
// account without any hard or soft penalty.
func (r *ResilienceService) groupHasHealthyAccount(ctx context.Context, groupID int64, now time.Time) bool {
	accounts, err := r.groupAccountsForLastResort(ctx, r.fallbackAccounts, groupID)
	if err != nil {
		// Unknown: keep the request in its own group.
		return true
	}
	for i := range accounts {
		a := &accounts[i]
		if !a.IsActive() || !a.Schedulable {
			continue
		}
		if tier, _, _ := resiliencePenaltyOf(a, "", now); tier == resilience.TierNone {
			return true
		}
	}
	return false
}

func (r *ResilienceService) loadFallbackGroup(ctx context.Context, id int64, now time.Time) *Group {
	r.fallbackGroupCache.mu.Lock()
	if e, ok := r.fallbackGroupCache.entries[id]; ok && now.Before(e.expires) {
		r.fallbackGroupCache.mu.Unlock()
		return e.group
	}
	r.fallbackGroupCache.mu.Unlock()
	g, err := r.fallbackGroups.GetByIDLite(ctx, id)
	if err != nil {
		g = nil
	}
	r.fallbackGroupCache.mu.Lock()
	if r.fallbackGroupCache.entries == nil || len(r.fallbackGroupCache.entries) > 256 {
		r.fallbackGroupCache.entries = map[int64]fallbackGroupEntry{}
	}
	r.fallbackGroupCache.entries[id] = fallbackGroupEntry{group: g, expires: now.Add(fallbackGroupCacheTTL)}
	r.fallbackGroupCache.mu.Unlock()
	return g
}

// AvailabilityFallbackOf returns the usable availability fallback group
// configured for group, regardless of current account health, so user pages
// can show the relationship and its rate (design §9.2). It returns nil when
// the feature is off or the fallback group is unusable.
func (r *ResilienceService) AvailabilityFallbackOf(ctx context.Context, group *Group) *Group {
	if r == nil || r.fallbackGroups == nil || !resilience.Current().FallbackGroupOn() ||
		group == nil || group.AvailabilityFallbackGroupID == nil || group.SubscriptionType == SubscriptionTypeSubscription {
		return nil
	}
	fallback := r.loadFallbackGroup(ctx, *group.AvailabilityFallbackGroupID, time.Now())
	if fallback == nil || fallback.Status != StatusActive || fallback.Platform != group.Platform ||
		fallback.SubscriptionType == SubscriptionTypeSubscription {
		return nil
	}
	return fallback
}

// CloneAPIKeyWithGroup returns a copy of apiKey bound to group.
func CloneAPIKeyWithGroup(apiKey *APIKey, group *Group) *APIKey {
	if apiKey == nil || group == nil {
		return apiKey
	}
	cloned := *apiKey
	groupID := group.ID
	cloned.GroupID = &groupID
	cloned.Group = group
	return &cloned
}

// resilienceInstance is the process's resilience service, for middlewares
// that are built without a reference to it.
var resilienceInstance atomic.Pointer[ResilienceService]

// Resilience returns the process's resilience service (nil before wiring).
func Resilience() *ResilienceService { return resilienceInstance.Load() }

// SetResilienceInstance publishes the process's resilience service.
func SetResilienceInstance(s *ResilienceService) { resilienceInstance.Store(s) }

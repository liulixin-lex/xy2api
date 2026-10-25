package service

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Observation-only scalars on cacheTrace, never on scheduler/resilience state.
// failoverN belongs to the logical request; the other two markers belong to one
// scheduler invocation and originate at the actual retained-owner/LR decisions.
type cacheTraceRoutingState struct {
	failoverN             int
	softOwner, lastResort int64
}

func beginCacheTraceSelection(ctx context.Context) {
	t := cacheTraceFrom(ctx)
	if t == nil {
		return
	}
	t.mu.Lock()
	// Candidate markers belong to this selection; confirmed route priority
	// belongs to the request and must survive later attempts.
	t.routing.softOwner, t.routing.lastResort = 0, 0
	t.mu.Unlock()
}

func noteCacheTraceSoftOwner(ctx context.Context, accountID int64) {
	if t := cacheTraceFrom(ctx); t != nil {
		t.mu.Lock()
		t.routing.softOwner = accountID
		t.mu.Unlock()
	}
}

func noteCacheTraceLastResort(ctx context.Context, accountID int64) {
	if t := cacheTraceFrom(ctx); t != nil {
		t.mu.Lock()
		t.routing.lastResort = accountID
		t.mu.Unlock()
	}
}

func noteCacheTraceFallback(ctx context.Context) {
	t := cacheTraceFrom(ctx)
	if t == nil {
		return
	}
	// Read only the already successful middleware decision. The origin ID is
	// not retained, logged, copied into a DTO, or used to select an account.
	if resilience.StateFrom(ctx).OriginGroupID() > 0 {
		t.mu.Lock()
		t.promoteCacheTraceRouteLocked("fallback_group")
		t.mu.Unlock()
	}
}

// This hook runs only after the existing scheduler/reconciliation has returned
// its successful final account. It observes existing decision flags and markers
// written at actual policy outcomes; no account/sticky/slot/cache-rate lookup.
func noteCacheTraceSelection(ctx context.Context, selection *AccountSelectionResult, decision OpenAIAccountScheduleDecision) {
	t := cacheTraceFrom(ctx)
	if t == nil || selection == nil || selection.Account == nil {
		return
	}
	fallback := resilience.StateFrom(ctx).OriginGroupID() > 0
	t.mu.Lock()
	defer t.mu.Unlock()
	// An unclassified share/quality layer is not evidence of a new sticky
	// session. Existing exact flags below can still explain those selections.
	if decision.Layer == openAIAccountScheduleLayerLoadBalance {
		t.promoteCacheTraceRouteLocked("sticky_new")
	}
	if decision.StickySessionHit {
		t.promoteCacheTraceRouteLocked("sticky_hit")
	}
	if decision.StickyPreviousHit {
		t.promoteCacheTraceRouteLocked("prev_owner")
	}
	if selection.StickyCapacitySpillover {
		t.promoteCacheTraceRouteLocked("overflow")
	}
	if t.routing.softOwner > 0 && t.routing.softOwner != selection.Account.ID {
		t.promoteCacheTraceRouteLocked("soft_divert")
	}
	if t.data.RebindCause != "none" && t.data.RebindCause != "" {
		t.promoteCacheTraceRouteLocked("session_rebind")
	}
	if t.routing.failoverN > 0 {
		t.promoteCacheTraceRouteLocked("failover_n")
	}
	if fallback {
		t.promoteCacheTraceRouteLocked("fallback_group")
	}
	if t.routing.lastResort > 0 && t.routing.lastResort == selection.Account.ID {
		t.promoteCacheTraceRouteLocked("last_resort")
	}
}

// NoteCacheTraceFailover observes the handler's existing approved switch count;
// it neither spends retry budget nor triggers a switch. Same-account retries do
// not call it. Forward's reset deliberately preserves this request-level fact.
func NoteCacheTraceFailover(ctx context.Context, n int) {
	t := cacheTraceFrom(ctx)
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	if n > t.routing.failoverN {
		t.routing.failoverN = n
	}
	t.promoteCacheTraceRouteLocked("failover_n")
	t.mu.Unlock()
}

// Called only when the existing session failure streak actually triggers a
// rebind. Below-threshold failures never set rebind_cause. Cause remains an
// independent field even if fallback/failover/LR has higher route priority.
func noteCacheTraceRebind(ctx context.Context, category resilience.Category) {
	t := cacheTraceFrom(ctx)
	if t == nil {
		return
	}
	cause := "other"
	switch category {
	case resilience.CategoryStall:
		cause = "stall"
	case resilience.CategoryFirstOutputTimeout:
		cause = "first_event_timeout"
	case resilience.CategoryTransient:
		cause = "pre_output_error"
		if resilience.StateFrom(ctx).FirstOutputNoted() {
			cause = "post_output"
		}
	case resilience.CategoryAuth, resilience.CategoryBalance, resilience.CategoryRateLimit, resilience.CategoryRateLimitReset:
		cause = "pre_output_error"
	}
	t.mu.Lock()
	t.data.RebindCause = cause
	t.promoteCacheTraceRouteLocked("session_rebind")
	t.mu.Unlock()
}

// PM-approved priority. Requires t.mu; all inputs are fixed internal enums.
func (t *cacheTrace) promoteCacheTraceRouteLocked(reason string) {
	if cacheTraceRoutePriority(reason) > cacheTraceRoutePriority(t.data.RouteReason) {
		t.data.RouteReason = reason
	}
}

func cacheTraceRoutePriority(reason string) int {
	switch reason {
	case "sticky_new":
		return 1
	case "sticky_hit":
		return 2
	case "prev_owner":
		return 3
	case "overflow":
		return 4
	case "soft_divert":
		return 5
	case "non_fault_rebind":
		return 55
	case "session_rebind":
		return 6
	case "failover_n":
		return 7
	case "fallback_group":
		return 8
	case "last_resort":
		return 9
	default:
		return 0
	}
}

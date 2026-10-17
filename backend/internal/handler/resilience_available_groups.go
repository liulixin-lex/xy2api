package handler

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/service"
)

// userFallbackGroup is the user-visible summary of an availability fallback
// group (design §9.2): requests are billed at its rate while it serves them.
type userFallbackGroup struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

// availabilityFallbacksOf resolves the configured availability fallback group
// of each group the user can use.
func availabilityFallbacksOf(ctx context.Context, groups []service.Group) map[int64]*userFallbackGroup {
	r := service.Resilience()
	out := map[int64]*userFallbackGroup{}
	for i := range groups {
		if fb := r.AvailabilityFallbackOf(ctx, &groups[i]); fb != nil {
			out[groups[i].ID] = &userFallbackGroup{ID: fb.ID, Name: fb.Name, RateMultiplier: fb.RateMultiplier}
		}
	}
	return out
}

func attachAvailabilityFallbacks(groups []userAvailableGroup, fallbacks map[int64]*userFallbackGroup) {
	for i := range groups {
		groups[i].AvailabilityFallback = fallbacks[groups[i].ID]
	}
}

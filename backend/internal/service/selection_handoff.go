package service

import (
	"context"
	"sync"
)

// Inner selection helpers may return successfully before sticky planning,
// hydration or logging runs. Keep cleanup reachable until the public return.
type selectionHandoff struct{ cleanup []func() }
type selectionHandoffKey struct{}

func beginSelectionHandoff(ctx context.Context) (context.Context, *selectionHandoff) {
	if ctx.Value(selectionHandoffKey{}) != nil {
		return ctx, nil
	}
	h := &selectionHandoff{}
	return context.WithValue(ctx, selectionHandoffKey{}, h), h
}

func guardAcquiredSelectionSlot(ctx context.Context, slot *AcquireResult) {
	h, _ := ctx.Value(selectionHandoffKey{}).(*selectionHandoff)
	if h == nil || slot == nil || !slot.Acquired || slot.ReleaseFunc == nil {
		return
	}
	slot.ReleaseFunc = sync.OnceFunc(slot.ReleaseFunc)
	h.cleanup = append(h.cleanup, slot.ReleaseFunc)
}

func guardSelectionReservation(ctx context.Context, reservation *selectionTrialReservation) {
	if h, _ := ctx.Value(selectionHandoffKey{}).(*selectionHandoff); h != nil {
		h.cleanup = append(h.cleanup, func() { reservation.release(ctx) })
	}
}

// Defer first, so every selection finalizer has run before ownership transfers.
// On a panic (even with a named result assigned), no receiver owns the resources.
func (h *selectionHandoff) finish(result **AccountSelectionResult) {
	if h == nil {
		return
	}
	failure := recover()
	cleanup := h.cleanup
	h.cleanup = nil
	if failure != nil || *result == nil {
		for _, release := range cleanup {
			defer release()
		}
	}
	if failure != nil {
		panic(failure)
	}
}

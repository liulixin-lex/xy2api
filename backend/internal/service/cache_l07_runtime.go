package service

import (
	"sync/atomic"
	"time"
)

// l07Runtime is the process-local state of L-07 (DESIGN §6.1A.2 A3–A5).
// It is created once with the policy service and is only touched on the
// busy path of an armed request.
type l07Runtime struct {
	guardUntil atomic.Int64 // unix nanoseconds; zero when the guard is not tripped
}

func newL07Runtime() *l07Runtime { return &l07Runtime{} }

func (r *l07Runtime) guardTripped(now time.Time) bool {
	if r == nil {
		return false
	}
	until := r.guardUntil.Load()
	return until != 0 && now.UnixNano() < until
}

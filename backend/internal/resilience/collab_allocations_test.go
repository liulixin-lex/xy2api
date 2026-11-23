package resilience

import "testing"

// AllocsPerRun includes allocations from background work in this process.
// That noise can only increase the measurement. Keep the exact expectations
// and use the minimum of five measurements to avoid unrelated test goroutines.
func collabMinAllocsPerRun(runs int, f func()) float64 {
	minimum := testing.AllocsPerRun(runs, f)
	for range 4 {
		minimum = min(minimum, testing.AllocsPerRun(runs, f))
	}
	return minimum
}

//go:build !unit && !gt0512

package resilience

import (
	"context"
	"time"
)

// BudgetTraceEnabledForTest is a compile-time switch. Production callers do
// not evaluate observation arguments or install any recording callbacks.
const BudgetTraceEnabledForTest = false

type timeoutPlanTrace struct{}

func captureTimeoutPlanForTest(*Budget, *TimeoutPlan, AttemptProfile, *RequestState) {}
func (b *Budget) traceProfileForTest(AttemptProfile)                                 {}
func (b *Budget) traceDeadlineForTest(string, time.Time, int64)                      {}
func (b *Budget) traceClampForTest(time.Time, time.Duration, time.Duration, time.Duration, time.Duration) {
}
func (p TimeoutPlan) ObserveFiredForTest(string)                                          {}
func (p TimeoutPlan) WithTimerSiteForTest(string) TimeoutPlan                             { return p }
func ObserveSlotWaitForTest(context.Context, string, int64, time.Duration, time.Duration) {}

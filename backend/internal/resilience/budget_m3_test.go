//go:build unit

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestM3StateRepairRetryExemptsOnlyAttemptCount(t *testing.T) {
	settings := &Settings{Enabled: true, Budget: BudgetSettings{Enabled: true, MaxAttempts: 1, SameAccountRetryMax: 1, PreOutputBudgetSeconds: 240}}
	now := time.Now()
	budget := NewBudget(context.Background(), settings, KindText, now)
	require.False(t, budget.ConsumeInPlaceRetry(1, false, now), "ordinary repairs respect the final attempt")
	require.True(t, budget.ConsumeM3StateRepairRetry(1, now), "state repair may use the final remaining attempt")
	require.False(t, budget.CanAttempt(now), "the exemption does not grant another upstream attempt")
	for range 3 {
		require.True(t, budget.ConsumeM3StateRepairRetry(1, now), "M3 can continue repairing distinct items past the attempt cap")
	}
	require.False(t, budget.ConsumeInPlaceRetry(1, false, now), "M3 does not raise the ordinary retry cap")
	require.False(t, budget.ConsumeM3StateRepairRetry(1, now.Add(240*time.Second)), "continued repairs stop at the original deadline")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	budget = NewBudget(canceled, settings, KindText, now)
	require.False(t, budget.ConsumeM3StateRepairRetry(1, now), "cancellation remains authoritative")
	budget = NewBudget(context.Background(), settings, KindText, now.Add(-240*time.Second))
	require.False(t, budget.ConsumeM3StateRepairRetry(1, now), "the replay exemption does not extend the deadline")
}

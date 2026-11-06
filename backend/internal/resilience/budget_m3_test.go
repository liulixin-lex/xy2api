//go:build unit

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncryptedRepairRetryExemptsOnlyAttemptCount(t *testing.T) {
	settings := &Settings{Enabled: true, Budget: BudgetSettings{Enabled: true, MaxAttempts: 1, SameAccountRetryMax: 1, PreOutputBudgetSeconds: 240}}
	now := time.Now()
	budget := NewBudget(context.Background(), settings, KindText, now)
	require.False(t, budget.ConsumeInPlaceRetry(1, false, now), "ordinary repairs respect the final attempt")
	require.True(t, budget.ConsumeInPlaceRetry(1, false, now, ReasonEncryptedContent), "state repair may use the final remaining attempt")
	require.False(t, budget.CanAttempt(now), "the exemption does not grant another upstream attempt")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	budget = NewBudget(canceled, settings, KindText, now)
	require.False(t, budget.ConsumeInPlaceRetry(1, false, now, ReasonEncryptedContent), "cancellation remains authoritative")
	budget = NewBudget(context.Background(), settings, KindText, now.Add(-240*time.Second))
	require.False(t, budget.ConsumeInPlaceRetry(1, false, now, ReasonEncryptedContent), "the replay exemption does not extend the deadline")
}

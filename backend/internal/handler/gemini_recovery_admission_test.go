//go:build unit

package handler

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGeminiRecoveryAdmissionDoesNotRecordUpstreamAttempt(t *testing.T) {
	withHandlerResilience(t, nil)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	budget := resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now())
	st.SetBudget(budget)
	e := &service.UpstreamFailoverError{StatusCode: 429, Reason: service.GeminiRecoveryPendingReason, RequestScopedTransient: true}
	require.False(t, resilienceAttemptFailed(ctx, 123, e))
	require.NotNil(t, budget)
	require.Zero(t, budget.Attempts())
	require.Equal(t, resilience.CategoryTransient, service.ClassifyFailoverError(e).Category)
}

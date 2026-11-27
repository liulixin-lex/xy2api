//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCompensationReviewMarginSnapshot(t *testing.T) {
	c, _, a := compensationFixture()
	c.Usage.GroupID = new(int64(7))
	c.Usage.BillingMode = new(string(BillingModeToken))
	requestGroup := &Group{ID: 7, Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI, RateMultiplier: 1, ProfitControlEnabled: true, ProfitMinMargin: .5, ProfitSafetyBuffer: .1}
	storedGroup := *requestGroup
	storedGroup.ProfitControlEnabled = false
	store := &compensationMemoryStore{action: a}
	svc := &OpenAIGatewayService{resilience: &ResilienceService{store: store}, schedulerSnapshot: &SchedulerSnapshotService{groupRepo: profitControlGroupRepo{group: &storedGroup}}}
	st := resilience.NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.SetAPIKey(1)
	st.NoteTurn(7, "fixture", "session")
	requestCtx := resilience.WithRequestState(context.Background(), st)
	requestCtx = context.WithValue(requestCtx, ctxkey.Group, requestGroup)
	requestCtx = svc.withOpenAIProfitControlGate(requestCtx, c.Usage.GroupID)
	selected := attachSelectionProfitGate(requestCtx, &AccountSelectionResult{Account: &Account{ID: 2}})
	require.NotNil(t, selected.profitGate)
	require.InDelta(t, .4, selected.profitGate.threshold, 1e-12)
	requestCtx = ContextWithSelectionProfitGate(requestCtx, selected)
	st.NoteGatewaySend(2)
	st.CompleteGatewayForward(true, false)
	meta, _ := resilience.StateFrom(requestCtx).ClaimGatewayRequest(true)
	// Production durable accounting starts with Background and copies metadata
	// and request state; it does not propagate the authentication group.
	billingCtx := resilience.WithRequestState(context.Background(), st)
	billingCtx = resilience.WithGatewayRequestMetadata(billingCtx, meta)
	require.Nil(t, billingCtx.Value(ctxkey.Group))
	resolve, _ := svc.compensationResolver(billingCtx, c.Usage, func(float64) (float64, error) { return 8, nil })
	require.NotNil(t, resolve)
	resolve(c)
	require.Equal(t, 10.0, c.Usage.ActualCost, "R=10 C=6 was already below the admission margin .5; a later disabled group cannot grant credit")
	require.Equal(t, 10.0, c.BalanceCost)
	t.Logf("admission margin=.5; stored group disabled; detached settlement=%g", c.Usage.ActualCost)
}

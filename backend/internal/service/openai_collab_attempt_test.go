//go:build unit

package service

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabAttemptIsolatesMutableLegacyState(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	lineage := &openAIM3Request{accounts: map[int64]*openAIM3Account{7: {replaced: map[openAIM3SlotID]struct{}{}}}}
	quota := &openAIFCOReplayQuota{key: "synthetic"}
	fields := &openAIResponsesRejectedFieldRetryBudget{attempts: 2}
	observer := beginUpstreamResponseModelObservation(c)
	c.Set(openAIM3ContextKey, lineage)
	c.Set(openAIFCOReplayKey, quota)
	c.Set(openAIResponsesRejectedFieldRetryBudgetContextKey, fields)
	event := &OpsUpstreamErrorEvent{AccountID: 7}
	c.Set(OpsUpstreamErrorsKey, []*OpsUpstreamErrorEvent{event})
	attempt := c.Copy()
	IsolateCollabAttempt(attempt)
	for _, key := range []string{openAIM3ContextKey, openAIFCOReplayKey, openAIResponsesRejectedFieldRetryBudgetContextKey, upstreamResponseModelObserverContextKey} {
		_, exists := attempt.Get(key)
		require.False(t, exists, key)
	}
	currentOpsUpstreamEvents(attempt)[0].Resent = true
	require.False(t, event.Resent)
	require.Same(t, observer, upstreamResponseModelObserverFromContext(c))
	value, _ := c.Get(openAIM3ContextKey)
	require.Same(t, lineage, value)
	require.Equal(t, 2, fields.attempts)
	flag := &atomic.Bool{}
	parent := context.WithValue(context.Background(), openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: "synthetic-thread", selectionPreservesBinding: flag})
	child := WithCollabAttemptIsolation(parent)
	openAIRejectionScope(child).selectionPreservesBinding.Store(true)
	require.False(t, flag.Load())
	require.Equal(t, openAIRejectionScope(parent).session, openAIRejectionScope(child).session)
}

func TestCollabCommittedResponseIdentityMatchesBinding(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	state := resilience.NewRequestState()
	state.InitDelivery(context.Background(), time.Now(), func() int64 { return 10 })
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), state))
	store := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: store}
	result := &OpenAIForwardResult{ResponseID: "resp_upstream"}
	svc.CommitCollabResponseIdentity(c, &Account{ID: 7}, result, "resp_delivered")
	require.Equal(t, "resp_delivered", result.ResponseID)
	id, err := store.GetResponseAccount(context.Background(), 0, "resp_delivered")
	require.NoError(t, err)
	require.Equal(t, int64(7), id)
	id, err = store.GetResponseAccount(context.Background(), 0, "resp_upstream")
	require.NoError(t, err)
	require.Zero(t, id)
}

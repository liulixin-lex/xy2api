//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type collabReadinessAliases struct {
	slowReady     bool
	readyBlock    chan struct{}
	readyCalls    atomic.Int32
	unblock       chan struct{}
	reads, writes atomic.Int32
}

func (s *collabReadinessAliases) Ready() bool {
	s.readyCalls.Add(1)
	if s.readyBlock != nil {
		<-s.readyBlock
	}
	if s.slowReady {
		time.Sleep(70 * time.Second)
	}
	return true
}
func (s *collabReadinessAliases) Get(context.Context, CollabContinuationScope, string) (CollabContinuationAlias, bool, error) {
	s.reads.Add(1)
	if s.unblock != nil {
		<-s.unblock
	}
	return CollabContinuationAlias{}, false, errors.New("synthetic store failure")
}
func (s *collabReadinessAliases) Put(context.Context, CollabContinuationScope, string, CollabContinuationAlias) error {
	s.writes.Add(1)
	if s.unblock != nil {
		<-s.unblock
	}
	return errors.New("synthetic store failure")
}
func collabDeliveredContext() *gin.Context {
	ctx := context.Background()
	st := resilience.NewRequestState()
	st.InitDelivery(ctx, time.Now(), func() int64 { return 10 })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(ctx, st))
	return c
}
func TestCollabRegressionAliasReadinessBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
		svc.SetCollabContinuationStore(&collabReadinessAliases{slowReady: true})
		start := time.Now()
		defer time.Sleep(70 * time.Second) // Join the deliberately uncooperative Ready call.
		ready := svc.CanContinueCollabResponse(context.Background())
		t.Logf("takeover_readiness=%t elapsed=%s", ready, time.Since(start))
		require.LessOrEqual(t, time.Since(start), 250*time.Millisecond, "alias readiness must not bypass takeover bounds")
	})
}
func TestCollabRegressionAliasReadinessSettlementBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
		svc.SetCollabContinuationStore(&collabReadinessAliases{slowReady: true})
		c := collabDeliveredContext()
		start := time.Now()
		persist := svc.CommitCollabResponseIdentity(c, &Account{ID: 7}, &OpenAIForwardResult{ResponseID: "resp_upstream"}, "resp_delivered")
		t.Logf("settlement_blocked=%s callback_present=%t", time.Since(start), persist != nil)
		require.Zero(t, time.Since(start), "alias readiness must not hold settlement")
	})
}
func TestCollabRegressionAliasUncooperativeStoreCaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
		store := &collabReadinessAliases{unblock: make(chan struct{})}
		svc.SetCollabContinuationStore(store)
		defer close(store.unblock)
		for i := 0; i < 8; i++ {
			start := time.Now()
			_, _, _ = svc.readCollabAlias(context.Background(), store, CollabContinuationScope{}, "resp_unknown")
			require.LessOrEqual(t, time.Since(start), 250*time.Millisecond)
		}
		require.EqualValues(t, 4, store.reads.Load())
		c := collabDeliveredContext()
		for i := 0; i < 8; i++ {
			start := time.Now()
			persist := svc.CommitCollabResponseIdentity(c, &Account{ID: 7}, &OpenAIForwardResult{ResponseID: "resp_upstream"}, "resp_delivered")
			require.NotNil(t, persist)
			persist()
			persist()
			require.Zero(t, time.Since(start))
			synctest.Wait()
		}
		require.EqualValues(t, 4, store.writes.Load())
		t.Logf("blocked_store_workers reads=%d writes=%d failures=%d", store.reads.Load(), store.writes.Load(), svc.CollabContinuationFailures())
	})
}

func TestCollabRegressionAliasReadinessCancellationAndCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		store := &collabReadinessAliases{readyBlock: make(chan struct{})}
		svc.SetCollabContinuationStore(store)
		defer close(store.readyBlock)
		for i := 0; i < 8; i++ {
			start := time.Now()
			require.False(t, svc.CanContinueCollabResponse(context.Background()))
			require.LessOrEqual(t, time.Since(start), 250*time.Millisecond)
		}
		require.EqualValues(t, 4, store.readyCalls.Load())
		require.EqualValues(t, 4, svc.collabAliasReads.Load())
		t.Logf("outstanding_readiness=%d failures=%d", store.readyCalls.Load(), svc.CollabContinuationFailures())
	})
	synctest.Test(t, func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		store := &collabReadinessAliases{readyBlock: make(chan struct{})}
		svc.SetCollabContinuationStore(store)
		defer close(store.readyBlock)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		start := time.Now()
		require.False(t, svc.CanContinueCollabResponse(ctx))
		require.Equal(t, 10*time.Millisecond, time.Since(start))
	})
}

type collabSlowIdentityStore struct {
	OpenAIWSStateStore
	slow  string
	calls []string
}

func (s *collabSlowIdentityStore) wait(ctx context.Context, operation string) error {
	s.calls = append(s.calls, operation)
	if s.slow != operation {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(70 * time.Second):
		return errors.New("synthetic identity storage delay")
	}
}
func (s *collabSlowIdentityStore) BindResponseAccount(ctx context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	return s.wait(ctx, "account")
}
func (s *collabSlowIdentityStore) BindHTTPResponseOwner(ctx context.Context, _ int64, _ string, _, _ int64, _ time.Duration) error {
	return s.wait(ctx, "owner")
}
func TestCollabRegressionIdentityBindingAfterSettlement(t *testing.T) {
	for _, operation := range []string{"account", "owner"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &collabSlowIdentityStore{OpenAIWSStateStore: NewOpenAIWSStateStore(nil), slow: operation}
				svc := &OpenAIGatewayService{openaiWSStateStore: store}
				c := collabDeliveredContext()
				c.Set(openAIHTTPResponseOwnerContextKey, openAIHTTPResponseOwner{userID: 3, apiKeyID: 5})
				start := time.Now()
				persist := svc.CommitCollabResponseIdentity(c, &Account{ID: 7}, &OpenAIForwardResult{ResponseID: "resp_delivered"}, "resp_delivered")
				t.Logf("operation=%s settlement_blocked=%s calls=%v", operation, time.Since(start), store.calls)
				require.Zero(t, time.Since(start), "shared identity storage cannot delay settlement")
				require.Empty(t, store.calls)
				require.NotNil(t, persist)
				persist()
				require.Zero(t, time.Since(start))
				synctest.Wait()
				time.Sleep(250 * time.Millisecond)
				synctest.Wait()
				require.Contains(t, store.calls, operation)
				require.Equal(t, uint64(1), svc.CollabContinuationFailures())
				require.Zero(t, svc.collabAliasWrites.Load())
				t.Logf("operation=%s persistence_elapsed=%s failures=%d", operation, time.Since(start), svc.CollabContinuationFailures())
			})
		})
	}
}

func TestCollabRegressionSharedIdentityVisibleBeforePersistence(t *testing.T) {
	cache := &responseBindContextProbeCache{}
	svc := &OpenAIGatewayService{cache: cache}
	c := collabDeliveredContext()
	c.Set(openAIHTTPResponseOwnerContextKey, openAIHTTPResponseOwner{userID: 3, apiKeyID: 5})
	persist := svc.CommitCollabResponseIdentity(c, &Account{ID: 7}, &OpenAIForwardResult{ResponseID: "resp_delivered"}, "resp_delivered")
	require.NotNil(t, persist)
	require.Empty(t, cache.setContextErrors)
	account, err := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 0, "resp_delivered")
	require.NoError(t, err)
	require.EqualValues(t, 7, account)
	user, key, found, err := svc.getOpenAIWSStateStore().GetHTTPResponseOwner(context.Background(), 0, "resp_delivered")
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 3, user)
	require.EqualValues(t, 5, key)
}

func (s *collabSlowIdentityStore) PrepareCollabResponseAccount(group int64, id string, account int64, ttl time.Duration) func(context.Context) error {
	return func(ctx context.Context) error { return s.BindResponseAccount(ctx, group, id, account, ttl) }
}
func (s *collabSlowIdentityStore) PrepareCollabHTTPResponseOwner(group int64, id string, user, key int64, ttl time.Duration) func(context.Context) error {
	return func(ctx context.Context) error { return s.BindHTTPResponseOwner(ctx, group, id, user, key, ttl) }
}

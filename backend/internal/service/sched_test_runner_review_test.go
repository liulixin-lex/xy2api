//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type schedTestAccountFunc func(context.Context, int64) (*Account, error)

func (f schedTestAccountFunc) GetByID(ctx context.Context, id int64) (*Account, error) {
	return f(ctx, id)
}

func TestSchedTestRunnerRefreshesAccount(t *testing.T) {
	for _, limit := range []int{1, 10} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, forward, admission, cache := schedTestFixture(limit)
				defer s.Stop()
				var mu sync.Mutex
				limited := false
				seen := make(map[*Account]bool)
				parallel := min(3, max(1, limit-1))
				entered := make(chan struct{}, parallel)
				release := make(chan struct{})
				s.accounts = schedTestAccountFunc(func(context.Context, int64) (*Account, error) {
					mu.Lock()
					defer mu.Unlock()
					account := &Account{ID: 7, Concurrency: limit, Extra: map[string]any{}}
					if limited {
						reset := time.Now().Add(time.Minute)
						account.RateLimitResetAt = &reset
					}
					return account, nil
				})
				admission.Fn = func(_ context.Context, a *Account, model string) (string, bool, string) {
					return model, a.RateLimitResetAt == nil, "rate_limit"
				}
				forward.Fn = func(ctx context.Context, _ *gin.Context, a *Account, _ []byte) (*OpenAIForwardResult, error) {
					mu.Lock()
					if seen[a] {
						t.Error("forward reused an account snapshot")
					}
					seen[a] = true
					mu.Unlock()
					// Forward is permitted to mutate its request's snapshot.
					a.Extra["forward"] = true
					entered <- struct{}{}
					<-release
					mu.Lock()
					limited = true
					mu.Unlock()
					return schedTestOK(ctx, 1), nil
				}
				ticket, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				require.True(t, ticket.Started)
				for range parallel {
					<-entered
				}
				close(release)
				r := <-store.finished
				require.True(t, r.Done)
				require.Len(t, r.Turns, parallel)
				for _, row := range r.Turns {
					require.Equal(t, 1, row.Turn)
				}
				require.Len(t, seen, parallel)
				require.Zero(t, cache.active)
			})
		})
	}
}

func TestSchedTestRunnerAccountReadFailure(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, forward, _, cache := schedTestFixture(1)
				defer s.Stop()
				reads := 0
				s.accounts = schedTestAccountFunc(func(context.Context, int64) (*Account, error) {
					reads++
					// Request + pre-slot + post-slot refresh; fail after acquiring a slot.
					if reads == 3 || all && reads > 1 {
						return nil, errors.New("account unavailable")
					}
					return &Account{ID: 7, Concurrency: 1}, nil
				})
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
					return schedTestOK(ctx, 1), nil
				}
				_, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				r := <-store.finished
				if all {
					require.False(t, r.Done)
					require.Equal(t, "error", r.AbortReason)
					require.Empty(t, r.Turns)
				} else {
					require.True(t, r.Done)
					require.Len(t, r.Turns, 6)
				}
				require.Zero(t, cache.active)
			})
		})
	}
}

func TestSchedTestRunnerModelsShareTierLimit(t *testing.T) {
	for _, tc := range []struct{ limit, parallel int }{{1, 1}, {2, 1}, {3, 2}, {10, 3}, {0, 3}} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, forward, _, cache := schedTestFixture(tc.limit)
				defer s.Stop()
				entered := make(chan struct{}, 18)
				release := make(chan struct{})
				var mu sync.Mutex
				active, peak := 0, 0
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
					mu.Lock()
					active++
					peak = max(peak, active)
					mu.Unlock()
					entered <- struct{}{}
					<-release
					mu.Lock()
					active--
					mu.Unlock()
					return schedTestOK(ctx, 1), nil
				}
				for _, model := range []string{"first", "second"} {
					ticket, err := s.Request(context.Background(), 7, model, false, 0)
					require.NoError(t, err)
					require.True(t, ticket.Started)
				}
				synctest.Wait()
				require.Len(t, entered, tc.parallel)
				close(release)
				first, second := <-store.finished, <-store.finished
				require.NotEqual(t, first.Model, second.Model)
				require.Len(t, first.Turns, 9)
				require.Len(t, second.Turns, 9)
				s.wg.Wait()
				require.Equal(t, tc.parallel, peak)
				require.Zero(t, active)
				require.Zero(t, cache.active)
				require.Empty(t, s.tierLimits)
			})
		})
	}
}

func TestSchedTestRunnerQueuedModelExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, forward, _, cache := schedTestFixture(1)
		defer s.Stop()
		forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
			select {
			case <-time.After(119 * time.Second):
				return schedTestOK(ctx, 1), nil
			case <-ctx.Done():
				first := 12
				resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeOK, TTFOMs: &first})
				return nil, ctx.Err()
			}
		}
		ticket, err := s.Request(context.Background(), 7, "first", false, 0)
		require.NoError(t, err)
		require.True(t, ticket.Started)
		synctest.Wait()
		ticket, err = s.Request(context.Background(), 7, "queued", false, 0)
		require.NoError(t, err)
		require.True(t, ticket.Started)
		a, b := <-store.finished, <-store.finished
		if a.Model == "queued" {
			a, b = b, a
		}
		require.True(t, a.Done)
		require.Len(t, a.Turns, 3)
		require.False(t, b.Done)
		require.Empty(t, b.Turns)
		require.Equal(t, "timeout", b.AbortReason)
		require.Equal(t, schedquality.TestTimeout, b.FinishedAt.Sub(b.StartedAt))
		require.Zero(t, cache.active)
	})
}

func TestSchedTestRunnerSiblingCancellation(t *testing.T) {
	for _, firstOutput := range []bool{false, true} {
		t.Run(fmt.Sprint(firstOutput), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, forward, admission, cache := schedTestFixture(10)
				defer s.Stop()
				ready := make(chan struct{}, 2)
				var mu sync.Mutex
				limited := false
				s.accounts = schedTestAccountFunc(func(context.Context, int64) (*Account, error) {
					mu.Lock()
					defer mu.Unlock()
					a := &Account{ID: 7, Concurrency: 10}
					if limited {
						reset := time.Now().Add(time.Minute)
						a.RateLimitResetAt = &reset
					}
					return a, nil
				})
				admission.Fn = func(_ context.Context, a *Account, model string) (string, bool, string) {
					return model, a.RateLimitResetAt == nil, "rate_limit"
				}
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, body []byte) (*OpenAIForwardResult, error) {
					if schedTestTier(body) == schedquality.TierSmall {
						<-ready
						<-ready
						mu.Lock()
						limited = true
						mu.Unlock()
						return schedTestOK(ctx, 1), nil
					}
					ready <- struct{}{}
					<-ctx.Done()
					sample := schedquality.AttemptSample{Outcome: schedquality.OutcomeTimeout}
					if firstOutput {
						first := 12
						sample.Outcome = schedquality.OutcomeOK
						sample.TTFOMs = &first
					}
					resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(sample)
					return nil, ctx.Err()
				}
				_, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				r := <-store.finished
				require.True(t, r.Done)
				require.Len(t, r.Turns, 3)
				for _, row := range r.Turns {
					if row.Tier == schedquality.TierSmall {
						require.Equal(t, schedquality.OutcomeOK, row.Outcome)
					} else {
						require.Equal(t, schedquality.OutcomeOther, row.Outcome)
						require.Equal(t, "cancelled", row.Detail)
					}
				}
				require.Zero(t, cache.active)
			})
		})
	}
}

func TestSchedTestRunnerSlotRetryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, _, _, _ := schedTestFixture(10)
		defer s.Stop()
		var mu sync.Mutex
		attempts := map[schedquality.TestTier][]time.Time{}
		s.acquire = func(ctx context.Context, _ *Account, tier schedquality.TestTier) (*AcquireResult, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts[tier] = append(attempts[tier], time.Now())
			return &AcquireResult{}, nil
		}
		_, err := s.Request(context.Background(), 7, "evidence", false, 0)
		require.NoError(t, err)
		r := <-store.finished
		require.Equal(t, "busy", r.AbortReason)
		require.Equal(t, schedquality.TestSlotWait, r.FinishedAt.Sub(r.StartedAt))
		for tier := schedquality.TierSmall; tier <= schedquality.TierLarge; tier++ {
			times := attempts[tier]
			// At t=10s the retry and deadline timers can become ready together.
			require.GreaterOrEqual(t, len(times), 5)
			require.LessOrEqual(t, len(times), 6)
			for i, at := range times {
				require.Equal(t, r.StartedAt.Add(time.Duration(i)*schedquality.TestSlotRetryInterval), at)
			}
		}
	})
}

func TestSchedTestRunnerPanicReleasesResources(t *testing.T) {
	for _, afterFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(afterFirst), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, forward, _, cache := schedTestFixture(1)
				defer s.Stop()
				calls := map[schedquality.TestTier]int{}
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, body []byte) (*OpenAIForwardResult, error) {
					tier := schedTestTier(body)
					calls[tier]++
					if afterFirst && calls[tier] == 1 {
						return schedTestOK(ctx, 1), nil
					}
					panic("synthetic forwarding panic")
				}
				_, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				r := <-store.finished
				s.wg.Wait()
				require.Zero(t, cache.active)
				require.Empty(t, s.tierLimits)
				require.Equal(t, 1, store.finishCalls)
				if afterFirst {
					require.True(t, r.Done)
					require.Len(t, r.Turns, 3)
				} else {
					require.False(t, r.Done)
					require.Equal(t, "error", r.AbortReason)
					require.Empty(t, r.Turns)
				}
				// A different model can use the same account after recovery.
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
					return schedTestOK(ctx, 1), nil
				}
				ticket, err := s.Request(context.Background(), 7, "next-model", false, 0)
				require.NoError(t, err)
				require.True(t, ticket.Started)
				require.Len(t, (<-store.finished).Turns, 9)
			})
		})
	}
}

func TestSchedTestRunnerUnknownPreviousUsage(t *testing.T) {
	for _, missingTurn := range []int{1, 2} {
		t.Run(fmt.Sprint(missingTurn), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, forward, _, _ := schedTestFixture(1)
				defer s.Stop()
				calls := map[schedquality.TestTier]int{}
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, body []byte) (*OpenAIForwardResult, error) {
					tier := schedTestTier(body)
					calls[tier]++
					n := calls[tier]
					if n == missingTurn {
						first := 12
						resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeOK, TTFOMs: &first})
						return &OpenAIForwardResult{}, nil
					}
					return schedTestOK(ctx, n), nil
				}
				_, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				r := <-store.finished
				require.True(t, r.Done)
				require.Len(t, r.Turns, 9)
				for _, row := range r.Turns {
					if row.Turn == 1 || row.Turn == missingTurn+1 {
						require.Nil(t, row.PrevPromptTokens)
					} else {
						require.Equal(t, (row.Turn-1)*1000, *row.PrevPromptTokens)
					}
				}
			})
		})
	}
}

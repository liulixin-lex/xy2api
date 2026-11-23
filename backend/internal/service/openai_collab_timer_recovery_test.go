//go:build unit

package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type collabTimerPanicContext struct {
	context.Context
	fail atomic.Bool
}

func (c *collabTimerPanicContext) Value(key any) any {
	if c.fail.Load() && fmt.Sprintf("%T", key) == "resilience.stateKey" {
		panic("synthetic timer identity failure")
	}
	return c.Context.Value(key)
}
func TestCollabFirstEventTimerPublication(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint(panics), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent := &collabTimerPanicContext{Context: context.Background()}
				start := time.Now()
				ctx, guard := newFirstEventGuard(parent, time.Second)
				defer guard.Release()
				parent.fail.Store(panics)
				<-ctx.Done()
				require.Equal(t, time.Second, time.Since(start))
				require.Equal(t, !panics, guard.Fired())
				select {
				case <-guard.published:
				default:
					t.Fatal("cancellation must publish the timer result before a receiver can read it")
				}
				parent.fail.Store(false)
				c := newOpenAIRejectedFieldTestContext(nil)
				require.NotNil(t, guard.failoverError(c, &Account{ID: 1}, "pre_output"))
				require.Equal(t, context.Canceled, ctx.Err())
			})
		})
	}
}

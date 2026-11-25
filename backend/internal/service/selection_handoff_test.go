//go:build unit

package service

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func reviewFrom(site string) bool {
	var pcs [50]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	for {
		f, more := frames.Next()
		if strings.HasSuffix(f.Function, site) {
			return true
		}
		if !more {
			return false
		}
	}
}

func TestSelectionPanicDuringReturn(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	f.svc.selectionBoard.view = func() q.View {
		if reviewFrom("(*selectionTrial).finish") {
			panic("return")
		}
		return f.view
	}
	require.PanicsWithValue(t, "return", func() { _, _ = selectOrdinaryNewSessionForTest(t, f, selectionReviewContext(), "turn", nil, true) })
	require.Equal(t, []int64{1}, f.releases)
	require.Empty(t, leases.held)
}

type reviewWSStore struct {
	OpenAIWSStateStore
	bind func()
}

func (s reviewWSStore) BindResponseAccount(context.Context, int64, string, int64, time.Duration) error {
	s.bind()
	return nil
}
func TestSelectionWSTouchHandoff(t *testing.T) {
	for _, mode := range []string{"inside", "outside", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			a := selectionFixtureAccount(1, 1)
			a.Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
			f := newSelectionRuntimeFixture(t, a)
			ctx := selectionReviewContext()
			base := NewOpenAIWSStateStore(f.cache)
			require.NoError(t, base.BindResponseAccount(ctx, 7, "resp_review", 1, time.Hour))
			f.svc.openaiWSStateStore = reviewWSStore{base, func() {
				if mode == "inside" {
					panic("ws")
				}
			}}
			if mode == "outside" {
				f.svc.selectionBoard.leases = newSelectionTrialLeaseFake()
				f.svc.selectionBoard.view = func() q.View {
					if reviewFrom("(*selectionTrial).touch") {
						panic("ws")
					}
					return f.view
				}
			}
			call := func() *AccountSelectionResult {
				r, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "resp_review", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				require.NoError(t, err)
				return r
			}
			if mode == "concurrent" {
				r := call()
				require.True(t, r.Acquired)
				require.Empty(t, f.releases)
				var wg sync.WaitGroup
				for range 64 {
					wg.Go(r.ReleaseFunc)
				}
				wg.Wait()
			} else {
				require.PanicsWithValue(t, "ws", func() { call() })
			}
			t.Logf("acquires=%v releases=%v", f.attempts, f.releases)
			require.Equal(t, []int64{1}, f.releases)
		})
	}
}

type reviewBindPanic struct{ GatewayCache }

func (reviewBindPanic) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	panic("bind-before-handler")
}
func TestSelectionGeminiServiceBindPanic(t *testing.T) {
	a := selectionFixtureAccount(1, 1)
	a.Platform = PlatformGemini
	var acquired, released []int64
	s := &GatewayService{cfg: testConfig(), cache: reviewBindPanic{}, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})}
	require.PanicsWithValue(t, "bind-before-handler", func() {
		_, _, _ = s.tryAcquireByLegacyOrder(selectionReviewContext(), []*Account{&a}, ptrInt64Selection(7), "session", "gemini-2.5-flash", false)
	})
	t.Logf("acquires=%v releases=%v", acquired, released)
	require.Equal(t, []int64{1}, released)
}

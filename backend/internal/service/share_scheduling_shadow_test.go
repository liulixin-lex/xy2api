//go:build unit

package service

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// The shadow tests replay one workload on an original group and on a shadow
// group whose share policy disagrees with it. Every selection, binding write
// and slot request must be identical: the shadow only observes.

// shadowReversedRules reverses the original priority order 1 < 2 < 3 < 4, so
// the shadow plans often pick another account.
var shadowReversedRules = []sharesched.AccountRule{
	{AccountID: 1, Tier: 4, Weight: 1}, {AccountID: 2, Tier: 3, Weight: 1},
	{AccountID: 3, Tier: 2, Weight: 1}, {AccountID: 4, Tier: 1, Weight: 1},
}

type shadowStep struct {
	key      string
	busy     map[int64]int
	excluded map[int64]struct{}
}

// shadowWorkload mixes new and returning sessions, sessionless requests,
// rejected and saturated slots, and failover exclusions over accounts 1-4.
func shadowWorkload(requests int, sessionless bool) []shadowStep {
	rng := rand.New(rand.NewPCG(7, 11))
	steps := make([]shadowStep, 0, requests)
	sessions := 0
	for len(steps) < requests {
		var step shadowStep
		switch r := rng.IntN(10); {
		case r == 0:
			if !sessionless {
				continue
			}
		case r < 4 || sessions == 0:
			sessions++
			step.key = fmt.Sprintf("shadow-%d", sessions)
		default:
			step.key = fmt.Sprintf("shadow-%d", rng.IntN(sessions)+1)
		}
		step.busy = map[int64]int{}
		switch r := rng.IntN(10); {
		case r < 4: // one account rejects its next slot request
			step.busy[int64(r+1)] = 1
		case r < 6: // one account is saturated
			step.busy[int64(rng.IntN(4)+1)] = -1
		case r == 6: // every account is saturated
			step.busy = map[int64]int{1: -1, 2: -1, 3: -1, 4: -1}
		}
		if rng.IntN(8) == 0 {
			step.excluded = map[int64]struct{}{int64(rng.IntN(4) + 1): {}}
		}
		steps = append(steps, step)
	}
	return steps
}

// shadowSide is one group of the comparison.
type shadowSide struct {
	cache   *shareTestCache
	conc    *shareTestConcurrency
	selec   func(step shadowStep) (*AccountSelectionResult, error)
	bind    func(key string, accountID int64) error
	counter func(field string) int64
}

func replayShadow(t *testing.T, steps []shadowStep, original, shadow shadowSide) {
	t.Helper()
	outcomes := map[string]int{}
	for i, step := range steps {
		var got [2]string
		for j, side := range []shadowSide{original, shadow} {
			side.conc.mu.Lock()
			side.conc.busy = maps.Clone(step.busy)
			side.conc.mu.Unlock()
			result, err := side.selec(step)
			got[j] = describeShareSelection(result, err)
			if err == nil && result.Acquired {
				// The handler binds the session once the request is admitted.
				if step.key != "" {
					require.NoError(t, side.bind(step.key, result.Account.ID))
				}
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			}
		}
		require.Equal(t, got[0], got[1], "request %d (session %q)", i, step.key)
		require.Equal(t, shadowSideEffects(original), shadowSideEffects(shadow), "bindings and slot requests after request %d", i)
		outcomes[strings.Fields(got[0])[1]]++
	}
	t.Logf("outcomes %v", outcomes)
	require.Positive(t, outcomes["acquired=false"], "some requests got a wait plan")

	total := int64(len(steps))
	require.Eventually(t, func() bool { return shadow.counter(shareStatsShadowField) == total }, 5*time.Second, 10*time.Millisecond)
	agree := shadow.counter(shareStatsShadowAgreeField)
	t.Logf("shadow agreed on %d of %d", agree, total)
	require.Less(t, agree, total, "the shadow plans disagree with the original")
	require.Zero(t, original.counter(shareStatsShadowField))
}

func describeShareSelection(result *AccountSelectionResult, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	out := fmt.Sprintf("account=%d acquired=%v", result.Account.ID, result.Acquired)
	if result.WaitPlan != nil {
		out += fmt.Sprintf(" wait=%+v", *result.WaitPlan)
	}
	return out
}

func shadowSideEffects(side shadowSide) []any {
	side.cache.mu.Lock()
	defer side.cache.mu.Unlock()
	side.conc.mu.Lock()
	defer side.conc.mu.Unlock()
	return []any{maps.Clone(side.cache.sessionBindings), maps.Clone(side.cache.ttls), slices.Clone(side.cache.refreshed),
		slices.Clone(side.cache.replaced), slices.Clone(side.cache.created), maps.Clone(side.conc.attempts), maps.Clone(side.conc.waiting)}
}

func TestShareSchedulingShadowMatchesOriginalPerRequest(t *testing.T) {
	// Distinct priorities keep the original scheduler free of random ties.
	accounts := []Account{{ID: 1, Priority: 1}, {ID: 2, Priority: 2}, {ID: 3, Priority: 3}, {ID: 4, Priority: 4}}
	gid := shareTestGroupID
	side := func(scheduler sharesched.Scheduler) shadowSide {
		env := newShareTestEnv(t, shareTestPolicy(scheduler, shadowReversedRules...), accounts...)
		return shadowSide{cache: env.cache, conc: env.conc, counter: env.counter,
			selec: func(step shadowStep) (*AccountSelectionResult, error) {
				return env.gateway.SelectAccountWithLoadAwareness(cacheableContext(), &gid, step.key, "claude-sonnet-4-5", step.excluded, "", 0)
			},
			bind: func(key string, accountID int64) error {
				return env.gateway.BindStickySession(context.Background(), &gid, key, accountID)
			}}
	}
	replayShadow(t, shadowWorkload(400, true), side(sharesched.SchedulerSub2API), side(sharesched.SchedulerShadow))
}

func TestOpenAIShareSchedulingShadowMatchesOriginalPerRequest(t *testing.T) {
	gid := shareTestGroupID
	side := func(scheduler sharesched.Scheduler) shadowSide {
		env := newOpenAIShareTestEnv(t, shareTestPolicy(scheduler, shadowReversedRules...), 1, 2, 3, 4)
		for i := range env.repo.accounts {
			env.repo.accounts[i].Priority = int(env.repo.accounts[i].ID)
		}
		return shadowSide{cache: env.cache, conc: env.conc, counter: env.counter,
			selec: func(step shadowStep) (*AccountSelectionResult, error) {
				result, _, err := env.svc.SelectAccountWithScheduler(cacheableContext(), &gid, "", step.key, "gpt-5.1", step.excluded, OpenAIUpstreamTransportAny, false)
				return result, err
			},
			bind: func(key string, accountID int64) error {
				return env.svc.BindStickySession(context.Background(), &gid, key, accountID)
			}}
	}
	// Sessionless OpenAI selections mix in the clock, so only sessions replay.
	replayShadow(t, shadowWorkload(400, false), side(sharesched.SchedulerSub2API), side(sharesched.SchedulerShadow))
}

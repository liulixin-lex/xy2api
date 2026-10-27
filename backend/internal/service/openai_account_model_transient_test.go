package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIModelTransient_FirstFailureDoesNotCreateLongBlock(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)

	decision := state.recordFailure(35, "gpt-5.5", now)

	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
	assert.False(t, state.isBlocked(35, "gpt-5.5", now))
}

func TestOpenAIModelTransient_SecondFailureCreatesShortModelBlock(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.recordFailure(35, "gpt-5.5", now)

	decision := state.recordFailure(35, "gpt-5.5", now.Add(time.Second))

	assert.Equal(t, 2, decision.FailureStreak)
	assert.Equal(t, openAIModelTransientShortCooldown, decision.Cooldown)
	assert.True(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	assert.False(t, state.isBlocked(35, "gpt-5.5", now.Add(openAIModelTransientShortCooldown+2*time.Second)))
}

func TestOpenAIModelTransient_ConsecutiveRoundsEscalateModelBlock(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.recordFailure(35, "gpt-5.5", now)
	state.recordFailure(35, "gpt-5.5", now.Add(time.Second))

	now = now.Add(11 * time.Second)
	for i, cooldown := range []time.Duration{45 * time.Second, 90 * time.Second, 180 * time.Second, 300 * time.Second, 300 * time.Second} {
		decision := state.recordFailure(35, "gpt-5.5", now)
		require.Equal(t, i+3, decision.FailureStreak)
		require.Equal(t, cooldown, decision.Cooldown)
		require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(cooldown-time.Nanosecond)))
		require.False(t, state.isBlocked(35, "gpt-5.5", decision.BlockUntil))
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				inFlight := state.recordFailure(35, "gpt-5.5", now.Add(time.Second))
				assert.Equal(t, decision.FailureStreak, inFlight.FailureStreak)
				assert.Equal(t, decision.BlockUntil, inFlight.BlockUntil)
				assert.Zero(t, inFlight.Cooldown)
			}()
		}
		wg.Wait()
		now = decision.BlockUntil
	}
}

func TestOpenAIModelTransient_BlockIsIsolatedByModel(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.recordFailure(35, "gpt-5.6-terra", now)
	state.recordFailure(35, "GPT-5.6-TERRA", now.Add(time.Second))

	assert.True(t, state.isBlocked(35, "gpt-5.6-terra", now.Add(2*time.Second)))
	assert.False(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	assert.False(t, state.isBlocked(47, "gpt-5.6-terra", now.Add(2*time.Second)))
}

func TestOpenAIModelTransient_SuccessClearsStreakAndBlock(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.recordFailure(35, "gpt-5.5", now)
	state.recordFailure(35, "gpt-5.5", now.Add(time.Second))
	require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	state.recordSuccessAt(35, "gpt-5.5", now)
	require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)), "an older ordinary request cannot erase a newer failure")

	state.recordSuccess(35, "gpt-5.5")

	assert.False(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	decision := state.recordFailure(35, "gpt-5.5", now.Add(3*time.Second))
	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
}

func TestOpenAIModelTransient_StaleStreakExpires(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.recordFailure(35, "gpt-5.5", now)

	decision := state.recordFailure(35, "gpt-5.5", now.Add(openAIModelTransientStreakTTL+time.Second))

	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
	_, ok := state.readEntry(35, "gpt-5.5", now.Add(2*openAIModelTransientStreakTTL+2*time.Second))
	require.False(t, ok)
	require.Zero(t, state.size())

}

// A streak must not depend on how often the gateway is called. Sparse traffic
// used to reset the streak between every request, so a broken account+model was
// never cooled down and each request paid a failed attempt plus a failover.
func TestOpenAIModelTransient_StreakSurvivesSparseTraffic(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	gap := 5 * time.Minute
	require.Greater(t, gap, openAIModelTransientLongCooldown,
		"the gap must exceed every cooldown, otherwise this passes for the wrong reason")

	first := state.recordFailure(35, "gpt-5.5", now)
	second := state.recordFailure(35, "gpt-5.5", now.Add(gap))
	third := state.recordFailure(35, "gpt-5.5", now.Add(2*gap))

	assert.Equal(t, 1, first.FailureStreak)
	assert.Zero(t, first.Cooldown)
	assert.Equal(t, 2, second.FailureStreak)
	assert.Equal(t, openAIModelTransientShortCooldown, second.Cooldown)
	assert.Equal(t, 3, third.FailureStreak)
	assert.Equal(t, openAIModelTransientLongCooldown, third.Cooldown)
	assert.True(t, state.isBlocked(35, "gpt-5.5", now.Add(2*gap+time.Second)))
}

// A success between two sparse failures still clears the streak, so an account
// that intermittently works is not pushed into the long cooldown.
func TestOpenAIModelTransient_SuccessResetsStreakAcrossSparseTraffic(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	gap := 5 * time.Minute

	state.recordFailure(35, "gpt-5.5", now)
	state.recordSuccess(35, "gpt-5.5")

	decision := state.recordFailure(35, "gpt-5.5", now.Add(gap))

	assert.Equal(t, 1, decision.FailureStreak)
	assert.Zero(t, decision.Cooldown)
	assert.False(t, state.isBlocked(35, "gpt-5.5", now.Add(gap+time.Second)))
}

func TestOpenAIModelTransient_SlowFastSlowKeepsSlowStreakContinuousOnly(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	first := state.recordSlowFailure(35, "gpt-5.5", now)
	require.Equal(t, 1, first.SlowStreak)
	fast := state.recordFailure(35, "gpt-5.5", now.Add(time.Second))
	require.Zero(t, fast.SlowStreak)
	second := state.recordSlowFailure(35, "gpt-5.5", now.Add(2*time.Second))
	require.Equal(t, 1, second.SlowStreak)
	require.Equal(t, 3, second.FailureStreak)
	require.Equal(t, openAIModelTransientLongCooldown, second.Cooldown)
	third := state.recordSlowFailure(35, "gpt-5.5", now.Add(3*time.Second))
	require.Equal(t, 2, third.SlowStreak)
	require.Equal(t, time.Minute, third.Cooldown)
}

func TestOpenAIModelTransient_OldOrUnknownSuccessDoesNotClearSlowBlock(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.recordSlowFailure(35, "gpt-5.5", now)
	state.recordSlowFailure(35, "gpt-5.5", now.Add(time.Second))
	state.recordSuccessAt(35, "gpt-5.5", now)
	require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	state.recordSuccessAt(35, "gpt-5.5", time.Time{})
	require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	state.recordSuccessAt(35, "gpt-5.5", now.Add(2*time.Second))
	require.False(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
}

func TestOpenAIModelTransient_FastFailureDoesNotShortenSlowBlock(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	state.recordSlowFailure(35, "gpt-5.5", now)
	second := state.recordSlowFailure(35, "gpt-5.5", now.Add(time.Second))
	fast := state.recordFailure(35, "gpt-5.5", now.Add(2*time.Second))
	require.Equal(t, second.BlockUntil, fast.BlockUntil)
	require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(30*time.Second)))
	state.recordSuccessAt(35, "gpt-5.5", now)
	require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(30*time.Second)), "fast failure preserves the old-success guard")
}

func TestOpenAIModelTransient_IgnoresInvalidKeys(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)

	assert.Zero(t, state.recordFailure(0, "gpt-5.5", now).FailureStreak)
	assert.Zero(t, state.recordFailure(35, " ", now).FailureStreak)
	assert.False(t, state.isBlocked(0, "gpt-5.5", now))
	assert.False(t, state.isBlocked(35, "", now))
	assert.Equal(t, 0, state.size())
}

func TestOpenAIModelTransient_IgnoresOversizedModelKey(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	model := strings.Repeat("m", openAIModelTransientMaxModelBytes+1)

	decision := state.recordFailure(35, model, now)

	assert.Zero(t, decision.FailureStreak)
	assert.False(t, state.isBlocked(35, model, now))
	assert.Equal(t, 0, state.size())
}

func TestOpenAIModelTransient_StateIsBoundedAndConcurrencySafe(t *testing.T) {
	const maxEntries = 16
	state := newOpenAIAccountModelTransientState(maxEntries)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup

	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("gpt-test-%d", i)
			state.recordFailure(int64(i+1), model, now.Add(time.Duration(i)*time.Millisecond))
			state.recordSlowFailure(int64(i+1), model, now.Add(time.Duration(i)*time.Millisecond))
			_ = state.isBlocked(int64(i+1), model, now.Add(time.Second))
		}(i)
	}
	wg.Wait()

	assert.LessOrEqual(t, state.size(), maxEntries)
}

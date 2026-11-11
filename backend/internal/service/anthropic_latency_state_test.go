//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnthropicLatencyStateHoldDecayAndThreshold(t *testing.T) {
	state := newAnthropicLatencyHoldState(16)
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		_, started, _, _ := state.record(1, "claude-test", time.Second, now)
		require.False(t, started)
	}
	entry, started, _, _ := state.record(1, "claude-test", 11*time.Second, now.Add(time.Second))
	require.False(t, started)
	require.Zero(t, entry.holdUntil)

	state = newAnthropicLatencyHoldState(16)
	for i := 0; i < 5; i++ {
		state.record(1, "claude-test", 11*time.Second, now)
	}
	state.record(1, "claude-test", time.Second, now.Add(anthropicLatencyHoldDecayTau))
	snap := state.snapshot(1, "claude-test", now.Add(anthropicLatencyHoldDecayTau))
	require.False(t, snap.Held)
}

func TestAnthropicLatencyStateHoldStartClearsSamples(t *testing.T) {
	state := newAnthropicLatencyHoldState(16)
	now := time.Unix(2000, 0)
	for i := 0; i < 5; i++ {
		state.record(2, "claude-test", 11*time.Second, now)
	}
	state.mu.Lock()
	before := state.entries[anthropicLatencyStateKey{AccountID: 2, Model: "claude-test"}]
	state.mu.Unlock()
	require.GreaterOrEqual(t, before.weightedSamples, float64(5))
	entry, started, _, _ := state.record(2, "claude-test", 11*time.Second, now)
	require.True(t, started)
	require.Zero(t, entry.weightedSamples)
	require.Zero(t, entry.weightedSlow)
	state.mu.Lock()
	entry = state.entries[anthropicLatencyStateKey{AccountID: 2, Model: "claude-test"}]
	state.mu.Unlock()
	require.Zero(t, entry.weightedSamples)
}

func TestAnthropicLatencyStateHoldExpiresAndRequiresNewEvidence(t *testing.T) {
	state := newAnthropicLatencyHoldState(16)
	now := time.Unix(3000, 0)
	for i := 0; i < 6; i++ {
		state.record(3, "claude-test", 11*time.Second, now)
	}
	require.True(t, state.snapshot(3, "claude-test", now).Held)
	expired := now.Add(anthropicLatencyHoldDuration)
	require.False(t, state.snapshot(3, "claude-test", expired).Held)
	for i := 0; i < 5; i++ {
		state.record(3, "claude-test", 11*time.Second, expired)
	}
	require.False(t, state.snapshot(3, "claude-test", expired).Held)
	state.record(3, "claude-test", 11*time.Second, expired)
	require.True(t, state.snapshot(3, "claude-test", expired).Held)
}

func TestAnthropicLatencyStateThresholdAndNoEscalation(t *testing.T) {
	now := time.Unix(4000, 0)
	for _, slow := range []int{6, 7} {
		state := newAnthropicLatencyHoldState(16)
		for i := 0; i < 20; i++ {
			sample := time.Second
			if i >= 20-slow {
				sample = 10 * time.Second
			}
			state.record(1, "claude-test", sample, now)
		}
		snapshot := state.snapshot(1, "claude-test", now)
		require.Equal(t, slow == 7, snapshot.Held)
		if !snapshot.Held {
			continue
		}
		require.Equal(t, now.Add(3*time.Minute), snapshot.HoldUntil)
		for round := 1; round < 5; round++ {
			at := now.Add(time.Duration(round) * 3 * time.Minute)
			for i := 0; i < 6; i++ {
				state.record(1, "claude-test", 10*time.Second, at)
			}
			require.Equal(t, at.Add(3*time.Minute), state.snapshot(1, "claude-test", at).HoldUntil)
		}
	}
}

func TestAnthropicLatencyStatePartitionDoesNotCrossPriority(t *testing.T) {
	items := []*Account{{ID: 1, Priority: 1}, {ID: 2, Priority: 1}, {ID: 3, Priority: 1}, {ID: 4, Priority: 2}, {ID: 5, Priority: 2}}
	held := map[int64]anthropicLatencyHoldSnapshot{1: {Held: true}, 4: {Held: true}}
	got := partitionAnthropicLatencyHeldSegments(items, func(a *Account) *Account { return a }, held, func(a *Account) int { return a.Priority })
	ids := make([]int64, len(got))
	for i, account := range got {
		ids[i] = account.ID
	}
	require.Equal(t, []int64{2, 3, 1, 5, 4}, ids)
}

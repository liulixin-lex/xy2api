//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSchedModelTransientListClear(t *testing.T) {
	now := time.Now()
	state := newOpenAIAccountModelTransientState(10)
	state.recordFailure(1, "one", now)
	state.recordFailure(1, "blocked", now)
	state.recordFailure(1, "blocked", now)
	state.recordFailure(2, "other", now)
	state.recordFailure(1, "expired", now.Add(-openAIModelTransientStreakTTL-time.Second))
	before := make(map[openAIAccountModelKey]openAIAccountModelTransientEntry)
	for k, v := range state.entries {
		before[k] = v
	}
	rows := state.listAt(1, now)
	require.Len(t, rows, 2)
	require.Equal(t, "blocked", rows[0].Model)
	require.NotNil(t, rows[0].Until)
	require.Equal(t, "one", rows[1].Model)
	require.Nil(t, rows[1].Until)
	require.Equal(t, before, state.entries, "List must not touch entries, timestamps, or expired entries")
	require.True(t, state.Clear(1, " BLOCKED "))
	require.False(t, state.Clear(1, "blocked"))
	first := state.recordFailure(1, "blocked", now)
	require.Equal(t, 1, first.FailureStreak)
	require.Zero(t, first.Cooldown)
	require.Equal(t, 3, state.ClearAll(1))
	require.Len(t, state.List(2), 1)
	require.Empty(t, state.List(1))
	first = state.recordFailure(1, "one", now)
	require.Equal(t, 1, first.FailureStreak)
	require.Zero(t, first.Cooldown)
}

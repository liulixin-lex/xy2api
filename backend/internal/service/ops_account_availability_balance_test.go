//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpsBalanceState(t *testing.T) {
	since := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	state, at := opsBalanceState(&Account{Extra: resilience.BalanceRecord{State: resilience.BalanceExhausted, Since: since}.Extra()})
	require.Equal(t, "EXHAUSTED", state)
	require.True(t, at.Equal(since))
	state, at = opsBalanceState(&Account{Extra: resilience.BalanceRecord{State: resilience.BalanceOK}.Extra()})
	require.Empty(t, state)
	require.Nil(t, at)
	state, at = opsBalanceState(&Account{})
	require.Empty(t, state)
	require.Nil(t, at)
}

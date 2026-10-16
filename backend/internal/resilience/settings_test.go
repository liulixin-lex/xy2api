package resilience

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloneSharesNothing(t *testing.T) {
	s := Defaults()
	s.Budget.ModelOverrides = map[string]int{"m": 200}
	c := s.Clone()
	c.Budget.ModelOverrides["m"] = 1
	c.Balance.RecheckScheduleSeconds[0] = 1
	c.Keepalive.Edges[0] = "x"
	c.Failure.BalanceKeywordsExtra = append(c.Failure.BalanceKeywordsExtra, "y")
	require.Equal(t, 200, s.Budget.ModelOverrides["m"])
	require.Equal(t, 60, s.Balance.RecheckScheduleSeconds[0])
	require.Equal(t, "cf", s.Keepalive.Edges[0])
	require.Empty(t, s.Failure.BalanceKeywordsExtra)
}

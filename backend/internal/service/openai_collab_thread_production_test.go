//go:build unit

package service

import (
	"io"
	"log/slog"
	"math/rand"
	"testing"
	"testing/synctest"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabThreadBindingProductionOracle(t *testing.T) {
	withResilienceSettings(t, nil)
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, seed := range []int64{31, 709, 3019} {
		r := rand.New(rand.NewSource(seed))
		for i := range 1000 {
			c := collabOracleCase{production: true, bound: int64(r.Intn(4)), guardian: r.Intn(2) == 0, avoid: r.Intn(2) == 0, alias: r.Intn(2) == 0, plain: r.Intn(2) == 0, mode: i % 12, slots: map[int64]bool{}}
			for id := int64(1); id <= 3; id++ {
				a := selectionFixtureAccount(id, r.Intn(3))
				a.Schedulable = r.Intn(8) != 0
				c.accounts = append(c.accounts, a)
				c.slots[id] = r.Intn(3) != 0
			}
			synctest.Test(t, func(t *testing.T) {
				resilience.ProcessNegativeCache().InvalidateGroup(7)
				want := collabOracleOutcome(t, c, false)
				resilience.ProcessNegativeCache().InvalidateGroup(7)
				got := collabOracleOutcome(t, c, true)
				require.Equal(t, want, got, "seed=%d case=%d bound=%d guardian=%t alias=%t plain=%t mode=%d avoid=%t", seed, i, c.bound, c.guardian, c.alias, c.plain, c.mode, c.avoid)
			})
		}
		t.Logf("seed=%d: zero differences in 1000 production requests", seed)
	}
}

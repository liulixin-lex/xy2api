//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedV2DecisionStateTransactionAndReset(t *testing.T) {
	r, a, g := qualityRepoFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	basis := q.DecisionBasis{RuleVersion: q.RuleVersion, Dims: []q.Dimension{q.DimensionTTFO, q.DimensionError}, AttemptIDs: []string{"synthetic-attempt"}}
	row := q.StateRow{AccountID: a, GroupID: g, Model: "synthetic-model", State: q.StateCooling, Level: 1, Since: at, Until: at.Add(20 * time.Minute), EvidenceSince: at.Add(-time.Hour), Basis: &basis}
	decision := q.Decision{At: at, Scope: q.Scope{AccountID: a, GroupID: g, Model: row.Model}, Action: "enter", Level: 1, Until: row.Until, Basis: basis}
	t.Cleanup(func() {
		_, err := integrationDB.Exec("DELETE FROM sched_decisions WHERE account_id=$1", a)
		require.NoError(t, err)
	})
	failure := errors.New("rollback evidence")
	_, err := r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		if err := tx.Upsert([]q.StateRow{row}); err != nil {
			return err
		}
		if err := tx.InsertDecisions([]q.Decision{decision}); err != nil {
			return err
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	var n int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM sched_states WHERE account_id=$1", a).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM sched_decisions WHERE account_id=$1", a).Scan(&n))
	require.Zero(t, n)
	_, err = r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		if err := tx.Upsert([]q.StateRow{row}); err != nil {
			return err
		}
		return tx.InsertDecisions([]q.Decision{decision})
	})
	require.NoError(t, err)
	rows, _, _, err := r.LoadRows(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, basis.Dims, rows[0].Basis.Dims)
	require.WithinDuration(t, row.EvidenceSince, rows[0].EvidenceSince, time.Microsecond)
	reset, err := r.RunReset(ctx, a)
	require.NoError(t, err)
	require.False(t, reset.Before(at))
	rows, _, _, err = r.LoadRows(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
	var action, version string
	require.NoError(t, integrationDB.QueryRow("SELECT action,rule_version FROM sched_decisions WHERE account_id=$1 ORDER BY id DESC LIMIT 1", a).Scan(&action, &version))
	require.Equal(t, "reset", action)
	require.Equal(t, q.RuleVersion, version)
}

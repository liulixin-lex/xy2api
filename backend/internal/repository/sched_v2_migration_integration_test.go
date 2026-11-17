//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedV2DecisionStateTransactionAndReset(t *testing.T) {
	r, a, g := qualityRepoFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	basis := q.Basis{RuleVersion: q.RuleVersion, Line: q.DimensionTTFO, SourceGroups: []int64{g}, AttemptIDs: []string{"synthetic-attempt"}}
	row := q.LineState{LineKey: q.LineKey{AccountID: a, GroupID: g, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Level: 1, Admissions: 2, PendingAt: at.Add(-5 * time.Minute), Entry: at, Until: at.Add(20 * time.Minute), Epoch: at.Add(-time.Hour), LastRear: at, EvaluatedAt: at, Basis: &basis}
	decision := q.Decision{At: at, Key: row.LineKey, Action: "enter", Level: 1, Until: row.Until, Basis: basis}
	t.Cleanup(func() {
		_, err := integrationDB.Exec("DELETE FROM sched_decisions WHERE account_id=$1", a)
		require.NoError(t, err)
	})
	failure := errors.New("rollback evidence")
	_, err := r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		if err := tx.Upsert([]q.LineState{row}); err != nil {
			return err
		}
		if err := tx.InsertDecisions([]q.Decision{decision}); err != nil {
			return err
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	var n int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM sched_line_states WHERE account_id=$1", a).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM sched_decisions WHERE account_id=$1", a).Scan(&n))
	require.Zero(t, n)
	_, err = r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		if err := tx.Upsert([]q.LineState{row}); err != nil {
			return err
		}
		return tx.InsertDecisions([]q.Decision{decision})
	})
	require.NoError(t, err)
	rows, _, _, err := r.LoadRows(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	want, got := row, rows[0]
	expectedBasis := basis
	expectedBasis.AttemptIDs = nil
	want.Basis = &expectedBasis
	for _, field := range []struct {
		name      string
		want, got *time.Time
	}{
		{"pending_at", &want.PendingAt, &got.PendingAt}, {"epoch", &want.Epoch, &got.Epoch}, {"entry", &want.Entry, &got.Entry},
		{"until", &want.Until, &got.Until}, {"trial_until", &want.TrialUntil, &got.TrialUntil}, {"good_since", &want.GoodSince, &got.GoodSince},
		{"last_rear", &want.LastRear, &got.LastRear}, {"evaluated_at", &want.EvaluatedAt, &got.EvaluatedAt},
	} {
		if field.want.IsZero() {
			require.True(t, field.got.IsZero(), field.name)
		} else {
			require.WithinDuration(t, *field.want, *field.got, time.Microsecond, field.name)
		}
		*field.want = time.Time{}
		*field.got = time.Time{}
	}
	require.Equal(t, want, got)
	var recorded pq.StringArray
	require.NoError(t, integrationDB.QueryRow("SELECT attempt_ids FROM sched_decisions WHERE account_id=$1 AND action='enter'", a).Scan(&recorded))
	require.Equal(t, basis.AttemptIDs, []string(recorded))
	require.Equal(t, basis.SourceGroups, rows[0].Basis.SourceGroups)
	require.WithinDuration(t, row.Epoch, rows[0].Epoch, time.Microsecond)
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

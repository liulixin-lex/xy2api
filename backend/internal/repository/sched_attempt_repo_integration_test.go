//go:build integration

package repository

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func schedAttemptTestRow(account int64, started time.Time) schedquality.AttemptSample {
	return schedquality.AttemptSample{
		AttemptID: uuid.Must(uuid.NewV7()).String(), StartedAt: started, FinishedAt: started.Add(time.Second),
		AccountID: account, GroupID: 1, Model: "model", Class: schedquality.ClassRegular,
		Outcome: schedquality.OutcomeOther,
	}
}

func TestSchedAttemptRepositoryIdempotenceAndDelayedInsert(t *testing.T) {
	ctx := context.Background()
	repo := NewSchedAttemptRepository(integrationDB)
	account := int64(8211001)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_attempt_samples WHERE account_id=$1`, account)
	})
	var start time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT now()`).Scan(&start))
	first := schedAttemptTestRow(account, start)
	late := schedAttemptTestRow(account, start.Add(-45*time.Second))
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{first, first}))
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{first}))
	rows, cursor, err := repo.LoadInsertedAfter(ctx, start.Add(-time.Second), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, first.AttemptID, rows[0].AttemptID)
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{late}))
	rows, _, err = repo.LoadInsertedAfter(ctx, cursor.Add(-schedquality.LedgerReloadOverlap), 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, late.AttemptID, rows[1].AttemptID)
	recent, err := repo.LoadRecentByAccount(ctx, account, 10)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	require.Equal(t, schedquality.OutcomeOther, recent[0].Outcome)
	started, err := repo.LoadStartedSince(ctx, start.Add(-time.Second))
	require.NoError(t, err)
	require.Len(t, started, 1)
	deleted, err := repo.DeleteBefore(ctx, start.Add(-time.Second))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
}

func TestSchedAttemptRepositoryOverlappingSlowStatement(t *testing.T) {
	ctx := context.Background()
	repo := NewSchedAttemptRepository(integrationDB)
	const account = int64(8211002)
	_, err := integrationDB.ExecContext(ctx, `CREATE FUNCTION sched_attempt_test_delay() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.account_id = 8211002 AND NEW.request_id = 'slow' THEN PERFORM pg_sleep(4); END IF; RETURN NEW; END $$`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `CREATE TRIGGER sched_attempt_test_delay BEFORE INSERT ON account_attempt_samples FOR EACH ROW EXECUTE FUNCTION sched_attempt_test_delay()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS sched_attempt_test_delay ON account_attempt_samples`)
		_, _ = integrationDB.ExecContext(ctx, `DROP FUNCTION IF EXISTS sched_attempt_test_delay()`)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_attempt_samples WHERE account_id=$1`, account)
	})
	var start time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT now() - interval '1 second'`).Scan(&start))
	slow := schedAttemptTestRow(account, start)
	slow.RequestID = "slow"
	fast := schedAttemptTestRow(account, start)
	finished := make(chan error, 1)
	go func() { finished <- repo.InsertBatch(ctx, []schedquality.AttemptSample{slow}) }()
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		var running bool
		err := integrationDB.QueryRowContext(waitCtx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event='PgSleep' AND query LIKE '%INSERT INTO account_attempt_samples%')`).Scan(&running)
		require.NoError(t, err)
		if running {
			break
		}
	}
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{fast}))
	rows, cursor, err := repo.LoadInsertedAfter(ctx, start, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, fast.AttemptID, rows[0].AttemptID)
	require.NoError(t, <-finished)
	rows, _, err = repo.LoadInsertedAfter(ctx, cursor.Add(-schedquality.LedgerReloadOverlap), 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, slow.AttemptID, rows[0].AttemptID)
	require.True(t, rows[0].InsertedAt.Before(cursor))
}

func TestSchedAttemptRepositoryInsertedPageKeepsTimestampGroup(t *testing.T) {
	ctx := context.Background()
	repo := NewSchedAttemptRepository(integrationDB)
	const account = int64(8211003)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_attempt_samples WHERE account_id=$1`, account)
	})
	var start time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT now()`).Scan(&start))
	ids := make(map[string]bool)
	for _, n := range []int{200, 40, 10} {
		batch := make([]schedquality.AttemptSample, n)
		for i := range batch {
			batch[i] = schedAttemptTestRow(account, start)
			ids[batch[i].AttemptID] = true
		}
		require.NoError(t, repo.InsertBatch(ctx, batch))
	}
	var base time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT min(inserted_at) - interval '1 microsecond' FROM account_attempt_samples WHERE account_id=$1`, account).Scan(&base))
	rows, cursor, err := repo.LoadInsertedAfter(ctx, base, 50)
	require.NoError(t, err)
	require.Len(t, rows, 200)
	for _, row := range rows {
		require.True(t, ids[row.AttemptID])
		delete(ids, row.AttemptID)
		require.True(t, cursor.Equal(row.InsertedAt))
	}
	for _, c := range []struct{ limit, want int }{{1, 40}, {40, 40}, {45, 50}, {50, 50}, {0, 50}} {
		page, maxInserted, err := repo.LoadInsertedAfter(ctx, cursor, c.limit)
		require.NoError(t, err)
		require.Len(t, page, c.want, "limit %d", c.limit)
		require.True(t, maxInserted.Equal(page[len(page)-1].InsertedAt))
		require.True(t, sort.SliceIsSorted(page, func(i, j int) bool {
			if !page[i].InsertedAt.Equal(page[j].InsertedAt) {
				return page[i].InsertedAt.Before(page[j].InsertedAt)
			}
			return page[i].AttemptID < page[j].AttemptID
		}))
	}
	last, lastCursor, err := repo.LoadInsertedAfter(ctx, cursor, 50)
	require.NoError(t, err)
	for _, row := range last {
		require.True(t, ids[row.AttemptID])
		delete(ids, row.AttemptID)
	}
	require.Empty(t, ids)
	next, nextCursor, err := repo.LoadInsertedAfter(ctx, lastCursor, 50)
	require.NoError(t, err)
	require.Empty(t, next)
	require.True(t, nextCursor.IsZero())
}

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newImageTaskRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func TestImageTaskStoreUpdateAndDelete(t *testing.T) {
	_, rdb := newImageTaskRedis(t)
	store := NewImageTaskStore(rdb)
	ctx := context.Background()
	task := &service.ImageTaskRecord{ID: "imgtask_u", UserID: 1, APIKeyID: 2, Status: service.ImageTaskStatusProcessing}
	require.NoError(t, store.Save(ctx, task, time.Hour))

	updated, err := store.Update(ctx, task.ID, time.Hour, func(rec *service.ImageTaskRecord) error {
		rec.Status = service.ImageTaskStatusCompleted
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusCompleted, updated.Status)

	sentinel := errors.New("finished")
	_, err = store.Update(ctx, task.ID, time.Hour, func(rec *service.ImageTaskRecord) error {
		return sentinel
	})
	require.ErrorIs(t, err, sentinel, "a mutation error aborts the write and is returned")
	got, err := store.Get(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusCompleted, got.Status)

	unchanged, err := store.Update(ctx, task.ID, time.Hour, func(rec *service.ImageTaskRecord) error {
		return service.ErrImageTaskUnchanged
	})
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusCompleted, unchanged.Status)

	_, err = store.Update(ctx, "imgtask_missing", time.Hour, func(*service.ImageTaskRecord) error { return nil })
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)

	require.NoError(t, store.Delete(ctx, task.ID))
	_, err = store.Get(ctx, task.ID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
}

// A write that lands between WATCH and EXEC makes the update re-run on the
// fresh value instead of overwriting it.
func TestImageTaskStoreUpdateRetriesOnConcurrentWrite(t *testing.T) {
	_, rdb := newImageTaskRedis(t)
	store := NewImageTaskStore(rdb)
	ctx := context.Background()
	require.NoError(t, store.Save(ctx, &service.ImageTaskRecord{ID: "imgtask_c", Status: service.ImageTaskStatusProcessing}, time.Hour))

	calls := 0
	updated, err := store.Update(ctx, "imgtask_c", time.Hour, func(rec *service.ImageTaskRecord) error {
		calls++
		if calls == 1 {
			// Another writer concludes the task meanwhile.
			require.NoError(t, store.Save(ctx, &service.ImageTaskRecord{ID: "imgtask_c", Status: service.ImageTaskStatusFailed}, time.Hour))
		}
		if rec.Status != service.ImageTaskStatusProcessing {
			return service.ErrImageTaskUnchanged
		}
		rec.HTTPStatus = 200
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, service.ImageTaskStatusFailed, updated.Status)
	got, err := store.Get(ctx, "imgtask_c")
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusFailed, got.Status)
	require.Zero(t, got.HTTPStatus)
}

func TestImageTaskCoordinatorIdempotency(t *testing.T) {
	mr, rdb := newImageTaskRedis(t)
	c := NewImageTaskCoordinator(rdb)
	ctx := context.Background()

	state, claimed, err := c.ClaimIdempotency(ctx, "1:2:k", "fp", 2*time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, "fp", state.Fingerprint)
	require.Equal(t, 2*time.Minute, mr.TTL(imageTaskIdempotencyPrefix+"1:2:k"))

	state, claimed, err = c.ClaimIdempotency(ctx, "1:2:k", "other", 2*time.Minute)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, service.ImageTaskIdempotency{Fingerprint: "fp"}, state, "still being submitted")

	require.NoError(t, c.BindIdempotency(ctx, "1:2:k", service.ImageTaskIdempotency{Fingerprint: "fp", TaskID: "imgtask_1"}, 24*time.Hour))
	state, claimed, err = c.ClaimIdempotency(ctx, "1:2:k", "fp", 2*time.Minute)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, "imgtask_1", state.TaskID)
	require.Equal(t, 24*time.Hour, mr.TTL(imageTaskIdempotencyPrefix+"1:2:k"))

	require.NoError(t, c.ReleaseIdempotency(ctx, "1:2:k"))
	_, claimed, err = c.ClaimIdempotency(ctx, "1:2:k", "fp2", 2*time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
}

func TestImageTaskCoordinatorInFlightIndex(t *testing.T) {
	_, rdb := newImageTaskRedis(t)
	c := NewImageTaskCoordinator(rdb)
	ctx := context.Background()
	base := time.Unix(1_800_000_000, 0)
	live := base.Add(-2 * time.Minute)

	ok, err := c.AdmitInFlight(ctx, 9, "a", 2, base, live)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = c.AdmitInFlight(ctx, 9, "b", 2, base, live)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = c.AdmitInFlight(ctx, 9, "c", 2, base, live)
	require.NoError(t, err)
	require.False(t, ok, "per-key cap reached")
	ok, err = c.AdmitInFlight(ctx, 10, "d", 2, base, live)
	require.NoError(t, err)
	require.True(t, ok)

	// Three minutes later only "b" kept heartbeating.
	later := base.Add(3 * time.Minute)
	require.NoError(t, c.TouchInFlight(ctx, 9, "b", later))
	stale, err := c.StaleInFlight(ctx, later.Add(-2*time.Minute), 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []service.ImageTaskInFlight{{TaskID: "a", APIKeyID: 9}, {TaskID: "d", APIKeyID: 10}}, stale)

	// Stale tasks do not hold the key's slots.
	ok, err = c.AdmitInFlight(ctx, 9, "e", 2, later, later.Add(-2*time.Minute))
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, c.ReleaseInFlight(ctx, 9, "a"))
	require.NoError(t, c.TouchInFlight(ctx, 9, "a", later), "touching a released task is a no-op")
	stale, err = c.StaleInFlight(ctx, later.Add(-2*time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, []service.ImageTaskInFlight{{TaskID: "d", APIKeyID: 10}}, stale)

	ok, err = c.AdmitInFlight(ctx, 11, "f", 0, later, later)
	require.NoError(t, err)
	require.True(t, ok, "limit 0 means no per-key cap")
}

func TestImageTaskCoordinatorLockAndPollLimit(t *testing.T) {
	mr, rdb := newImageTaskRedis(t)
	c := NewImageTaskCoordinator(rdb)
	ctx := context.Background()

	locked, err := c.TryLock(ctx, "sweeper", time.Minute)
	require.NoError(t, err)
	require.True(t, locked)
	locked, err = c.TryLock(ctx, "sweeper", time.Minute)
	require.NoError(t, err)
	require.False(t, locked)
	mr.FastForward(2 * time.Minute)
	locked, err = c.TryLock(ctx, "sweeper", time.Minute)
	require.NoError(t, err)
	require.True(t, locked)

	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 3; i++ {
		allowed, err := c.AllowPoll(ctx, 9, 3, now)
		require.NoError(t, err)
		require.True(t, allowed)
	}
	allowed, err := c.AllowPoll(ctx, 9, 3, now)
	require.NoError(t, err)
	require.False(t, allowed)
	allowed, err = c.AllowPoll(ctx, 10, 3, now)
	require.NoError(t, err)
	require.True(t, allowed, "limits are per key")
	allowed, err = c.AllowPoll(ctx, 9, 3, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, allowed, "the window is one second")
}

func TestImageTaskBillingLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	lookup := NewImageTaskBillingLookup(db)
	rows := func(charged, pending bool) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"charged", "pending"}).AddRow(charged, pending)
	}
	query := `SELECT\s+EXISTS \(SELECT 1 FROM usage_logs WHERE request_id = \$1 AND api_key_id = \$2\),\s+EXISTS \(SELECT 1 FROM usage_billing_intents`
	mock.ExpectQuery(query).WithArgs("client:a", int64(9)).WillReturnRows(rows(true, false))
	mock.ExpectQuery(query).WithArgs("client:b", int64(9)).WillReturnRows(rows(false, true))
	mock.ExpectQuery(query).WithArgs("client:c", int64(9)).WillReturnRows(rows(false, false))
	mock.ExpectQuery(query).WithArgs("client:d", int64(9)).WillReturnError(errors.New("db down"))

	status, err := lookup.ImageTaskBillingStatus(context.Background(), "client:a", 9)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskBillingCharged, status)
	status, err = lookup.ImageTaskBillingStatus(context.Background(), "client:b", 9)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskBillingPending, status)
	status, err = lookup.ImageTaskBillingStatus(context.Background(), "client:c", 9)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskBillingNotCharged, status)
	_, err = lookup.ImageTaskBillingStatus(context.Background(), "client:d", 9)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

package usageexport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExportConcurrentAdmissionAndBudgets(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := []string{}
	errs := []error{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task, err := e.Create(ctx, 1, "user", fmt.Sprintf("duplicate-%02d", i), options())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else {
				ids = append(ids, task.ID)
			}
		}(i)
	}
	wg.Wait()
	require.Empty(t, errs)
	require.Len(t, ids, 12)
	for _, id := range ids {
		require.Equal(t, ids[0], id)
	}
	var charged int
	require.NoError(t, e.DB.QueryRow(`SELECT count FROM usage_export_rate WHERE owner_id=1 AND scope='create'`).Scan(&charged))
	require.Equal(t, 1, charged)
	e.Config.QueueLimit = 1
	o := options()
	o.Filters.UserID = 2
	_, err := e.Create(ctx, 2, "user", "queue-full", o)
	require.ErrorContains(t, err, "QUEUE_FULL")
	require.NoError(t, e.Cancel(ctx, 1, "user", ids[0]))
	require.NoError(t, e.Cleanup(ctx))
	e.Config.StorageBytes = e.Config.TaskDiskBytes - 1
	_, err = e.Create(ctx, 2, "user", "disk-full-1", o)
	require.ErrorContains(t, err, "STORAGE_FULL")
	e.Config.StorageBytes = 1 << 30
	e.Config.FreeBytes = 1 << 60
	_, err = e.Create(ctx, 2, "user", "disk-full-2", o)
	require.ErrorContains(t, err, "STORAGE_FULL")
}
func TestExportExpiryTicketAndDownloadLimits(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, func(context.Context, *sql.Tx, Options, func([]string) error) error { return nil })
	task, err := e.Create(ctx, 1, "user", "empty-0001", options())
	require.NoError(t, err)
	e.runSlot(ctx, 0)
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "succeeded", task.Status)
	require.EqualValues(t, 0, *task.Total)
	token, err := e.Ticket(ctx, 1, "user", task.ID, TicketIdentity{Owner: 1})
	require.NoError(t, err)
	_, err = e.DB.Exec(`UPDATE usage_export_tickets SET expires_at=now()-interval '1 second'`)
	require.NoError(t, err)
	_, _, err = e.Redeem(ctx, "user", task.ID, token)
	require.ErrorContains(t, err, "INVALID_TICKET")
	a, err := e.DownloadSlot(ctx, 1)
	require.NoError(t, err)
	b, err := e.DownloadSlot(ctx, 1)
	require.NoError(t, err)
	_, err = e.DownloadSlot(ctx, 1)
	require.ErrorContains(t, err, "DOWNLOAD_BUSY")
	a()
	b()
	_, err = e.DB.Exec(`UPDATE usage_export_tasks SET expires_at=now()-interval '1 second' WHERE id=$1`, task.ID)
	require.NoError(t, err)
	_, err = e.Ticket(ctx, 1, "user", task.ID, TicketIdentity{Owner: 1})
	require.ErrorContains(t, err, "EXPIRED")
	require.NoError(t, e.Cleanup(ctx))
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "expired", task.Status)
}

type flakyStore struct {
	Store
	mu       sync.Mutex
	failures int
	puts     int
}

func (s *flakyStore) Put(ctx context.Context, key, file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	if s.failures > 0 {
		s.failures--
		return errors.New("temporary storage failure")
	}
	return s.Store.Put(ctx, key, file)
}
func TestExportUploadRetryReusesSnapshot(t *testing.T) {
	ctx := context.Background()
	reads := 0
	e := testEngine(t, func(_ context.Context, _ *sql.Tx, _ Options, emit func([]string) error) error {
		reads++
		return emit([]string{"frozen"})
	})
	store := &flakyStore{Store: e.Store, failures: 2}
	e.Store = store
	task, err := e.Create(ctx, 1, "user", "retry-0001", options())
	require.NoError(t, err)
	e.runSlot(ctx, 0)
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "succeeded", task.Status)
	require.Equal(t, 1, reads)
	require.Equal(t, 3, store.puts)
	body, err := store.Open(ctx, task.StorageKey)
	require.NoError(t, err)
	b, _ := io.ReadAll(body)
	_ = body.Close()
	require.Contains(t, string(b), "frozen")
}
func TestExportDeadlineAndQueueTimeout(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, func(ctx context.Context, _ *sql.Tx, _ Options, _ func([]string) error) error {
		<-ctx.Done()
		return ctx.Err()
	})
	e.Config.ReadSeconds = 1
	e.Config.RunSeconds = 2
	task, err := e.Create(ctx, 1, "user", "timeout-1", options())
	require.NoError(t, err)
	start := time.Now()
	e.runSlot(ctx, 0)
	require.Less(t, time.Since(start), 5*time.Second)
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "failed", task.Status)
	task, err = e.Create(ctx, 1, "user", "queue-old", options())
	require.NoError(t, err)
	_, err = e.DB.Exec(`UPDATE usage_export_tasks SET created_at=now()-interval '1 hour' WHERE id=$1`, task.ID)
	require.NoError(t, err)
	require.NoError(t, e.Cleanup(ctx))
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "EXPORT_QUEUE_TIMEOUT", task.ErrorCode)
}

func TestExportLostWorkerLockCannotPublish(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	e := testEngine(t, func(ctx context.Context, _ *sql.Tx, _ Options, emit func([]string) error) error {
		if err := emit([]string{"row"}); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	task, err := e.Create(ctx, 1, "user", "connection-loss", options())
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { e.runSlot(ctx, 0); close(done) }()
	<-started
	var terminated bool
	require.NoError(t, e.DB.QueryRow(`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND granted LIMIT 1`, workerLockBase).Scan(&terminated))
	require.True(t, terminated)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop after losing its lock connection")
	}
	task, err = e.Get(ctx, 1, "user", task.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", task.Status)
	require.Equal(t, "EXPORT_INTERRUPTED", task.ErrorCode)
	require.Empty(t, task.SHA256)
}

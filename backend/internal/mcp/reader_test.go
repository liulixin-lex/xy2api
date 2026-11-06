package mcp

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func testReaderActor() Actor {
	return Actor{UserID: 7, Role: "user", GrantID: "grant-1", ClientID: "host-1", TokenVersion: 1}
}
func testReaderCursor() *CursorCodec { return NewCursorCodec([]byte(strings.Repeat("c", 32))) }
func readerMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	t.Cleanup(func() { _ = db.Close() })
	return db, m
}
func expectReaderBegin(m sqlmock.Sqlmock) {
	m.ExpectBegin()
	for _, s := range []string{"SET LOCAL search_path = pg_catalog, public", "SET LOCAL statement_timeout = '2s'", "SET LOCAL lock_timeout = '100ms'"} {
		m.ExpectExec(regexp.QuoteMeta(s)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
}
func TestReaderWindowCursorBinding(t *testing.T) {
	now := time.Now().UTC()
	req := QueryRequest{Tool: "xy2api.me.search_usage", Actor: testReaderActor(), Cursor: testReaderCursor()}
	q, e := newReaderQuery(req, now)
	require.NoError(t, e)
	token, e := req.Cursor.Encode(q.binding, readerPage{Window: q.window, At: now.Add(-time.Minute), ID: 42, Source: "usage_logs"})
	require.NoError(t, e)
	req.Input.Cursor = token
	next, e := newReaderQuery(req, now.Add(time.Minute))
	require.NoError(t, e)
	require.Equal(t, q.window, next.window)
	req.Actor.UserID++
	_, e = newReaderQuery(req, now)
	require.ErrorIs(t, e, ErrInvalidInput)
	req.Actor = testReaderActor()
	req.Actor.GrantID = "other-grant"
	_, e = newReaderQuery(req, now)
	require.ErrorIs(t, e, ErrInvalidInput)
	req.Actor = testReaderActor()
	req.Input.Model = "different"
	_, e = newReaderQuery(req, now)
	require.ErrorIs(t, e, ErrInvalidInput)
	for _, in := range []QueryInput{{Limit: 101}, {Limit: -1}, {StartTime: now.Add(-25 * time.Hour).Format(time.RFC3339Nano), EndTime: now.Format(time.RFC3339Nano)}, {AccountIDs: []int64{-1}}, {RequestID: "line\nbreak"}} {
		req.Input = in
		_, e = newReaderQuery(req, now)
		require.ErrorIs(t, e, ErrInvalidInput)
	}
}
func TestReaderSelfDetailNeverHydratesForeignOwner(t *testing.T) {
	db, m := readerMock(t)
	expectReaderBegin(m)
	m.ExpectQuery(`SELECT .*ul\.user_id=\$3.*ul\.id = \$4`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(7), int64(999), sqlmock.AnyArg(), int64(0), "", 21).WillReturnRows(sqlmock.NewRows([]string{"usage_row_id"}))
	m.ExpectRollback()
	_, e := NewQueryReader(&ReaderDB{db}).Query(context.Background(), QueryRequest{Tool: "xy2api.me.get_usage_detail", Actor: testReaderActor(), Cursor: testReaderCursor(), Input: QueryInput{UsageRowID: 999}})
	require.ErrorIs(t, e, ErrNotFound)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestReaderAttemptsClassifiesBeforeOptionalRead(t *testing.T) {
	db, m := readerMock(t)
	expectReaderBegin(m)
	m.ExpectQuery(`SELECT CASE.*FROM public.ops_error_logs e WHERE e.id=\$1`).WithArgs(int64(9), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"classification"}).AddRow("recovered_upstream"))
	m.ExpectRollback()
	called := false
	_, e := NewQueryReader(&ReaderDB{db}).Query(context.Background(), QueryRequest{Tool: "xy2api.requests.get_attempts", Actor: testReaderActor(), Cursor: testReaderCursor(), Input: QueryInput{ErrorRowID: 9}, RequireSource: func(_ context.Context, key string) error {
		called = true
		require.Equal(t, "GET /ops/upstream-errors/:id", key)
		return ErrForbidden
	}})
	require.True(t, called)
	require.ErrorIs(t, e, ErrForbidden)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestReaderAttemptsProjectsSQLScalars(t *testing.T) {
	db, m := readerMock(t)
	expectReaderBegin(m)
	m.ExpectQuery(`SELECT CASE.*FROM public.ops_error_logs`).WillReturnRows(sqlmock.NewRows([]string{"classification"}).AddRow("final_failure"))
	m.ExpectQuery(`SELECT \* FROM \(SELECT .*jsonb_typeof\(ev->'account_id'\).*WITH ORDINALITY`).WillReturnRows(sqlmock.NewRows([]string{"error_row_id", "attempt_index", "reason", "_page_time", "_page_id", "_page_source"}).AddRow(9, 1, "unknown", time.Now(), 1, "ops_error_logs.upstream_errors"))
	m.ExpectCommit()
	called := false
	got, e := NewQueryReader(&ReaderDB{db}).Query(context.Background(), QueryRequest{Tool: "xy2api.requests.get_attempts", Actor: testReaderActor(), Cursor: testReaderCursor(), Input: QueryInput{ErrorRowID: 9}, RequireSource: func(context.Context, string) error { called = true; return nil }})
	require.NoError(t, e)
	require.False(t, called)
	require.Len(t, got.Items, 1)
	require.NotContains(t, got.Items[0], "_page_time")
	require.NoError(t, m.ExpectationsWereMet())
}
func TestReaderUnavailableIsSanitized(t *testing.T) {
	db, m := readerMock(t)
	m.ExpectBegin().WillReturnError(errors.New("password=canary-secret"))
	_, e := NewQueryReader(&ReaderDB{db}).Query(context.Background(), QueryRequest{Tool: "xy2api.me.get_profile", Actor: testReaderActor()})
	require.ErrorIs(t, e, ErrUnavailable)
	require.NotContains(t, e.Error(), "canary")
	_, e = NewQueryReader(nil).Query(context.Background(), QueryRequest{Tool: "xy2api.me.get_profile", Actor: testReaderActor()})
	require.ErrorIs(t, e, ErrUnavailable)
}
func TestReaderSubscriptionDisplayResetPreservesFinalPeriod(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-31 * 24 * time.Hour)
	expiry := start.Add(30 * 24 * time.Hour)
	m := map[string]any{"starts_at": start, "expires_at": expiry, "status": "active", "monthly_window_start": start, "monthly_usage_usd": 9.0}
	normalizeSubscriptionItem(m, now)
	require.Equal(t, "expired", m["status"])
	require.Equal(t, 9.0, m["monthly_usage_usd"])
	m = map[string]any{"starts_at": start, "expires_at": now.Add(45 * 24 * time.Hour), "status": "active", "monthly_window_start": start, "monthly_usage_usd": 9.0}
	normalizeSubscriptionItem(m, now)
	require.Equal(t, 0.0, m["monthly_usage_usd"])
	require.Nil(t, m["monthly_window_start"])
}

func TestReaderUnknownToolAndInvalidNumericIDDoNotOpenDB(t *testing.T) {
	db, m := readerMock(t)
	for _, req := range []QueryRequest{{Tool: "xy2api.invalid", Actor: testReaderActor()}, {Tool: "xy2api.requests.get_detail", Actor: testReaderActor(), Input: QueryInput{ErrorRowID: -1}}} {
		_, err := NewQueryReader(&ReaderDB{db}).Query(context.Background(), req)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	require.NoError(t, m.ExpectationsWereMet())
}

func TestReaderStatementTimeoutIsStructuredTimeout(t *testing.T) {
	require.ErrorIs(t, readerError(context.Background(), &pq.Error{Code: "57014", Message: "canary SQL statement timeout"}), context.DeadlineExceeded)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, readerError(ctx, &pq.Error{Code: "57014"}), context.Canceled)
}

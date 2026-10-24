//go:build unit

package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCacheNorthstarRepositoryFailureDoesNotPublishWatermark(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	repo := NewCacheNorthstarRepository(db)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO cache_northstar_rollup_state").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("CREATE TEMP TABLE cache_northstar_work").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM cache_northstar_hourly").WillReturnResult(sqlmock.NewResult(0, 1))
	failure := errors.New("synthetic insert failure")
	mock.ExpectExec("INSERT INTO cache_northstar_hourly").WillReturnError(failure)
	mock.ExpectRollback()
	now := time.Now().UTC().Truncate(time.Hour)
	require.ErrorIs(t, repo.AggregateCacheNorthstar(context.Background(), now.Add(-time.Hour), now, service.CacheNorthstarPolicy{PolicyVersion: "v"}, now, false), failure)
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}

func TestCacheNorthstarRepositoryEmptyAndInvalidRequests(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	repo := NewCacheNorthstarRepository(db)
	require.NoError(t, repo.SaveCacheTraceHours(context.Background(), nil))
	now := time.Now().UTC().Truncate(time.Hour)
	for _, end := range []time.Time{now, now.Add(30 * time.Minute), now.Add(25 * time.Hour)} {
		require.Error(t, repo.AggregateCacheNorthstar(context.Background(), now, end, service.CacheNorthstarPolicy{}, now, false))
	}
	mock.ExpectQuery("SELECT policy_version").WillReturnError(sql.ErrNoRows)
	state, err := repo.GetCacheNorthstarState(context.Background())
	require.NoError(t, err)
	require.Nil(t, state)
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}

func TestCacheNorthstarRepositoryCanonicalUTCTimes(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	repo := NewCacheNorthstarRepository(db)
	at := time.Date(2026, 10, 24, 13, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	mock.ExpectQuery("SELECT policy_version").WillReturnRows(sqlmock.NewRows([]string{"version", "spec", "changed", "until", "from", "day"}).
		AddRow("v", []byte("{}"), at, at.Add(time.Hour), at, at))
	state, err := repo.GetCacheNorthstarState(context.Background())
	require.NoError(t, err)
	for _, value := range []time.Time{state.PolicyChangedAt, state.AggregatedUntil, state.BackfilledFrom, *state.ReconciledDay} {
		require.Same(t, time.UTC, value.Location())
	}
	columns := []string{"bucket", "platform", "group", "group_label", "account", "account_label", "host", "client", "protocol", "model", "band", "media"}
	values := []driver.Value{at, "openai", int64(1), "pool", int64(2), "relay", "relay.example", "codex_cli_rs", "responses_http", "gpt-5.4", int64(2048), false}
	for i := 0; i < 17; i++ {
		columns = append(columns, fmt.Sprintf("metric%d", i))
		values = append(values, int64(1))
	}
	mock.ExpectQuery("SELECT bucket_start,platform").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
	mock.ExpectQuery("SELECT bucket_start,").WillReturnRows(sqlmock.NewRows([]string{"bucket", "media", "zero", "probe"}).AddRow(at, 0, 0, 0))
	data, err := repo.ReadCacheNorthstar(context.Background(), "v", at, at.Add(time.Hour), "all")
	require.NoError(t, err)
	require.Equal(t, at.UTC(), data.Rows[0].BucketStart)
	require.Equal(t, at.UTC(), data.Hours[0].BucketStart)
	mock.ExpectQuery("SELECT at,kind,label").WillReturnRows(sqlmock.NewRows([]string{"at", "kind", "label", "version", "cold", "policy"}).
		AddRow(at, "release", "test", "test", at.Add(time.Hour), ""))
	events, err := repo.ListCachePanelEvents(context.Background(), at, at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, at.UTC(), events[0].At)
	require.Equal(t, at.UTC().Add(time.Hour), *events[0].ColdUntil)
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}

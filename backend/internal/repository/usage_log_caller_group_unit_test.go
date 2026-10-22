//go:build unit

package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// Users see their usage by the group the request was made for: a row an
// availability fallback group served counts for its origin group. Admin
// queries keep the serving group (fallback-billing design §2.3).

var errStopQuery = errors.New("stop after the query was built")

func TestUsageExportWhereMatchesTheCallerGroup(t *testing.T) {
	where, args := usageExportWhere(usagestats.UsageLogFilters{UserID: 1, GroupID: 10, CallerGroup: true})
	require.Contains(t, where, "COALESCE(origin_group_id, group_id) = $2")
	require.Equal(t, []any{int64(1), int64(10)}, args)

	admin, _ := usageExportWhere(usagestats.UsageLogFilters{UserID: 1, GroupID: 10})
	require.Contains(t, admin, "group_id = $2")
	require.NotContains(t, admin, "origin_group_id")
}

func TestUsageStatsMatchTheCallerGroup(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(origin_group_id, group_id) = $2")).WillReturnError(errStopQuery)

	_, err := repo.GetStatsWithFilters(context.Background(), usagestats.UsageLogFilters{UserID: 1, GroupID: 10, CallerGroup: true})
	require.ErrorIs(t, err, errStopQuery)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageTrendAndModelStatsMatchTheCallerGroup(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start, end := time.Now().Add(-time.Hour), time.Now()
	filters := usagestats.UsageLogFilters{UserID: 1, GroupID: 10, CallerGroup: true}

	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(origin_group_id, group_id) = $")).WillReturnError(errStopQuery)
	_, err := repo.GetUsageTrendWithUsageFilters(context.Background(), start, end, "day", filters)
	require.ErrorIs(t, err, errStopQuery)

	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(origin_group_id, group_id) = $")).WillReturnError(errStopQuery)
	_, err = repo.GetModelStatsWithUsageFiltersBySource(context.Background(), start, end, filters, usagestats.ModelSourceRequested)
	require.ErrorIs(t, err, errStopQuery)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupStatsForCallersUseTheCallerGroup(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start, end := time.Now().Add(-time.Hour), time.Now()

	callerGroup := regexp.QuoteMeta("COALESCE(ul.origin_group_id, ul.group_id)")
	mock.ExpectQuery(`SELECT\s+COALESCE\(` + callerGroup + `, 0\) as group_id[\s\S]*LEFT JOIN groups g ON g.id = ` + callerGroup + `[\s\S]*GROUP BY ` + callerGroup).WillReturnError(errStopQuery)
	_, err := repo.GetGroupStatsWithUsageFilters(context.Background(), start, end, usagestats.UsageLogFilters{UserID: 1, CallerGroup: true})
	require.ErrorIs(t, err, errStopQuery)

	mock.ExpectQuery(regexp.QuoteMeta("GROUP BY ul.group_id, g.name")).WillReturnError(errStopQuery)
	_, err = repo.GetGroupStatsWithUsageFilters(context.Background(), start, end, usagestats.UsageLogFilters{UserID: 1})
	require.ErrorIs(t, err, errStopQuery, "admin views keep the serving group")
	require.NoError(t, mock.ExpectationsWereMet())
}

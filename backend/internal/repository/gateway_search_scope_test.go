//go:build unit

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestN12SearchExcludedFromTextLast60SQL(t *testing.T) {
	// Check both billed and unbilled arms; neither may admit the new search scope.
	arms := strings.Split(groupLast60Query, "UNION ALL")
	require.Len(t, arms, 2)
	for _, arm := range arms {
		require.Contains(t, arm, "AND (request_scope IS NULL OR request_scope = 'text')")
		require.NotContains(t, arm, "'search'")
	}
	require.Contains(t, schedAdminLatencySQL, "AND (request_scope IS NULL OR request_scope = 'text')", "the overview latency query must use the same text scope even when non-text rows have first-output times")
	require.Equal(t, "search", gatewayScopeSQL(resilience.GatewayRequestScope("search")), "search must persist as a non-NULL scope")
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()
	at := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery(groupLast60Query).WithArgs("{42}", at.Add(-time.Hour), at).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "requests", "p50", "failed_rate", "cache_ratio"}).AddRow(42, 3, nil, 0.0, nil))
	repo := newUsageLogRepositoryWithSQL(nil, db)
	got, err := repo.GroupLast60(context.Background(), []int64{42}, at)
	require.NoError(t, err)
	require.EqualValues(t, 3, got[42].Requests)
	require.NoError(t, mock.ExpectationsWereMet())
}

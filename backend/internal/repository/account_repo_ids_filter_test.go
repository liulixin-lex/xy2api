package repository

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountIDsFilterAppliesBeforeCountAndPagination(t *testing.T) {
	var queries []string
	matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		queries = append(queries, actual)
		return sqlmock.QueryMatcherRegexp.Match(expected, actual)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)
	ctx := service.WithAccountIDsFilter(context.Background(), []int64{2, 3})
	ctx = service.WithSchedQualityAccountFilter(ctx, []int64{3, 4})
	mock.ExpectQuery(`SELECT COUNT`).WithArgs("openai", "apikey", "inactive", "%needle%", int64(7), "training_off", int64(2), int64(3), int64(3), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT "accounts"`).WithArgs("openai", "apikey", "inactive", "%needle%", int64(7), "training_off", int64(2), int64(3), int64(3), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	rows, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 2, PageSize: 1}, "openai", "apikey", "inactive", "needle", 7, "training_off")
	require.NoError(t, err)
	require.Empty(t, rows)
	require.EqualValues(t, 1, page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Len(t, queries, 2)
	for _, query := range queries {
		require.Contains(t, query, `"accounts"."id" IN ($7, $8)`)
		require.Contains(t, query, `AND "accounts"."id" IN ($9, $10)`)
	}
	require.NotContains(t, queries[0], "LIMIT")
	require.True(t, strings.Contains(queries[1], "LIMIT 1 OFFSET 1"), queries[1])
}

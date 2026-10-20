//go:build unit

package repository

import (
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func accountListOrderSQL(t *testing.T, sortBy, sortOrder string) string {
	t.Helper()
	selector := entsql.Dialect(dialect.Postgres).Select("*").From(entsql.Table("accounts"))
	for _, order := range accountListOrder(pagination.PaginationParams{SortBy: sortBy, SortOrder: sortOrder}) {
		order(selector)
	}
	query, _ := selector.Query()
	return query
}

// The admin account list defaults to "last used, newest first"; accounts that were never used
// (last_used_at NULL) must not be pushed to the top by PostgreSQL's default NULLS FIRST for DESC.
func TestAccountListOrderPutsNeverUsedAccountsLast(t *testing.T) {
	require.Equal(t, `SELECT * FROM "accounts" ORDER BY "accounts"."last_used_at" DESC NULLS LAST, "accounts"."id" DESC`,
		accountListOrderSQL(t, "last_used_at", "desc"))
	require.Equal(t, `SELECT * FROM "accounts" ORDER BY "accounts"."last_used_at" NULLS LAST, "accounts"."id" ASC`,
		accountListOrderSQL(t, "last_used_at", "asc"))
	require.Equal(t, `SELECT * FROM "accounts" ORDER BY "accounts"."name" ASC, "accounts"."id" ASC`,
		accountListOrderSQL(t, "name", "asc"))
}

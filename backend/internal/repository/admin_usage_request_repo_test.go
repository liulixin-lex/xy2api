package repository

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestAdminRequestOrderByDefaultsAcrossSourcesByCreatedAt(t *testing.T) {
	require.Equal(t, "created_at DESC, id DESC, source ASC", adminRequestOrderBy(pagination.PaginationParams{}))
	require.Equal(t, "created_at ASC, id ASC, source ASC", adminRequestOrderBy(pagination.PaginationParams{SortBy: "created_at", SortOrder: "asc"}))
	require.Equal(t, "id DESC, source ASC", adminRequestOrderBy(pagination.PaginationParams{SortBy: "id"}))
}

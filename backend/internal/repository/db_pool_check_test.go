//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestDBConnectionBudget(t *testing.T) {
	// The pre-launch instance: 256 against 100.
	b := dbConnectionBudget{MaxConnections: 100, SuperuserReserve: 3}
	require.Equal(t, 97, b.available())
	require.True(t, b.poolExceeds(256))
	require.True(t, b.poolExceeds(50), "two pools of 50 during a blue-green overlap exceed 97")
	require.False(t, b.poolExceeds(40))
	// PostgreSQL 16+ reserved_connections and other users count too.
	b = dbConnectionBudget{MaxConnections: 100, SuperuserReserve: 3, Reserved: 5, OtherUsers: 12}
	require.Equal(t, 80, b.available())
	require.True(t, b.poolExceeds(41))
	require.False(t, b.poolExceeds(40))
	require.False(t, b.poolExceeds(0))
}

func TestWarnDBPoolAboveServerLimitSkipsOnQueryFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT current_setting").WillReturnError(errors.New("permission denied"))
	warnDBPoolAboveServerLimit(context.Background(), db, 256) // must not panic or block
	require.NoError(t, mock.ExpectationsWereMet())

	db2, mock2, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db2.Close() }()
	mock2.ExpectQuery("SELECT current_setting").WillReturnRows(
		sqlmock.NewRows([]string{"a", "b", "c", "d"}).AddRow(100, 3, 0, 0))
	warnDBPoolAboveServerLimit(context.Background(), db2, 256)
	require.NoError(t, mock2.ExpectationsWereMet())
}

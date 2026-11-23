//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollabLocalOutcomeUnbilledInsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := newUsageLogRepositoryWithSQL(nil, db)
	for _, result := range []int16{resilience.RequestLocalEnded, resilience.RequestLocalUndeliverable} {
		mock.ExpectExec("INSERT INTO gateway_unbilled_requests").WillReturnResult(sqlmock.NewResult(1, 1))
		require.NoError(t, repo.CreateUnbilledRequest(context.Background(), &service.GatewayUnbilledRequest{RequestID: "synthetic-request", CreatedAt: time.Now(), Result: result}))
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

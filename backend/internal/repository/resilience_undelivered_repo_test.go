package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUndeliveredRepositoryGatewayIdentityAndLegacyNull(t *testing.T) {
	for _, gatewayID := range []*string{nil, new(uuid.NewString())} {
		db, mock := newSQLMock(t)
		repo := NewUndeliveredRepository(db)
		row := &service.UndeliveredRequest{RequestID: "client-reused", ClientRequestID: "client", GatewayRequestID: gatewayID,
			Platform: "gemini", Model: "gemini-fixture", InboundEndpoint: "/v1beta/models:generateContent", DisconnectStage: "upload"}
		mock.ExpectExec("INSERT INTO gateway_undelivered_requests.*billed, gateway_request_id").
			WithArgs("client-reused", "client", nil, nil, nil, nil, "gemini", "gemini-fixture", "/v1beta/models:generateContent", "upload", int64(0), nil, float64(0), false, gatewayID).
			WillReturnResult(sqlmock.NewResult(1, 1))
		require.NoError(t, repo.InsertUndelivered(context.Background(), row))
		mock.ExpectQuery("SELECT COUNT.*FROM gateway_undelivered_requests").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery("SELECT id, request_id.*created_at, gateway_request_id.*FROM gateway_undelivered_requests").WithArgs(50, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "request_id", "client_request_id", "user_id", "api_key_id", "group_id", "account_id", "platform", "model", "inbound_endpoint", "disconnect_stage", "duration_ms", "usage", "cost", "billed", "created_at", "gateway_request_id"}).
				AddRow(1, "client-reused", "client", nil, nil, nil, nil, "gemini", "gemini-fixture", "/v1beta/models:generateContent", "upload", 0, "", 0, false, time.Now(), gatewayID))
		rows, page, err := repo.ListUndelivered(context.Background(), service.UndeliveredFilter{}, pagination.PaginationParams{})
		require.NoError(t, err)
		require.EqualValues(t, 1, page.Total)
		require.Len(t, rows, 1)
		require.Equal(t, gatewayID, rows[0].GatewayRequestID)
		require.Equal(t, row.RequestID, rows[0].RequestID)
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

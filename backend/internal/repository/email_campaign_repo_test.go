//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestEmailCampaignResolveBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,lower\(btrim\(email\)\) FROM users WHERE lower\(btrim\(email\)\)=ANY\(\$1::text\[\]\) OR id=ANY\(\$2::bigint\[\]\)`).
		WithArgs(`{"same@example.com","missing@example.com"}`, `{3,999}`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(1, "same@example.com").AddRow(2, "same@example.com").AddRow(3, "other@example.com"))
	mock.ExpectRollback()
	tx, err := db.Begin()
	require.NoError(t, err)
	ids, missing, err := campaignResolve(context.Background(), tx, []string{" SAME@Example.com ", "3", "missing@example.com", "999", "  ", "\t", "invalid"})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3}, ids)
	require.Equal(t, []string{"missing@example.com", "999", "invalid"}, missing)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestEmailCampaignSuppressionsBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,lower\(btrim\(email\)\) FROM users`).WithArgs(`{"same@example.com","new@example.com"}`, `{3}`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(1, "same@example.com").AddRow(2, "same@example.com").AddRow(3, "other@example.com"))
	for _, tc := range []struct {
		email string
		uid   any
	}{{"same@example.com", int64(1)}, {"other@example.com", int64(3)}, {"new@example.com", nil}} {
		mock.ExpectExec(`INSERT INTO email_marketing_suppressions`).WithArgs(tc.email, tc.uid, "note", int64(9)).WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectCommit()
	out, err := NewEmailCampaignRepository(db, nil).AddSuppressions(context.Background(), []string{"SAME@example.com", "3", "new@example.com", "  "}, "note", 9)
	require.NoError(t, err)
	require.EqualValues(t, 3, out.Added)
	require.Empty(t, out.NotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestEmailCampaignFinishFailureCounts(t *testing.T) {
	for _, tc := range []struct {
		name, kind, next string
		count            bool
	}{{"permanent RCPT", "recipient", "failed", false}, {"temporary RCPT", "recipient", "failed", true}, {"shutdown", "retry", "pending", false}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT status FROM email_campaigns`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("sending"))
			mock.ExpectExec(`UPDATE email_campaign_recipients SET status=\$4::text`).WithArgs(int64(1), int64(2), int64(3), tc.next, false, "fixture").WillReturnResult(sqlmock.NewResult(0, 1))
			if tc.count {
				mock.ExpectQuery(`UPDATE email_campaigns SET consecutive_failures`).WithArgs(int64(1), "fixture", false, int64(1)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("sending"))
			}
			mock.ExpectCommit()
			err = NewEmailCampaignRepository(db, nil).Finish(context.Background(), service.EmailCampaignRecipient{CampaignID: 1, UserID: 2, ClaimVersion: 3}, service.EmailCampaignOutcome{Kind: tc.kind, Error: "fixture", CountFailure: tc.count})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

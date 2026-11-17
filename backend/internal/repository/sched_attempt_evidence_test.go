//go:build unit || integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

// Shared by the isolated local-PG test and CI's migrated integration database.
func assertSchedAttemptV2Evidence(t *testing.T, db *sql.DB) {
	t.Helper()
	requestID := uuid.NewString()
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM account_attempt_samples WHERE request_id=$1", requestID) })
	// Keep the global ledger's other fixtures outside our page and finish ranges.
	start := time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
	legacyID := uuid.NewString()
	_, err := db.Exec(`INSERT INTO account_attempt_samples
        (attempt_id, started_at, finished_at, account_id, group_id, model, request_class, stream, warm, outcome, session_key, request_id, inserted_at)
        VALUES ($1,$2,$3,1,1,'model',1,true,false,1,$4,$4,$5)`,
		legacyID, start, start.Add(time.Second), requestID, start.Add(2*time.Second))
	require.NoError(t, err)
	var version int
	require.NoError(t, db.QueryRow("SELECT capture_version FROM account_attempt_samples WHERE attempt_id=$1", legacyID).Scan(&version))
	require.Equal(t, 1, version)
	ctx := context.Background()
	repo := &schedAttemptRepository{db: db}
	// A page containing only v1 data must not emit evidence or advance the cursor.
	rows, cursor, err := repo.LoadInsertedAfter(ctx, start.Add(-time.Second), 1)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.True(t, cursor.IsZero())
	finished, err := repo.LoadFinishedSince(ctx, start)
	require.NoError(t, err)
	require.Empty(t, finished)
	current := schedquality.AttemptSample{AttemptID: uuid.NewString(), StartedAt: start.Add(20 * time.Second), GroupID: 1, Model: "model", SessionKey: requestID}
	previous, err := repo.LoadPreviousAttempts(ctx, []schedquality.AttemptSample{current})
	require.NoError(t, err)
	require.Nil(t, previous[current.AttemptID])

	event, valid, answer, status, output := 10, 20, 30, 503, 42
	sample := schedquality.AttemptSample{
		AttemptID: uuid.NewString(), StartedAt: start.Add(3 * time.Second), FinishedAt: start.Add(4 * time.Second),
		AccountID: 1, GroupID: 1, Model: "model", Stream: true, SessionKey: requestID, RequestID: requestID,
		TTFOMs: &valid, FirstEventMs: &event, FirstAnswerMs: &answer, Outcome: schedquality.OutcomeAccountFault,
		Class: schedquality.ClassUpstream, HTTPStatus: &status, StatusOrigin: schedquality.OriginUpstream, OutputBytes: &output,
	}
	nullable := sample
	nullable.AttemptID, nullable.StartedAt, nullable.FinishedAt = uuid.NewString(), start.Add(5*time.Second), start.Add(6*time.Second)
	nullable.TTFOMs, nullable.FirstEventMs, nullable.FirstAnswerMs = nil, nil, nil
	nullable.Class, nullable.HTTPStatus, nullable.StatusOrigin, nullable.OutputBytes = schedquality.ClassNone, nil, schedquality.OriginNone, nil
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{sample, nullable}))
	require.NoError(t, db.QueryRow("SELECT capture_version FROM account_attempt_samples WHERE attempt_id=$1", sample.AttemptID).Scan(&version))
	require.Equal(t, 2, version)
	_, err = db.Exec("UPDATE account_attempt_samples SET inserted_at=$1 WHERE attempt_id=$2", start.Add(7*time.Second), sample.AttemptID)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE account_attempt_samples SET inserted_at=$1 WHERE attempt_id=$2", start.Add(8*time.Second), nullable.AttemptID)
	require.NoError(t, err)
	// Put v1 immediately before current: it must not mask the eligible predecessor.
	_, err = db.Exec("UPDATE account_attempt_samples SET started_at=$1 WHERE attempt_id=$2", start.Add(19*time.Second), legacyID)
	require.NoError(t, err)

	rows, cursor, err = repo.LoadInsertedAfter(ctx, start.Add(-time.Second), 1)
	require.NoError(t, err)
	require.Len(t, rows, 1, "v1 must not consume the page limit and stall ingestion")
	require.Equal(t, sample.AttemptID, rows[0].AttemptID)
	require.True(t, cursor.Equal(start.Add(7*time.Second)))
	require.Equal(t, sample.FirstEventMs, rows[0].FirstEventMs)
	require.Equal(t, sample.TTFOMs, rows[0].TTFOMs)
	require.Equal(t, sample.FirstAnswerMs, rows[0].FirstAnswerMs)
	require.Equal(t, sample.Class, rows[0].Class)
	require.Equal(t, sample.HTTPStatus, rows[0].HTTPStatus)
	require.Equal(t, sample.StatusOrigin, rows[0].StatusOrigin)
	require.Equal(t, sample.OutputBytes, rows[0].OutputBytes)
	finished, err = repo.LoadFinishedSince(ctx, start)
	require.NoError(t, err)
	require.Len(t, finished, 2)
	require.Equal(t, nullable.AttemptID, finished[1].AttemptID)
	require.Nil(t, finished[1].FirstEventMs)
	require.Nil(t, finished[1].FirstAnswerMs)
	require.Nil(t, finished[1].HTTPStatus)
	require.Nil(t, finished[1].OutputBytes)
	require.Equal(t, schedquality.ClassNone, finished[1].Class)
	require.Equal(t, schedquality.OriginNone, finished[1].StatusOrigin)
	previous, err = repo.LoadPreviousAttempts(ctx, []schedquality.AttemptSample{current})
	require.NoError(t, err)
	require.Equal(t, nullable.AttemptID, previous[current.AttemptID].AttemptID)
}

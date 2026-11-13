//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedPreviousBatchKeepsImmediateFailedAndStrictOrder(t *testing.T) {
	ctx := context.Background()
	repo := &schedAttemptRepository{db: integrationDB}
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := uuid.NewString()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM account_attempt_samples WHERE session_key=$1", session)
	})
	base := schedAttemptTestRow(8612001, now.Add(-6*time.Hour))
	base.SessionKey, base.Outcome = session, schedquality.OutcomeOK
	base.FinishedAt = now.Add(-time.Hour)
	failed := base
	failed.AttemptID = "ffffffff-ffff-4fff-bfff-ffffffffffff"
	failed.AccountID++
	failed.Outcome = schedquality.OutcomeAccountFault
	failed.FinishedAt = now.Add(time.Hour) // Not completed at the current send.
	equal := base
	equal.AttemptID, equal.StartedAt = uuid.NewString(), now
	wrongModel := base
	wrongModel.AttemptID, wrongModel.Model, wrongModel.StartedAt = uuid.NewString(), "other", now.Add(-time.Minute)
	old := base
	old.AttemptID, old.StartedAt = uuid.NewString(), now.Add(-8*24*time.Hour)
	old.GroupID++
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{base, failed, equal, wrongModel, old}))
	current := base
	current.AttemptID, current.StartedAt = uuid.NewString(), now
	empty, expired := current, current
	empty.AttemptID, empty.SessionKey = uuid.NewString(), ""
	expired.AttemptID, expired.GroupID = uuid.NewString(), old.GroupID
	previous, err := repo.LoadPreviousAttempts(ctx, []schedquality.AttemptSample{current, empty, expired})
	require.NoError(t, err)
	require.Len(t, previous, 3)
	require.Equal(t, failed.AttemptID, previous[current.AttemptID].AttemptID)
	require.Equal(t, schedquality.OutcomeAccountFault, previous[current.AttemptID].Outcome)
	require.True(t, previous[current.AttemptID].FinishedAt.After(now))
	require.Nil(t, previous[empty.AttemptID])
	require.Nil(t, previous[expired.AttemptID])
	finished, err := repo.LoadFinishedSince(ctx, base.FinishedAt)
	require.NoError(t, err)
	found := false
	for _, row := range finished {
		found = found || row.AttemptID == base.AttemptID
	}
	require.True(t, found, "inclusive finish-time loading must retain a long-running attempt")
}

func TestSchedAttemptAllTerminalTokensRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := &schedAttemptRepository{db: integrationDB}
	requestID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM account_attempt_samples WHERE request_id=$1", requestID)
	})
	start := time.Now().UTC().Truncate(time.Microsecond)
	input, read, creation, prompt, ttfo := 0, 80, 20, 100, 12
	var batch []schedquality.AttemptSample
	for _, outcome := range []schedquality.Outcome{1, 2, 3, 5} {
		row := schedAttemptTestRow(8612002, start)
		row.RequestID, row.Outcome = requestID, outcome
		row.InputTokens, row.CacheReadTokens, row.CacheCreationTokens, row.PromptTokens = &input, &read, &creation, &prompt
		row.TTFOMs = &ttfo
		row.Detail = "AUTH" // Cancellation must be read from code 5, not this string.
		row.Class, row.Warm, row.PrevPromptTokens = schedquality.ClassXHeavy, true, &prompt
		batch = append(batch, row)
	}
	require.NoError(t, repo.InsertBatch(ctx, batch))
	rows, err := repo.read(ctx, "WHERE request_id=$1", requestID)
	require.NoError(t, err)
	require.Len(t, rows, 4)
	for _, row := range rows {
		require.Equal(t, row.Outcome == 5, row.ClientCanceled)
		require.Equal(t, 0, *row.InputTokens)
		require.Equal(t, 80, *row.CacheReadTokens)
		require.Equal(t, 20, *row.CacheCreationTokens)
		require.Equal(t, 12, *row.TTFOMs)
		require.EqualValues(t, 1, row.Class)
		require.False(t, row.Warm)
		require.Nil(t, row.PrevPromptTokens)
	}
}

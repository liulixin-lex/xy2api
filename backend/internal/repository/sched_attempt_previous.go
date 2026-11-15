package repository

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

func schedAttemptScanTargets(row *schedquality.AttemptSample) []any {
	return []any{&row.AttemptID, &row.StartedAt, &row.FinishedAt, &row.AccountID, &row.GroupID,
		&row.Model, &row.Stream, &row.TTFOMs, &row.CensoredMs, &row.Outcome,
		&row.Detail, &row.PromptTokens, &row.CacheReadTokens, &row.SessionKey,
		&row.RequestID, &row.InsertedAt, &row.InputTokens, &row.CacheCreationTokens}
}

func (r *schedAttemptRepository) LoadFinishedSince(ctx context.Context, since time.Time) ([]schedquality.AttemptSample, error) {
	return r.read(ctx, `WHERE finished_at >= $1 ORDER BY finished_at, attempt_id`, since)
}

// LoadPreviousAttempts performs one indexed lookup per requested key inside one
// database round trip. A failed or unfinished predecessor is still returned;
// the engine decides whether it establishes continuity.
func (r *schedAttemptRepository) LoadPreviousAttempts(ctx context.Context, current []schedquality.AttemptSample) (map[string]*schedquality.AttemptSample, error) {
	result := make(map[string]*schedquality.AttemptSample, len(current))
	type lookup struct {
		AttemptID string    `json:"attempt_id"`
		GroupID   int64     `json:"group_id"`
		Session   string    `json:"session_key"`
		Model     string    `json:"model"`
		StartedAt time.Time `json:"started_at"`
	}
	inputs := make([]lookup, 0, len(current))
	for _, row := range current {
		result[row.AttemptID] = nil
		if row.SessionKey != "" {
			inputs = append(inputs, lookup{row.AttemptID, row.GroupID, row.SessionKey, row.Model, row.StartedAt})
		}
	}
	if len(inputs) == 0 {
		return result, nil
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, schedquality.LedgerReadTimeout)
	defer cancel()
	cols := strings.FieldsFunc(schedAttemptColumns, func(r rune) bool { return r == ',' || r == '\n' || r == '\t' || r == ' ' })
	query := `SELECT c.attempt_id, p.` + strings.Join(cols, ", p.") + `
		FROM jsonb_to_recordset($1::jsonb) AS c(attempt_id text, group_id bigint, session_key text, model text, started_at timestamptz)
		JOIN LATERAL (
			SELECT ` + schedAttemptColumns + ` FROM account_attempt_samples s
			WHERE s.group_id=c.group_id AND s.session_key=c.session_key AND s.model=c.model
			AND s.started_at<c.started_at AND s.started_at >= $2
			ORDER BY s.started_at DESC, s.attempt_id DESC LIMIT 1
		) p ON true`
	rows, err := r.db.QueryContext(ctx, query, string(data), time.Now().Add(-7*24*time.Hour))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var row schedquality.AttemptSample
		if err := rows.Scan(append([]any{&id}, schedAttemptScanTargets(&row)...)...); err != nil {
			return nil, err
		}
		row.ClientCanceled = row.Outcome == schedquality.Outcome(5)
		result[id] = &row
	}
	return result, rows.Err()
}

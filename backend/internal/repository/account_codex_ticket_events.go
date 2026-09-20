package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

func (r *accountRepository) AppendCodexTicketEvents(ctx context.Context, events []service.CodexTicketTraceEvent) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	for _, e := range events {
		detail, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		// Aggregate only newly inserted event IDs, including retried flushes.
		count := int64(1)
		if e.Detail.Count > 0 {
			count = e.Detail.Count
		}
		_, err = c.ExecContext(ctx, `WITH inserted AS (
   INSERT INTO codex_ticket_events(event_id,account_id,model,at,stage,outcome,detail)
   SELECT $1,$2,$3,$4,$5,$6,$7::jsonb WHERE EXISTS(SELECT 1 FROM accounts WHERE id=$2)
   ON CONFLICT(event_id) DO NOTHING RETURNING account_id,model,at,stage,outcome
  ) INSERT INTO codex_ticket_hourly(account_id,model,hour,stage,outcome,count)
   SELECT account_id,model,date_trunc('hour',at),stage,outcome,$8 FROM inserted
   ON CONFLICT(account_id,model,hour,stage,outcome) DO UPDATE SET count=codex_ticket_hourly.count+EXCLUDED.count`, e.EventID, e.AccountID, e.Model, e.At, e.Stage, e.Outcome, string(detail), count)
		if err != nil {
			return err
		}
	}
	if _, err = c.ExecContext(ctx, `DELETE FROM codex_ticket_events WHERE at < now()-interval '72 hours'`); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, `DELETE FROM codex_ticket_hourly WHERE hour < now()-interval '30 days'`); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *accountRepository) ListCodexTicketEvents(ctx context.Context, id int64, model string, before int64, limit int) ([]service.CodexTicketTraceEvent, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := r.client.QueryContext(ctx, `SELECT id,event_id,account_id,model,at,stage,outcome,detail FROM codex_ticket_events WHERE account_id=$1 AND ($2='' OR model=$2) AND ($3::bigint=0 OR id<$3) AND at>$4 ORDER BY id DESC LIMIT $5`, id, model, before, time.Now().Add(-72*time.Hour), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []service.CodexTicketTraceEvent{}
	for rows.Next() {
		var e service.CodexTicketTraceEvent
		var detail []byte
		if err = rows.Scan(&e.ID, &e.EventID, &e.AccountID, &e.Model, &e.At, &e.Stage, &e.Outcome, &detail); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(detail, &e.Detail); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

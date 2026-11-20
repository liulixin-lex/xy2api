package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Sample the insertion clock before reading pages, so advancing a watermark
// never assumes the application and PostgreSQL clocks are synchronized.
func (r *schedAttemptRepository) PerfDatabaseTime(ctx context.Context) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, q.LedgerReadTimeout)
	defer cancel()
	var at time.Time
	err := r.db.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&at)
	return at, err
}

func (r *schedAttemptRepository) LoadPerfConfig(ctx context.Context) (map[int64]service.SchedPolicyRecord, map[int64]time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, q.LedgerReadTimeout)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	policies, err := loadSchedPolicies(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	resets, err := loadSchedQualityResets(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	return policies, resets, tx.Commit()
}

const perfFinishedPage = `SELECT attempt_id, finished_at FROM account_attempt_samples
 WHERE capture_version>=2 AND finished_at >= $1 AND finished_at < $2 AND inserted_at < $2
 AND (finished_at,attempt_id) > ($3,$4::uuid) ORDER BY finished_at,attempt_id LIMIT $5`

// An attempt becomes visible when both clocks pass. Partition by the later
// clock so each branch can use its own index, with no duplicate at equality.
const perfVisiblePage = `SELECT attempt_id, visible_at FROM (
 SELECT attempt_id, finished_at AS visible_at FROM account_attempt_samples
 WHERE capture_version>=2 AND finished_at >= $1 AND finished_at < $2 AND inserted_at <= finished_at
 AND (finished_at,attempt_id) > ($3,$4::uuid)
 UNION ALL
 SELECT attempt_id, inserted_at AS visible_at FROM account_attempt_samples
 WHERE capture_version>=2 AND inserted_at >= $1 AND inserted_at < $2 AND finished_at < inserted_at
 AND (inserted_at,attempt_id) > ($3,$4::uuid)
 ) newly_visible ORDER BY visible_at,attempt_id LIMIT $5`

func (r *schedAttemptRepository) LoadPerfPage(ctx context.Context, from, at time.Time, after service.SchedAttemptCursor, incremental bool, limit int) ([]service.SchedPerfFact, service.SchedAttemptCursor, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, q.LedgerReadTimeout)
	defer cancel()
	query := perfFinishedPage
	if incremental {
		query = perfVisiblePage
	}
	if after.At.IsZero() {
		after = service.SchedAttemptCursor{At: from.Add(-time.Microsecond), AttemptID: "00000000-0000-0000-0000-000000000000"}
	}
	rows, err := r.db.QueryContext(ctx, query, from, at, after.At, after.AttemptID, limit)
	if err != nil {
		return nil, after, false, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	next := after
	for rows.Next() {
		if err = rows.Scan(&next.AttemptID, &next.At); err != nil {
			return nil, after, false, err
		}
		ids = append(ids, next.AttemptID)
	}
	if err = rows.Err(); err != nil {
		return nil, after, false, err
	}
	if err = rows.Close(); err != nil {
		return nil, after, false, err
	}
	if len(ids) == 0 {
		return nil, after, true, nil
	}
	facts, err := r.perfFacts(ctx, ids, at, incremental)
	if err != nil {
		return nil, after, false, err
	}
	return facts, next, len(ids) < limit, nil
}

// A late attempt can change the first or delivered account of its request and
// the predecessor of the next session dispatch (including tied start times).
const perfAffected = `
 UNION SELECT s.attempt_id FROM page p JOIN account_attempt_samples s
   ON p.request_id<>'' AND s.request_id=p.request_id
 UNION SELECT successor.attempt_id FROM page p CROSS JOIN LATERAL (
   SELECT s.attempt_id FROM account_attempt_samples s
   WHERE p.session_key<>'' AND s.group_id=p.group_id AND s.session_key=p.session_key
     AND s.model=p.model AND s.capture_version>=2 AND s.started_at>p.started_at
     AND s.finished_at<$2 AND s.inserted_at<$2
   ORDER BY s.started_at FETCH FIRST 1 ROW WITH TIES
 ) successor`

func (r *schedAttemptRepository) perfFacts(ctx context.Context, ids []string, at time.Time, refresh bool) ([]service.SchedPerfFact, error) {
	affected := ""
	if refresh {
		affected = perfAffected
	}
	query := `WITH page AS (SELECT attempt_id,request_id,group_id,session_key,model,started_at
 FROM account_attempt_samples WHERE attempt_id=ANY($1::uuid[])),
 affected AS (SELECT attempt_id FROM page ` + affected + `)
 SELECT s.attempt_id,s.started_at,s.finished_at,s.account_id,s.group_id,s.model,s.session_key,
 s.stream,s.warm,s.full_tier_first_output_timeout,s.outcome,coalesce(s.ttfo_ms,0),coalesce(s.censored_ms,-1),
 s.input_tokens,s.cache_read_tokens,s.cache_creation_tokens,
 s.request_id<>'' AND NOT EXISTS (SELECT 1 FROM account_attempt_samples x WHERE x.request_id=s.request_id
  AND (x.started_at,x.attempt_id)<(s.started_at,s.attempt_id) AND x.finished_at<$2 AND x.inserted_at<$2),
 s.request_id<>'' AND s.outcome=1 AND NOT EXISTS (SELECT 1 FROM account_attempt_samples x WHERE x.request_id=s.request_id
  AND x.outcome=1 AND (x.finished_at,x.attempt_id)>(s.finished_at,s.attempt_id) AND x.finished_at<$2 AND x.inserted_at<$2),
 p.account_id,p.finished_at,p.outcome
 FROM affected JOIN account_attempt_samples s USING(attempt_id)
 LEFT JOIN LATERAL (
  SELECT x.account_id,x.finished_at,x.outcome FROM account_attempt_samples x
  WHERE s.session_key<>'' AND x.group_id=s.group_id AND x.session_key=s.session_key AND x.model=s.model
   AND x.capture_version>=2 AND x.started_at<s.started_at AND x.finished_at<$2 AND x.inserted_at<$2
  ORDER BY x.started_at DESC,x.attempt_id DESC LIMIT 1
 ) p ON true
 WHERE s.capture_version>=2 AND s.request_class=1 AND s.finished_at >= $3 AND s.finished_at<$2 AND s.inserted_at<$2`
	rows, err := r.db.QueryContext(ctx, query, pq.Array(ids), at, at.Add(-12*time.Hour))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.SchedPerfFact, 0, len(ids))
	for rows.Next() {
		var f service.SchedPerfFact
		var a q.AttemptSample
		var prevID *int64
		var prevEnd *time.Time
		var prevOutcome *q.Outcome
		if err := rows.Scan(&f.ID, &a.StartedAt, &a.FinishedAt, &f.Account, &f.Group, &f.Model, &f.Session,
			&f.Stream, &f.Warm, &f.FullTier, &f.Outcome, &f.TTFO, &f.Censored, &a.InputTokens, &a.CacheReadTokens, &a.CacheCreationTokens,
			&f.First, &f.Delivered, &prevID, &prevEnd, &prevOutcome); err != nil {
			return nil, err
		}
		f.Start, f.Finish = a.StartedAt.UnixMicro(), a.FinishedAt.UnixMicro()
		if prevID != nil {
			a.AccountID, a.GroupID, a.Model, a.SessionKey, a.Outcome = f.Account, f.Group, f.Model, f.Session, f.Outcome
			previous := q.AttemptSample{AccountID: *prevID, GroupID: f.Group, Model: f.Model, SessionKey: f.Session, FinishedAt: *prevEnd, Outcome: *prevOutcome}
			a.Continuation = q.IsContinuation(a, previous)
			f.Read, f.Input, _ = q.ContinuationTokens(a)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// The (component,created_at) and (created_at,id) indexes in migration 054
// bound this scan. An older event's request model may not be its outbound model.
func (r *schedAttemptRepository) LoadPerfSwitches(ctx context.Context, from, at time.Time, afterID int64, limit int) ([]service.SchedPerfSwitch, error) {
	ctx, cancel := context.WithTimeout(ctx, q.LedgerReadTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, `WITH page AS (
  SELECT id,created_at,extra->>'gateway_request_id' AS req,(extra->>'group_id')::bigint AS grp,
   (extra->>'from')::bigint AS owner,coalesce((extra->>'to')::bigint,0) AS target,
   extra->>'kind' AS kind,coalesce(extra->>'new_gen','') AS gen,coalesce(extra->>'reason','') AS reason, nullif(extra->>'from_model','') AS source_model
  FROM ops_system_logs WHERE component='sched_quality' AND message='sched_quality.session_switch'
   AND (created_at,id)>($1,$3) AND created_at>=$1 AND created_at<$2 AND extra @> '{"committed":true}'
   AND extra->>'from' ~ '^[0-9]{1,18}$' AND extra->>'group_id' ~ '^[0-9]{1,18}$'
   AND extra->>'kind'<>'create'
  ORDER BY created_at,id LIMIT $4
 ) SELECT id,created_at,coalesce(req,''),grp,owner,target,kind,reason,gen,
 coalesce(source_model,(SELECT min(model) FROM account_attempt_samples s
 WHERE req<>'' AND s.request_id=page.req AND s.group_id=grp AND s.inserted_at<$2 AND s.finished_at<$2
 HAVING min(model)=max(model)),'')
 FROM page ORDER BY created_at,id`, from, at, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.SchedPerfSwitch
	for rows.Next() {
		var e service.SchedPerfSwitch
		if err := rows.Scan(&e.ID, &e.At, &e.Request, &e.Group, &e.Account, &e.Target, &e.Kind, &e.Reason, &e.Generation, &e.Model); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func NewSchedAdminAccountRepository(client *dbent.Client, db *sql.DB, cache service.SchedulerCache) service.SchedAdminAccountRepository {
	return newAccountRepositoryWithSQL(client, db, cache)
}
func (r *accountRepository) schedAdminChanged(ctx context.Context, id int64, result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	r.afterTempUnschedulableChange(ctx, id, "sched admin clear")
	return true, nil
}
func (r *accountRepository) SchedAdminClearError(ctx context.Context, id int64, message string) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts SET status='active',error_message=NULL,updated_at=NOW() WHERE id=$1 AND deleted_at IS NULL AND status='error' AND COALESCE(error_message,'')=$2`, id, message)
	return r.schedAdminChanged(ctx, id, result, err)
}
func (r *accountRepository) SchedAdminClear429(ctx context.Context, id int64, limited, reset *time.Time) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts SET rate_limited_at=NULL,rate_limit_reset_at=NULL,updated_at=NOW() WHERE id=$1 AND deleted_at IS NULL AND rate_limited_at IS NOT DISTINCT FROM $2::timestamptz AND rate_limit_reset_at IS NOT DISTINCT FROM $3::timestamptz`, id, limited, reset)
	return r.schedAdminChanged(ctx, id, result, err)
}
func (r *accountRepository) SchedAdminClearOverload(ctx context.Context, id int64, until *time.Time) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts SET overload_until=NULL,updated_at=NOW() WHERE id=$1 AND deleted_at IS NULL AND overload_until IS NOT DISTINCT FROM $2::timestamptz`, id, until)
	return r.schedAdminChanged(ctx, id, result, err)
}
func (r *accountRepository) SchedAdminClearTemp(ctx context.Context, id int64, reason string, until *time.Time) (bool, error) {
	if until != nil {
		return r.ClearTempUnschedulableIf(ctx, id, reason, *until)
	}
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts SET temp_unschedulable_until=NULL,temp_unschedulable_reason=NULL,updated_at=NOW() WHERE id=$1 AND deleted_at IS NULL AND COALESCE(temp_unschedulable_reason,'')=$2 AND temp_unschedulable_until IS NULL`, id, reason)
	return r.schedAdminChanged(ctx, id, result, err)
}
func (r *accountRepository) SchedAdminClearModelLimit(ctx context.Context, id int64, key string, original any) (bool, error) {
	if strings.HasPrefix(key, "iq:") {
		return false, fmt.Errorf("iq gate is read only")
	}
	raw, err := json.Marshal(original)
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{model_rate_limits}',(extra->'model_rate_limits')-$2::text),updated_at=NOW() WHERE id=$1 AND deleted_at IS NULL AND extra->'model_rate_limits'->$2::text=$3::jsonb`, id, key, string(raw))
	return r.schedAdminChanged(ctx, id, result, err)
}
func (r *accountRepository) SchedAdminProxyPeers(ctx context.Context, id, proxy int64) (int, error) {
	var n int
	err := scanSingleRow(ctx, r.sql, `SELECT count(*) FROM accounts WHERE proxy_id=$1 AND id<>$2 AND deleted_at IS NULL`, []any{proxy, id}, &n)
	return n, err
}

// Compare and delete in Redis so a replacement detail cannot be removed after a read.
func (c *tempUnschedCache) SchedAdminDeleteTempIf(ctx context.Context, id int64, reason string, until *time.Time) (bool, error) {
	var expected service.TempUnschedState
	if err := json.Unmarshal([]byte(reason), &expected); err != nil {
		expected.ErrorMessage = reason
	}
	if until != nil {
		expected.UntilUnix = until.Unix()
	}
	raw, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	n, err := c.rdb.Eval(ctx, `local raw=redis.call('GET',KEYS[1]); if not raw then return 1 end; local ok,a=pcall(cjson.decode,raw); if not ok then return 0 end; local b=cjson.decode(ARGV[1]); for k,v in pairs(b) do if a[k]~=v and not (a[k]==nil and v==0) then return 0 end end; for k,v in pairs(a) do if b[k]==nil and v~=0 and v~='' then return 0 end end; redis.call('DEL',KEYS[1]); return 1`, []string{fmt.Sprintf("%s%d", tempUnschedPrefix, id)}, string(raw)).Int()
	return n == 1, err
}
func (c *tempUnschedCache) SchedAdminClearHealth(ctx context.Context, id int64) error {
	key := c.openAIAPIKeyHealthKey(id)
	return c.rdb.Del(ctx, key, key+":sequence").Err()
}

var _ service.SchedAdminAccountRepository = (*accountRepository)(nil)

// SchedAdminAccountIDs includes unsampled and ungrouped accounts for quality identity filters.
func (r *accountRepository) SchedAdminAccountIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT id FROM accounts WHERE deleted_at IS NULL AND platform='openai' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const schedAdminDecisionsSQL = `SELECT id,at,group_id,model,line,action,level,until,basis FROM sched_decisions
 WHERE account_id=$1 AND ($2::bigint=0 OR (at,id)<
 (SELECT at,id FROM sched_decisions WHERE account_id=$1 AND id=$2))
 ORDER BY at DESC,id DESC LIMIT $3`

// Cursor id identifies a stable (at,id) boundary, including replayed older events.
func (r *accountRepository) SchedAdminDecisions(ctx context.Context, id int64, limit int, before int64) ([]service.SchedAdminDecision, error) {
	rows, err := r.sql.QueryContext(ctx, schedAdminDecisionsSQL, id, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.SchedAdminDecision{}
	for rows.Next() {
		var d service.SchedAdminDecision
		var at time.Time
		var until sql.NullTime
		var raw []byte
		if err := rows.Scan(&d.ID, &at, &d.GroupID, &d.Model, &d.Line, &d.Action, &d.Level, &until, &raw); err != nil {
			return nil, err
		}
		d.At = at.UTC().Format(time.RFC3339Nano)
		if d.Line != "" {
			if until.Valid && (d.Action == "enter" || d.Action == "renew" || d.Action == "trial_fail" || d.Action == "trial") {
				v := until.Time.UTC().Format(time.RFC3339Nano)
				d.Until = &v
			}
			var basis schedquality.Basis
			if err := json.Unmarshal(raw, &basis); err != nil {
				return nil, fmt.Errorf("decode decision %d basis: %w", d.ID, err)
			}
			d.Basis = service.SchedAdminDecisionBasis(basis)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const schedAdminLatencySQL = `SELECT group_id,
 percentile_cont(0.5) WITHIN GROUP (ORDER BY end_to_end_first_output_ms),
 percentile_cont(0.9) WITHIN GROUP (ORDER BY end_to_end_first_output_ms),
 percentile_cont(0.5) WITHIN GROUP (ORDER BY end_to_end_first_output_ms-body_eof_ms)
  FILTER (WHERE body_eof_ms IS NOT NULL AND body_eof_ms>=0 AND body_eof_ms<=end_to_end_first_output_ms),
 percentile_cont(0.9) WITHIN GROUP (ORDER BY end_to_end_first_output_ms-body_eof_ms)
  FILTER (WHERE body_eof_ms IS NOT NULL AND body_eof_ms>=0 AND body_eof_ms<=end_to_end_first_output_ms),
 count(*) FROM usage_logs
 WHERE group_id=ANY($1::bigint[]) AND created_at>=$2 AND created_at<$3
 AND end_to_end_first_output_ms IS NOT NULL AND end_to_end_first_output_ms>=0
 GROUP BY group_id`
const schedAdminMigrationSQL = `SELECT group_id,count(*) FILTER (WHERE error_class=$4)
 FROM account_attempt_samples WHERE group_id=ANY($1::bigint[])
 AND started_at>=$2 AND started_at<$3 AND capture_version>=2 GROUP BY group_id`

func (r *accountRepository) SchedAdminGroupLast60(ctx context.Context, ids []int64, now time.Time) (map[int64]service.SchedAdminGroupLast60, error) {
	out := map[int64]service.SchedAdminGroupLast60{}
	if len(ids) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []any{pq.Array(ids), now.Add(-time.Hour), now}
	rows, err := r.sql.QueryContext(ctx, schedAdminLatencySQL, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, n int64
		var e50, e90, b50, b90 sql.NullFloat64
		if err := rows.Scan(&id, &e50, &e90, &b50, &b90, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		v := out[id]
		v.Requests = &n
		if e50.Valid {
			v.E2EFirstOutputP50Ms = &e50.Float64
		}
		if e90.Valid {
			v.E2EFirstOutputP90Ms = &e90.Float64
		}
		if b50.Valid {
			v.BodyFirstOutputP50Ms = &b50.Float64
		}
		if b90.Valid {
			v.BodyFirstOutputP90Ms = &b90.Float64
		}
		out[id] = v
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = r.sql.QueryContext(ctx, schedAdminMigrationSQL, append(args, int16(schedquality.ClassMigration400))...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		v := out[id]
		v.Migration400 = &n
		out[id] = v
	}
	return out, rows.Err()
}

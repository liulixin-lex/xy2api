package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
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

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/sjson"
)

const schedQualityCycleLockID int64 = 734891260129

type schedQualityRepository struct{ db *sql.DB }

func NewSchedQualityRepository(_ *ent.Client, db *sql.DB) service.SchedQualityStore {
	return &schedQualityRepository{db: db}
}

func (r *schedQualityRepository) RunCycle(ctx context.Context, fn func(service.SchedQualityTx) error) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('statement_timeout',$1,true)`, fmt.Sprintf("%dms", schedquality.EvalStatementTimeout.Milliseconds())); err != nil {
		return false, err
	}
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, schedQualityCycleLockID).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	if err = fn(&schedQualityTx{ctx: ctx, tx: tx}); err != nil {
		return true, err
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	return true, nil
}

func (r *schedQualityRepository) RunReset(ctx context.Context, accountID int64) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, schedquality.ResetLockTimeout)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('statement_timeout',$1,true)`, fmt.Sprintf("%dms", schedquality.EvalStatementTimeout.Milliseconds())); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, schedQualityCycleLockID); err != nil {
		return time.Time{}, err
	}
	var at time.Time
	if err = tx.QueryRowContext(ctx, `UPDATE accounts SET extra = jsonb_set(COALESCE(extra,'{}'::jsonb),'{group_quality_reset_at}',to_jsonb(clock_timestamp()::text),true) WHERE id=$1 RETURNING (extra->>'group_quality_reset_at')::timestamptz`, accountID).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE account_id=$1`, accountID); err != nil {
		return time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return at, nil
}

func (r *schedQualityRepository) LoadRows(ctx context.Context) ([]schedquality.StateRow, []service.SchedOverride, map[int64]time.Time, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('statement_timeout',$1,true)`, fmt.Sprintf("%dms", schedquality.EvalStatementTimeout.Milliseconds())); err != nil {
		return nil, nil, nil, err
	}
	states, err := loadSchedQualityStates(ctx, tx)
	if err != nil {
		return nil, nil, nil, err
	}
	overrides, err := loadSchedQualityOverrides(ctx, tx)
	if err != nil {
		return nil, nil, nil, err
	}
	resets, err := loadSchedQualityResets(ctx, tx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	return states, overrides, resets, nil
}

func loadSchedQualityStates(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]schedquality.StateRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT account_id,model,group_id,state,level,reason,since,until FROM account_quality_states ORDER BY account_id,model,group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schedquality.StateRow
	for rows.Next() {
		var row schedquality.StateRow
		var until sql.NullTime
		if err := rows.Scan(&row.AccountID, &row.Model, &row.GroupID, &row.State, &row.Level, &row.Reason, &row.Since, &until); err != nil {
			return nil, err
		}
		if until.Valid {
			row.Until = until.Time
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
func loadSchedQualityOverrides(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]service.SchedOverride, error) {
	rows, err := q.QueryContext(ctx, `SELECT account_id,group_id,mode,expires_at,reason,operator_id FROM account_quality_overrides ORDER BY account_id,group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []service.SchedOverride
	for rows.Next() {
		var o service.SchedOverride
		var expires sql.NullTime
		if err := rows.Scan(&o.AccountID, &o.GroupID, &o.Mode, &expires, &o.Reason, &o.OperatorID); err != nil {
			return nil, err
		}
		if expires.Valid {
			t := expires.Time
			o.ExpiresAt = &t
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func loadSchedQualityResets(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[int64]time.Time, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,(extra->>'group_quality_reset_at')::timestamptz FROM accounts WHERE extra ? 'group_quality_reset_at' AND NULLIF(extra->>'group_quality_reset_at','') IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]time.Time)
	for rows.Next() {
		var id int64
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

func (r *schedQualityRepository) LoadMembers(ctx context.Context) (map[int64][]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT g.id,a.id FROM groups g LEFT JOIN account_groups ag ON ag.group_id=g.id LEFT JOIN accounts a ON a.id=ag.account_id AND a.deleted_at IS NULL WHERE lower(g.platform)='openai' AND g.deleted_at IS NULL ORDER BY g.id,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]int64)
	for rows.Next() {
		var g int64
		var a sql.NullInt64
		if err := rows.Scan(&g, &a); err != nil {
			return nil, err
		}
		if _, ok := out[g]; !ok {
			out[g] = nil
		}
		if a.Valid {
			out[g] = append(out[g], a.Int64)
		}
	}
	return out, rows.Err()
}

func (r *schedQualityRepository) UpsertOverride(ctx context.Context, o service.SchedOverride) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO account_quality_overrides(account_id,group_id,mode,expires_at,reason,operator_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account_id,group_id) DO UPDATE SET mode=EXCLUDED.mode,expires_at=EXCLUDED.expires_at,reason=EXCLUDED.reason,operator_id=EXCLUDED.operator_id,created_at=NOW()`, o.AccountID, o.GroupID, o.Mode, o.ExpiresAt, o.Reason, o.OperatorID)
	return err
}
func (r *schedQualityRepository) DeleteOverride(ctx context.Context, a, g int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM account_quality_overrides WHERE account_id=$1 AND group_id=$2`, a, g)
	return err
}

type schedQualityTx struct {
	ctx context.Context
	tx  *sql.Tx
}

func (t *schedQualityTx) States() ([]schedquality.StateRow, error) {
	return loadSchedQualityStates(t.ctx, t.tx)
}
func (t *schedQualityTx) Overrides() ([]service.SchedOverride, error) {
	return loadSchedQualityOverrides(t.ctx, t.tx)
}
func (t *schedQualityTx) ResetTimes() (map[int64]time.Time, error) {
	return loadSchedQualityResets(t.ctx, t.tx)
}
func (t *schedQualityTx) Upsert(rows []schedquality.StateRow) error {
	for _, r := range rows {
		var until any
		if !r.Until.IsZero() {
			until = r.Until
		}
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO account_quality_states(account_id,model,group_id,state,level,reason,since,until) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id,model,group_id) DO UPDATE SET state=EXCLUDED.state,level=EXCLUDED.level,reason=EXCLUDED.reason,since=EXCLUDED.since,until=EXCLUDED.until`, r.AccountID, r.Model, r.GroupID, r.State, r.Level, string(r.Reason), r.Since, until); err != nil {
			return err
		}
	}
	return nil
}
func (t *schedQualityTx) Delete(rows []schedquality.StateRow) error {
	for _, r := range rows {
		if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM account_quality_states WHERE account_id=$1 AND model=$2 AND group_id=$3`, r.AccountID, r.Model, r.GroupID); err != nil {
			return err
		}
	}
	return nil
}
func (t *schedQualityTx) DeleteOverrides(rows []service.SchedOverride) error {
	for _, o := range rows {
		if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM account_quality_overrides o WHERE account_id=$1 AND group_id=$2
 AND expires_at IS NOT DISTINCT FROM $3::timestamptz
 AND ((expires_at IS NOT NULL AND expires_at <= clock_timestamp()) OR
 (group_id <> 0 AND NOT EXISTS (SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id JOIN accounts a ON a.id=ag.account_id WHERE ag.group_id=o.group_id AND ag.account_id=o.account_id AND lower(g.platform)='openai' AND g.deleted_at IS NULL AND a.deleted_at IS NULL)))`, o.AccountID, o.GroupID, o.ExpiresAt); err != nil {
			return err
		}
	}
	return nil
}

type schedQualityPolicyRoot struct {
	Version int64                      `json:"version"`
	Groups  map[string]json.RawMessage `json:"groups"`
}
type schedQualityPolicyEntry struct {
	V4 *schedQualityPolicyV4 `json:"v4"`
	V3 *schedQualityPolicyV3 `json:"v3"`
}
type schedQualityPolicyV4 struct {
	Enabled             bool    `json:"enabled"`
	TTFTSeconds         float64 `json:"ttft_seconds"`
	MaxErrorRate        float64 `json:"max_error_rate"`
	MinCacheRatio       float64 `json:"min_cache_ratio"`
	TakeoverIdleSeconds int     `json:"takeover_idle_seconds"`
}
type schedQualityPolicyV3 struct {
	Enabled           bool    `json:"enabled"`
	MaxTTFTP90Seconds float64 `json:"max_ttft_p90_seconds"`
	MaxErrorRate      float64 `json:"max_error_rate"`
	MinCacheKeepRate  float64 `json:"min_cache_keep_rate"`
}

func decodeSchedQualityPolicies(raw string) (map[int64]service.SchedPolicyRecord, int64, error) {
	root := schedQualityPolicyRoot{Groups: map[string]json.RawMessage{}}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &root); err != nil {
			return nil, 0, err
		}
	}
	out := make(map[int64]service.SchedPolicyRecord)
	for key, rawEntry := range root.Groups {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 {
			return nil, 0, fmt.Errorf("invalid policy group %q", key)
		}
		var entry schedQualityPolicyEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			return nil, 0, err
		}
		record := service.SchedPolicyRecord{GroupID: id, Version: root.Version}
		if entry.V4 != nil {
			v := entry.V4
			record.Policy = schedquality.Policy{Enabled: v.Enabled, TTFTSeconds: v.TTFTSeconds, MaxErrorRate: v.MaxErrorRate, MinCacheRatio: v.MinCacheRatio, TakeoverIdleSeconds: v.TakeoverIdleSeconds}
		} else if entry.V3 != nil {
			v := entry.V3
			record.Policy = schedquality.Policy{Enabled: v.Enabled, TTFTSeconds: v.MaxTTFTP90Seconds, MaxErrorRate: v.MaxErrorRate, MinCacheRatio: v.MinCacheKeepRate, TakeoverIdleSeconds: 0}
			record.FromV3 = true
		}
		out[id] = record
	}
	return out, root.Version, nil
}
func (r *schedQualityRepository) LoadPolicies(ctx context.Context) (map[int64]service.SchedPolicyRecord, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyGroupQualityGatePolicies).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	policies, version, err := decodeSchedQualityPolicies(raw)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM groups WHERE lower(platform)='openai' AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]service.SchedPolicyRecord)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		record := policies[id]
		record.GroupID, record.Version = id, version
		out[id] = record
	}
	return out, rows.Err()
}

func (r *schedQualityRepository) SavePolicy(ctx context.Context, groupID, expected int64, p schedquality.Policy) (service.SchedPolicyRecord, error) {
	if groupID <= 0 {
		return service.SchedPolicyRecord{}, infraerrors.BadRequest("INVALID_SCHED_GROUP", "quality policies require an OpenAI group")
	}
	if err := service.ValidateSchedQualityPolicy(p); err != nil {
		return service.SchedPolicyRecord{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return service.SchedPolicyRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var platform string
	if err := tx.QueryRowContext(ctx, `SELECT platform FROM groups WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, groupID).Scan(&platform); err != nil {
		return service.SchedPolicyRecord{}, err
	}
	if !strings.EqualFold(platform, "openai") {
		return service.SchedPolicyRecord{}, infraerrors.BadRequest("INVALID_SCHED_GROUP", "quality policies require an OpenAI group")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,'{"version":0,"groups":{}}',NOW()) ON CONFLICT(key) DO NOTHING`, service.SettingKeyGroupQualityGatePolicies); err != nil {
		return service.SchedPolicyRecord{}, err
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR UPDATE`, service.SettingKeyGroupQualityGatePolicies).Scan(&raw); err != nil {
		return service.SchedPolicyRecord{}, err
	}
	_, version, err := decodeSchedQualityPolicies(raw)
	if err != nil {
		return service.SchedPolicyRecord{}, err
	}
	if version != expected {
		return service.SchedPolicyRecord{}, service.ErrSchedPolicyConflict
	}
	encoded, err := json.Marshal(schedQualityPolicyV4{Enabled: p.Enabled, TTFTSeconds: p.TTFTSeconds, MaxErrorRate: p.MaxErrorRate, MinCacheRatio: p.MinCacheRatio, TakeoverIdleSeconds: p.TakeoverIdleSeconds})
	if err != nil {
		return service.SchedPolicyRecord{}, err
	}
	path := "groups.:" + strconv.FormatInt(groupID, 10) + ".v4"
	raw, err = sjson.SetRaw(raw, path, string(encoded))
	if err != nil {
		return service.SchedPolicyRecord{}, err
	}
	raw, err = sjson.Set(raw, "version", version+1)
	if err != nil {
		return service.SchedPolicyRecord{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE settings SET value=$2,updated_at=NOW() WHERE key=$1`, service.SettingKeyGroupQualityGatePolicies, raw); err != nil {
		return service.SchedPolicyRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return service.SchedPolicyRecord{}, err
	}
	return service.SchedPolicyRecord{GroupID: groupID, Version: version + 1, Policy: p}, nil
}

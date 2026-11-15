package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/sjson"
	"golang.org/x/time/rate"
)

const schedQualityCycleLockID int64 = 734891260229

var (
	schedQualityResetWarnings  = rate.NewLimiter(rate.Every(time.Minute), 1)
	schedQualityPolicyWarnings = rate.NewLimiter(rate.Every(time.Minute), 1)
)

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
	if err = tx.QueryRowContext(ctx, `UPDATE accounts SET extra = jsonb_set(COALESCE(extra,'{}'::jsonb),'{group_quality_reset_at}',to_jsonb(to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),true) WHERE id=$1 RETURNING (extra->>'group_quality_reset_at')::timestamptz`, accountID).Scan(&at); err != nil {
		return time.Time{}, err
	}
	rows, err := loadSchedQualityStates(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	var decisions []schedquality.Decision
	for _, row := range rows {
		if row.AccountID == accountID && row.State == schedquality.StateCooling {
			basis := schedquality.DecisionBasis{RuleVersion: schedquality.RuleVersion}
			if row.Basis != nil {
				basis = *row.Basis
			}
			decisions = append(decisions, schedquality.Decision{At: at, Scope: schedquality.Scope{AccountID: accountID, GroupID: row.GroupID, Model: row.Model}, Action: "reset", Basis: basis})
		}
	}
	if err := (&schedQualityTx{ctx: ctx, tx: tx}).InsertDecisions(decisions); err != nil {
		return time.Time{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sched_states WHERE account_id=$1`, accountID); err != nil {
		return time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return time.Time{}, err
	}
	for _, decision := range decisions {
		service.LogSchedQualityDecision(ctx, decision)
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
	rows, err := q.QueryContext(ctx, `SELECT account_id,model,group_id,failing,dims,level,since,until,evidence_since,basis FROM sched_states ORDER BY account_id,model,group_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []schedquality.StateRow
	for rows.Next() {
		var r schedquality.StateRow
		var failing bool
		var dims pq.StringArray
		var since, until, evidence sql.NullTime
		var raw []byte
		if err := rows.Scan(&r.AccountID, &r.Model, &r.GroupID, &failing, &dims, &r.Level, &since, &until, &evidence, &raw); err != nil {
			return nil, err
		}
		r.Since, r.Until, r.EvidenceSince = since.Time, until.Time, evidence.Time
		if failing {
			r.State = schedquality.StateCooling
		}
		r.Basis = &schedquality.DecisionBasis{}
		if err := json.Unmarshal(raw, r.Basis); err != nil {
			return nil, err
		}
		r.Basis.Dims = nil
		for _, dim := range dims {
			r.Basis.Dims = append(r.Basis.Dims, schedquality.Dimension(dim))
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func loadSchedQualityOverrides(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]service.SchedOverride, error) {
	rows, err := q.QueryContext(ctx, `SELECT account_id,group_id,mode,expires_at,reason,operator_id FROM account_quality_overrides WHERE mode=1 ORDER BY account_id,group_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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
	rows, err := q.QueryContext(ctx, `SELECT id,extra->>'group_quality_reset_at' FROM accounts WHERE extra ? 'group_quality_reset_at' AND NULLIF(extra->>'group_quality_reset_at','') IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]time.Time)
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var at time.Time
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05Z07", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05Z07:00:00"} {
			at, err = time.Parse(layout, raw)
			if err == nil {
				break
			}
		}
		if err != nil {
			if schedQualityResetWarnings.Allow() {
				slog.WarnContext(ctx, "sched_quality.invalid_reset_time", "account_id", id)
			}
			continue
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
	defer func() { _ = rows.Close() }()
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
func nullableQualityTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at
}
func qualityBasis(b *schedquality.DecisionBasis) ([]byte, []string, error) {
	if b == nil {
		return []byte("{}"), []string{}, nil
	}
	raw, err := json.Marshal(b)
	dims := make([]string, len(b.Dims))
	for i, dim := range b.Dims {
		dims[i] = string(dim)
	}
	return raw, dims, err
}
func (t *schedQualityTx) Upsert(rows []schedquality.StateRow) error {
	for _, r := range rows {
		raw, dims, err := qualityBasis(r.Basis)
		if err != nil {
			return err
		}
		_, err = t.tx.ExecContext(t.ctx, `INSERT INTO sched_states(account_id,model,group_id,failing,dims,level,since,until,evidence_since,basis)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
  ON CONFLICT(account_id,model,group_id) DO UPDATE SET failing=EXCLUDED.failing,dims=EXCLUDED.dims,level=EXCLUDED.level,since=EXCLUDED.since,until=EXCLUDED.until,evidence_since=EXCLUDED.evidence_since,basis=EXCLUDED.basis,updated_at=NOW()`,
			r.AccountID, r.Model, r.GroupID, r.State == schedquality.StateCooling, pq.Array(dims), r.Level, nullableQualityTime(r.Since), nullableQualityTime(r.Until), nullableQualityTime(r.EvidenceSince), string(raw))
		if err != nil {
			return err
		}
	}
	return nil
}
func (t *schedQualityTx) InsertDecisions(rows []schedquality.Decision) error {
	for _, r := range rows {
		raw, dims, err := qualityBasis(&r.Basis)
		if err != nil {
			return err
		}
		ids := r.Basis.AttemptIDs
		if ids == nil {
			ids = []string{}
		}
		_, err = t.tx.ExecContext(t.ctx, `INSERT INTO sched_decisions(at,rule_version,account_id,model,group_id,action,dims,level,until,basis,attempt_ids) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			r.At, schedquality.RuleVersion, r.Scope.AccountID, r.Scope.Model, r.Scope.GroupID, r.Action, pq.Array(dims), r.Level, nullableQualityTime(r.Until), string(raw), pq.Array(ids))
		if err != nil {
			return err
		}
	}
	return nil
}
func (r *schedQualityRepository) DeleteDecisionsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM sched_decisions WHERE at<$1`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (t *schedQualityTx) Delete(rows []schedquality.StateRow) error {
	for _, r := range rows {
		if _, err := t.tx.ExecContext(t.ctx, `DELETE FROM sched_states WHERE account_id=$1 AND model=$2 AND group_id=$3`, r.AccountID, r.Model, r.GroupID); err != nil {
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
type schedQualityPolicyV4 struct {
	Enabled             bool    `json:"enabled"`
	TTFTSeconds         float64 `json:"ttft_seconds"`
	MaxErrorRate        float64 `json:"max_error_rate"`
	MinCacheRatio       float64 `json:"min_cache_ratio"`
	TakeoverIdleSeconds int     `json:"takeover_idle_seconds"`
}

func decodeSchedQualityPolicies(raw string) (map[int64]service.SchedPolicyRecord, int64, error) {
	root := schedQualityPolicyRoot{Groups: map[string]json.RawMessage{}}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &root); err != nil {
			return nil, 0, err
		}
	}
	out := make(map[int64]service.SchedPolicyRecord)
	warn := func(group string, err error) {
		if schedQualityPolicyWarnings.Allow() {
			slog.Warn("sched_quality.invalid_policy", "group_id", group, "error", err)
		}
	}
	for key, rawEntry := range root.Groups {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 {
			warn(key, errors.New("invalid policy group ID"))
			continue
		}
		record, err := decodeSchedQualityPolicy(rawEntry, key)
		if err != nil {
			warn(key, err)
			continue
		}
		record.GroupID, record.Version = id, root.Version
		out[id] = record
	}
	return out, root.Version, nil
}

func decodeSchedQualityPolicy(raw json.RawMessage, groupID string) (service.SchedPolicyRecord, error) {
	var entry map[string]json.RawMessage
	var record service.SchedPolicyRecord
	if err := json.Unmarshal(raw, &entry); err != nil {
		return record, err
	}
	if entry == nil {
		return record, errors.New("policy entry must be an object")
	}

	rawV4 := entry["v4"]
	if len(rawV4) == 0 || string(rawV4) == "null" {
		record.Policy = schedquality.DefaultPolicy()
		return record, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawV4, &fields); err != nil {
		return record, err
	}
	if fields == nil {
		return record, errors.New("v4 policy must be an object")
	}

	defaults := schedquality.DefaultPolicy()
	var issues []error
	v := schedQualityPolicyV4{Enabled: defaults.Enabled, TTFTSeconds: defaults.TTFTSeconds, MaxErrorRate: defaults.MaxErrorRate, MinCacheRatio: defaults.MinCacheRatio, TakeoverIdleSeconds: defaults.TakeoverIdleSeconds}
	read := func(err error) {
		if err != nil {
			issues = append(issues, err)
		}
	}
	var fieldErr error
	v.Enabled, fieldErr = readSchedQualityPolicyField(fields, "enabled", defaults.Enabled)
	read(fieldErr)
	v.TTFTSeconds, fieldErr = readSchedQualityPolicyField(fields, "ttft_seconds", defaults.TTFTSeconds)
	read(fieldErr)
	v.MaxErrorRate, fieldErr = readSchedQualityPolicyField(fields, "max_error_rate", defaults.MaxErrorRate)
	read(fieldErr)
	v.MinCacheRatio, fieldErr = readSchedQualityPolicyField(fields, "min_cache_ratio", defaults.MinCacheRatio)
	read(fieldErr)
	v.TakeoverIdleSeconds, fieldErr = readSchedQualityPolicyField(fields, "takeover_idle_seconds", defaults.TakeoverIdleSeconds)
	read(fieldErr)
	record.Policy = schedquality.Policy{Enabled: v.Enabled, TTFTSeconds: v.TTFTSeconds, MaxErrorRate: v.MaxErrorRate, MinCacheRatio: v.MinCacheRatio, TakeoverIdleSeconds: v.TakeoverIdleSeconds}
	// The v0.6.4 editor allowed error thresholds below 5%. Keep old saved
	// values usable during rollback/blue-green overlap, matching migration 301.
	if record.Policy.MaxErrorRate < schedquality.ErrorRateFloor {
		record.Policy.MaxErrorRate = schedquality.ErrorRateFloor
	}
	if err := service.ValidateSchedQualityPolicy(record.Policy); err != nil {
		issues = append(issues, err)
	}
	if issue := errors.Join(issues...); issue != nil && schedQualityPolicyWarnings.Allow() {
		slog.Warn("sched_quality.invalid_policy", "group_id", groupID, "error", issue)
	}
	return record, nil
}

func readSchedQualityPolicyField[T any](fields map[string]json.RawMessage, name string, fallback T) (T, error) {
	raw, ok := fields[name]
	if !ok {
		return fallback, fmt.Errorf("%s: missing policy field", name)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return fallback, fmt.Errorf("%s: null is not a policy value", name)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback, fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
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
	defer func() { _ = rows.Close() }()
	out := make(map[int64]service.SchedPolicyRecord)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		record, ok := policies[id]
		if !ok {
			record.Policy = schedquality.DefaultPolicy()
		}
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

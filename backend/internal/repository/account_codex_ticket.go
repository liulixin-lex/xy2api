package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/ent"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// MutateCodexTicket serializes STATE transitions with ordinary account edits.
// The callback performs no I/O; network requests run after the transaction commits.
func (r *accountRepository) MutateCodexTicket(ctx context.Context, id int64, limit int, fn service.CodexTicketMutation) (*service.Account, error) {
	accounts, err := r.MutateCodexTicketBatch(ctx, []int64{id}, limit, fn)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, service.ErrAccountNotFound
	}
	return accounts[0], nil
}

// Batch admission holds the cluster lock once and counts leases once. Callbacks
// only acquire leases when limit > 0; lifecycle mutations use limit == 0.
func (r *accountRepository) MutateCodexTicketBatch(ctx context.Context, ids []int64, limit int, fn service.CodexTicketMutation) ([]*service.Account, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if fence, ok := service.CodexTicketFenceFromContext(ctx); ok {
		// Also fences insertion of a previously absent global setting.
		if _, err = c.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421023)"); err != nil {
			return nil, err
		}
		rows, e := c.QueryContext(ctx, `SELECT key,value FROM settings WHERE key IN ($1,$2,$3) FOR SHARE`, service.SettingKeyOpenAICodexTicketEnabled, service.SettingKeyOpenAICodexTicketHarvestProxyURL, service.SettingKeyCodexTicketProxyPool)
		if e != nil {
			return nil, e
		}
		enabled := fence.FallbackEnabled
		pool := fence.FallbackPool
		poolRevision := ""
		for rows.Next() {
			var key, value string
			if e = rows.Scan(&key, &value); e != nil {
				_ = rows.Close()
				return nil, e
			}
			if key == service.SettingKeyCodexTicketProxyPool {
				var p struct {
					Revision string `json:"revision"`
				}
				if json.Unmarshal([]byte(value), &p) != nil {
					_ = rows.Close()
					return nil, service.ErrCodexTicketConflict
				}
				poolRevision = p.Revision
			}
			if key == service.SettingKeyOpenAICodexTicketEnabled {
				enabled = value == "true"
			}
			if key == service.SettingKeyOpenAICodexTicketHarvestProxyURL && strings.TrimSpace(value) != "" {
				pool = strings.TrimSpace(value)
			}
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return nil, e
		}
		if !enabled || poolRevision != fence.PoolRevision || poolRevision == "" && pool != fence.Pool {
			return nil, service.ErrCodexTicketConflict
		}
	}

	if limit > 0 {
		if _, err = c.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421022)"); err != nil {
			return nil, err
		}
	}
	active := 0
	// Lease decisions use the database clock, shared by every gateway instance.
	clockRows, err := c.QueryContext(ctx, "SELECT clock_timestamp()")
	if err != nil {
		return nil, err
	}
	var now time.Time
	if !clockRows.Next() {
		err = clockRows.Err()
		_ = clockRows.Close()
		if err == nil {
			err = errors.New("STATE database clock unavailable")
		}
		return nil, err
	}
	err = clockRows.Scan(&now)
	if err == nil {
		err = clockRows.Err()
	}
	_ = clockRows.Close()
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	if limit > 0 {
		rows, e := c.QueryContext(ctx, `SELECT count(*) FROM accounts a CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(a.extra->'codex_ticket_runtime')='object' THEN a.extra->'codex_ticket_runtime' ELSE '{}'::jsonb END) j WHERE a.deleted_at IS NULL AND (j.value->>'lease_until')::timestamptz > $1`, now)
		if e != nil {
			return nil, e
		}
		if rows.Next() {
			err = rows.Scan(&active)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	accounts := make([]*service.Account, 0, len(ids))
	var changedIDs []int64
	ordered := sortedUniqueAccountIDs(append([]int64(nil), ids...))
	locked := map[int64]*ent.Account{}
	if limit > 0 && len(ordered) > 1 {
		rows, e := c.Account.Query().Where(dbaccount.IDIn(ordered...)).Order(ent.Asc(dbaccount.FieldID)).ForUpdate().All(ctx)
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			locked[row.ID] = row
		}
		ordered = nil
		seen := map[int64]bool{}
		for _, id := range ids {
			if !seen[id] {
				ordered = append(ordered, id)
				seen[id] = true
			}
		}
	}
	for _, id := range ordered {
		if len(ids) > 1 && limit > 0 && active >= limit {
			break
		}
		m := locked[id]
		var err error
		if m == nil {
			m, err = c.Account.Query().Where(dbaccount.IDEQ(id)).ForUpdate().Only(ctx)
		}
		if err != nil {
			return nil, translatePersistenceError(err, service.ErrAccountNotFound, nil)
		}
		a := accountEntityToService(m)
		if a.ProxyID != nil {
			p, e := c.Proxy.Get(ctx, *a.ProxyID)
			if e != nil {
				return nil, e
			}
			a.Proxy = proxyEntityToService(p)
			matched, e := lockAndMatchProbeProxyIdentity(ctx, c, a)
			if e != nil {
				return nil, e
			}
			if !matched {
				return nil, service.ErrCodexTicketConflict
			}
		}
		changed, err := fn(a, active, now)
		if err != nil {
			return nil, err
		}
		if changed {
			changedIDs = append(changedIDs, id)
			if limit > 0 {
				active++
			}
			update := c.Account.UpdateOneID(id).SetExtra(a.Extra)
			if !service.CodexTicketDiagnosticsOnly(ctx) {
				update.SetIqCheck(a.IQCheck)
			}
			if _, err = update.Save(ctx); err != nil {
				return nil, err
			}
			if !service.CodexTicketDiagnosticsOnly(ctx) {
				if err = enqueueSchedulerOutbox(ctx, c, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
					return nil, err
				}
			}
		}
		accounts = append(accounts, a)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for _, id := range changedIDs {
		if !service.CodexTicketDiagnosticsOnly(ctx) {
			r.syncSchedulerAccountSnapshot(ctx, id)
		}
	}
	return accounts, nil
}

func (r *accountRepository) MigrateCodexTicketAccounts(ctx context.Context, cfg config.OpenAICodexTicketConfig) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if _, err = c.ExecContext(ctx, "SELECT pg_advisory_xact_lock(78421022)"); err != nil {
		return err
	}
	rows, err := c.QueryContext(ctx, `SELECT value FROM settings WHERE key='codex_ticket_envelope_migration_v2'`)
	if err != nil {
		return err
	}
	done := rows.Next()
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if done {
		return tx.Commit()
	}
	rows, err = c.QueryContext(ctx, `SELECT value FROM settings WHERE key='codex_ticket_account_migration_v1'`)
	if err != nil {
		return err
	}
	scopeMigrated := rows.Next()
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	rows, err = c.QueryContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, service.SettingKeyOpenAICodexTicketEnabled)
	if err != nil {
		return err
	}
	if rows.Next() {
		var enabled string
		if err = rows.Scan(&enabled); err != nil {
			_ = rows.Close()
			return err
		}
		cfg.Enabled = enabled == "true"
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	accounts, err := c.Account.Query().Where(dbaccount.PlatformEQ(service.PlatformOpenAI)).ForUpdate().All(ctx)
	if err != nil {
		return err
	}
	for _, m := range accounts {
		a := accountEntityToService(m)
		scopeChanged := false
		if !scopeMigrated {
			scopeChanged = service.CodexTicketMigrationExtra(a, cfg)
		}
		if service.NormalizeCodexTicketAccountTimes(a, cfg.TTLSeconds) || scopeChanged {
			if _, err = c.Account.UpdateOneID(m.ID).SetExtra(a.Extra).Save(ctx); err != nil {
				return err
			}
			if err = enqueueSchedulerOutbox(ctx, c, service.SchedulerOutboxEventAccountChanged, &m.ID, nil, nil); err != nil {
				return err
			}
		}
	}
	if _, err = c.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES ('codex_ticket_account_migration_v1','1',now()), ('codex_ticket_envelope_migration_v2','1',now()) ON CONFLICT(key) DO NOTHING`); err != nil {
		return err
	}
	return tx.Commit()
}

package repository

import (
	"context"
	"errors"
	"log/slog"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/liulixin-lex/xy2api/ent"
	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// MutateGeminiCooldown uses the same row lock as account edits. State and its
// scheduler event commit together, including a recovery probe's lease owner.
func (r *accountRepository) MutateGeminiCooldown(ctx context.Context, id int64, mutate func(*service.Account, time.Time) (bool, error)) error {
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.MutateGeminiCooldown(ctx, id, mutate)
	}
	client := r.client
	tx, err := client.Tx(ctx)
	if err != nil && !errors.Is(err, ent.ErrTxStarted) {
		return err
	}
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		client = tx.Client()
	}
	row, err := client.Account.Query().Where(dbaccount.IDEQ(id), dbaccount.DeletedAtIsNil()).ForUpdate(entsql.WithLockClause("FOR NO KEY UPDATE")).Only(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrAccountNotFound, nil)
	}
	account := accountEntityToService(row)
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	// All processes use the database clock for cooldown and lease transitions.
	rows, err := client.QueryContext(ctx, "SELECT clock_timestamp()")
	if err != nil {
		return err
	}
	var now time.Time
	if !rows.Next() {
		_ = rows.Close()
		return errors.New("Gemini cooldown database clock unavailable")
	}
	err = rows.Scan(&now)
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	changed, err := mutate(account, now.UTC())
	if err != nil {
		return err
	}
	if changed {
		if _, err = client.Account.UpdateOneID(id).SetExtra(account.Extra).Save(ctx); err != nil {
			return err
		}
		if err = enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return err
		}
	}
	if tx != nil {
		if err = tx.Commit(); err != nil {
			return err
		}
		if changed {
			r.syncSchedulerAccountSnapshotDetached(ctx, id)
			slog.Info("gemini_cooldown_state_changed", "account_id", id)
		}
	}
	return nil
}

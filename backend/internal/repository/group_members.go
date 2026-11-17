package repository

import (
	"context"
	"errors"

	"github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (r *groupRepository) SetMemberPinned(ctx context.Context, groupID, accountID int64, pinned bool) error {
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}
	exec := sqlExecutor(r.client)
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	if err := lockLiveGroups(ctx, exec, []int64{groupID}); err != nil {
		return err
	}
	result, err := exec.ExecContext(ctx, `UPDATE account_groups ag SET pinned=$3 FROM groups g WHERE ag.group_id=$1 AND ag.account_id=$2 AND g.id=ag.group_id AND g.platform='openai' AND g.deleted_at IS NULL`, groupID, accountID, pinned)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return infraerrors.BadRequest("INVALID_GROUP_MEMBER", "account must be a member of an OpenAI group")
	}
	if err := bumpAccountMembershipRevision(ctx, exec, []int64{accountID}); err != nil {
		return err
	}
	if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload([]int64{groupID})); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// CopyAccountMembers materializes one filtered source set in a single statement.
// Both replacement and insertion consume that same snapshot, even if a source
// membership changes while the target rows are locked by another transaction.
func (r *groupRepository) CopyAccountMembers(ctx context.Context, groupID int64, sources []int64, oauthOnly, replace bool) (int64, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return 0, err
	}
	exec := sqlExecutor(r.client)
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	if err := lockLiveGroups(ctx, exec, []int64{groupID}); err != nil {
		return 0, err
	}
	var count int64
	err = scanSingleRow(ctx, exec, `/* ratefloor_copy_members */
 WITH desired AS MATERIALIZED (
  SELECT DISTINCT ag.account_id FROM account_groups ag
  JOIN accounts a ON a.id=ag.account_id
  WHERE ag.group_id=ANY($2::bigint[]) AND a.deleted_at IS NULL AND (NOT $3 OR a.type<>$4)
 ), removed AS (
  DELETE FROM account_groups ag WHERE ag.group_id=$1 AND $5
  AND NOT EXISTS (SELECT 1 FROM desired d WHERE d.account_id=ag.account_id)
  RETURNING ag.account_id
 ), added AS (
  INSERT INTO account_groups(account_id,group_id,pinned,created_at)
  SELECT account_id,$1::bigint,FALSE,NOW() FROM desired
  ON CONFLICT(account_id,group_id) DO NOTHING RETURNING account_id
 ), revised AS (
  UPDATE accounts SET updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond')
  WHERE id IN (SELECT account_id FROM removed UNION SELECT account_id FROM added) RETURNING id
 ) SELECT COUNT(*) FROM desired`, []any{groupID, pq.Array(uniquePositiveInt64s(sources)), oauthOnly, service.AccountTypeAPIKey, replace}, &count)
	if err != nil {
		return 0, err
	}
	if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventGroupChanged, nil, &groupID, nil); err != nil {
		return 0, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// Membership projections share the accounts.updated_at revision used by the
// scheduler's monotonic cache writer. Advance it in the membership transaction.
func bumpAccountMembershipRevision(ctx context.Context, exec sqlExecutor, accountIDs []int64) error {
	if len(accountIDs) == 0 {
		return nil
	}
	_, err := exec.ExecContext(ctx, `UPDATE accounts SET updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=ANY($1::bigint[])`, pq.Array(uniquePositiveInt64s(accountIDs)))
	return err
}

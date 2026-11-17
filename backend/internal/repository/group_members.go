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
	if err := bumpAccountMembershipRevision(ctx, exec, []int64{accountID}); err != nil {
		return err
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
	if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload([]int64{groupID})); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// CopyAccountMembers serializes copies to one target, then locks candidate
// accounts before the target group and its memberships. Only locked accounts
// participate in the definitive source snapshot shared by deletion and insertion.
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
	if err := lockMemberReplacement(ctx, exec, groupID); err != nil {
		return 0, err
	}
	accountIDs, err := lockMemberCopyAccounts(ctx, exec, groupID, sources, oauthOnly)
	if err != nil {
		return 0, err
	}
	if err := lockLiveGroups(ctx, exec, []int64{groupID}); err != nil {
		return 0, err
	}
	var count int64
	err = scanSingleRow(ctx, exec, `/* ratefloor_copy_members */
 WITH desired AS MATERIALIZED (
  SELECT DISTINCT ag.account_id FROM account_groups ag
  JOIN accounts a ON a.id=ag.account_id
  WHERE ag.group_id=ANY($2::bigint[]) AND a.id=ANY($6::bigint[]) AND a.deleted_at IS NULL AND (NOT $3 OR a.type<>$4)
 ), removed AS (
  DELETE FROM account_groups ag WHERE ag.group_id=$1 AND $5 AND ag.account_id=ANY($6::bigint[])
  AND NOT EXISTS (SELECT 1 FROM desired d WHERE d.account_id=ag.account_id)
  RETURNING ag.account_id
 ), added AS (
  INSERT INTO account_groups(account_id,group_id,pinned,created_at)
  SELECT account_id,$1::bigint,FALSE,NOW() FROM desired
  ON CONFLICT(account_id,group_id) DO NOTHING RETURNING account_id
 ), revised AS (
  UPDATE accounts SET updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond')
  WHERE id IN (SELECT account_id FROM removed UNION SELECT account_id FROM added) RETURNING id
 ) SELECT COUNT(*) FROM desired`, []any{groupID, pq.Array(uniquePositiveInt64s(sources)), oauthOnly, service.AccountTypeAPIKey, replace, pq.Array(accountIDs)}, &count)
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

// Copies alone acquire this transaction lock, before any existing row locks.
// A distinct textual namespace avoids the repository's other advisory protocols.
func lockMemberReplacement(ctx context.Context, exec sqlExecutor, groupID int64) error {
	_, err := exec.ExecContext(ctx, `/* ratefloor_member_replace_lock */ SELECT pg_advisory_xact_lock(hashtextextended('ratefloor:member_replace:' || $1::text, 0))`, groupID)
	return err
}

// Capture and lock the possible target/source accounts in one ordered statement.
// The source is read again after these locks for the definitive copy snapshot;
// neither that read nor the target delete may grow beyond this locked set.
func lockMemberCopyAccounts(ctx context.Context, exec sqlExecutor, groupID int64, sources []int64, oauthOnly bool) ([]int64, error) {
	rows, err := exec.QueryContext(ctx, `/* ratefloor_copy_account_locks */
 SELECT a.id FROM accounts a WHERE a.id IN (
  SELECT ag.account_id FROM account_groups ag WHERE ag.group_id=$1
  UNION
  SELECT ag.account_id FROM account_groups ag JOIN accounts src ON src.id=ag.account_id
  WHERE ag.group_id=ANY($2::bigint[]) AND src.deleted_at IS NULL AND (NOT $3 OR src.type<>$4)
 ) ORDER BY a.id FOR NO KEY UPDATE OF a`, groupID, pq.Array(uniquePositiveInt64s(sources)), oauthOnly, service.AccountTypeAPIKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Membership projections share the accounts.updated_at revision used by the
// scheduler's monotonic cache writer. Advance it in the membership transaction.
func bumpAccountMembershipRevision(ctx context.Context, exec sqlExecutor, accountIDs []int64) error {
	if len(accountIDs) == 0 {
		return nil
	}
	_, err := exec.ExecContext(ctx, `/* ratefloor_membership_revision */ UPDATE accounts SET updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=ANY($1::bigint[])`, pq.Array(uniquePositiveInt64s(accountIDs)))
	return err
}

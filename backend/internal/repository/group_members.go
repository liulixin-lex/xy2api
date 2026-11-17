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
// accounts before the target group and its memberships. The source and candidate
// accounts share one snapshot taken before waiting for any account lock.
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
	snapshot, err := lockMemberCopyAccounts(ctx, exec, groupID, sources, oauthOnly)
	if err != nil {
		return 0, err
	}
	if err := lockLiveGroups(ctx, exec, []int64{groupID}); err != nil {
		return 0, err
	}
	var count int64
	err = scanSingleRow(ctx, exec, `/* ratefloor_copy_members */
 WITH desired AS MATERIALIZED (
  SELECT unnest($2::bigint[]) AS account_id
 ), removed AS (
  DELETE FROM account_groups ag WHERE ag.group_id=$1 AND $3 AND ag.account_id=ANY($4::bigint[])
  AND NOT EXISTS (SELECT 1 FROM desired d WHERE d.account_id=ag.account_id)
  RETURNING ag.account_id
 ), added AS (
  INSERT INTO account_groups(account_id,group_id,pinned,created_at)
  SELECT account_id,$1::bigint,FALSE,NOW() FROM desired
  ON CONFLICT(account_id,group_id) DO NOTHING RETURNING account_id
 ), revised AS (
  UPDATE accounts SET updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond')
  WHERE id IN (SELECT account_id FROM removed UNION SELECT account_id FROM added) RETURNING id
 ) SELECT COUNT(*) FROM desired`, []any{groupID, pq.Array(snapshot.sourceIDs), replace, pq.Array(snapshot.accountIDs)}, &count)
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

// Copies alone acquire this transaction lock before their explicit row locks.
// The prefix distinguishes this protocol; the bigint key is still a 64-bit hash.
func lockMemberReplacement(ctx context.Context, exec sqlExecutor, groupID int64) error {
	_, err := exec.ExecContext(ctx, `/* ratefloor_member_replace_lock */ SELECT pg_advisory_xact_lock(hashtextextended('ratefloor:member_replace:' || $1::text, 0))`, groupID)
	return err
}

// memberCopySnapshot retains source membership (including pin) from before any
// account lock wait. Its source arrays contain only accounts actually locked.
type memberCopySnapshot struct {
	accountIDs   []int64
	sourceIDs    []int64
	sourcePinned []bool
}

func lockMemberCopyAccounts(ctx context.Context, exec sqlExecutor, groupID int64, sources []int64, oauthOnly bool) (*memberCopySnapshot, error) {
	// One read freezes both the source and the target candidate IDs. Never read
	// source membership again after a lock wait: it may have moved to new IDs.
	rows, err := exec.QueryContext(ctx, `/* ratefloor_copy_snapshot */
 WITH source AS MATERIALIZED (
  SELECT ag.account_id, BOOL_OR(ag.pinned) AS pinned
  FROM account_groups ag JOIN accounts a ON a.id=ag.account_id
  WHERE ag.group_id=ANY($2::bigint[]) AND a.deleted_at IS NULL AND (NOT $3 OR a.type<>$4)
  GROUP BY ag.account_id
 ), candidates AS (
  SELECT account_id FROM account_groups WHERE group_id=$1
  UNION SELECT account_id FROM source
 ) SELECT c.account_id, s.account_id IS NOT NULL, COALESCE(s.pinned,FALSE)
 FROM candidates c LEFT JOIN source s ON s.account_id=c.account_id`, groupID, pq.Array(uniquePositiveInt64s(sources)), oauthOnly, service.AccountTypeAPIKey)
	if err != nil {
		return nil, err
	}
	candidates := make([]int64, 0)
	sourcePins := make(map[int64]bool)
	for rows.Next() {
		var id int64
		var source, pinned bool
		if err := rows.Scan(&id, &source, &pinned); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, id)
		if source {
			sourcePins[id] = pinned
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}

	// PostgreSQL rechecks existence/deleted_at after a wait. Accounts removed
	// meanwhile are omitted; source membership and its pin remain the old snapshot.
	rows, err = exec.QueryContext(ctx, `/* ratefloor_copy_account_locks */
 SELECT id FROM accounts WHERE id=ANY($1::bigint[]) AND deleted_at IS NULL
 ORDER BY id FOR NO KEY UPDATE`, pq.Array(candidates))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	snapshot := &memberCopySnapshot{accountIDs: make([]int64, 0), sourceIDs: make([]int64, 0), sourcePinned: make([]bool, 0)}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		snapshot.accountIDs = append(snapshot.accountIDs, id)
		if pinned, source := sourcePins[id]; source {
			snapshot.sourceIDs = append(snapshot.sourceIDs, id)
			snapshot.sourcePinned = append(snapshot.sourcePinned, pinned)
		}
	}
	return snapshot, rows.Err()
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

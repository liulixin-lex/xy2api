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
	if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload([]int64{groupID})); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// CopyAccountMembers preserves retained target pins. Newly added members start
// unpinned because the target group can have different pricing from the source.
func (r *groupRepository) CopyAccountMembers(ctx context.Context, groupID int64, sources, accounts []int64, replace bool) error {
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
	accounts = uniquePositiveInt64s(accounts)
	if replace {
		if _, err := exec.ExecContext(ctx, `DELETE FROM account_groups WHERE group_id=$1 AND NOT (account_id = ANY($2::bigint[]))`, groupID, pq.Array(accounts)); err != nil {
			return err
		}
	}
	if len(accounts) > 0 {
		_, err := exec.ExecContext(ctx, `INSERT INTO account_groups (account_id,group_id,pinned,created_at)
   SELECT DISTINCT ag.account_id,$1::bigint,FALSE,NOW()
   FROM account_groups ag
   WHERE ag.group_id=ANY($2::bigint[]) AND ag.account_id=ANY($3::bigint[])
   ON CONFLICT (account_id,group_id) DO NOTHING`, groupID, pq.Array(sources), pq.Array(accounts))
		if err != nil {
			return err
		}
	}
	if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventGroupChanged, nil, &groupID, nil); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

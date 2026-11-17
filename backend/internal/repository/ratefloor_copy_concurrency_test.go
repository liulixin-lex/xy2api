//go:build unit || integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/lib/pq"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/stretchr/testify/require"
)

// The target membership row is locked until the copy's SQL snapshot is active.
// Replace A with B in the source while the copy waits; no sleep determines order.
func runRatefloorCopyConcurrentSourceChange(t *testing.T, client *dbent.Client, db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	source, err := client.Group.Create().SetName(fmt.Sprintf("copy-source-%d", suffix)).SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	target, err := client.Group.Create().SetName(fmt.Sprintf("copy-target-%d", suffix)).SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	ids := []int64{}
	for _, name := range []string{"A", "B", "C"} {
		a, err := client.Account.Create().SetName(fmt.Sprintf("copy-%s-%d", name, suffix)).SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).Save(ctx)
		require.NoError(t, err)
		ids = append(ids, a.ID)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM account_groups WHERE group_id IN ($1,$2)`, source.ID, target.ID)
		_, _ = db.Exec(`DELETE FROM accounts WHERE id IN ($1,$2,$3)`, ids[0], ids[1], ids[2])
		_, _ = db.Exec(`DELETE FROM groups WHERE id IN ($1,$2)`, source.ID, target.ID)
	})
	_, err = db.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,created_at) VALUES($1,$2,NOW()),($3,$4,NOW())`, ids[0], source.ID, ids[2], target.ID)
	require.NoError(t, err)
	holder, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = holder.ExecContext(ctx, `UPDATE account_groups SET pinned=pinned WHERE group_id=$1 AND account_id=$2`, target.ID, ids[2])
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, copyErr := newGroupRepositoryWithSQL(client, db).CopyAccountMembers(ctx, target.ID, []int64{source.ID}, false, true)
		done <- copyErr
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query LIKE '/* ratefloor_copy_members */%' AND wait_event_type='Lock')`).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond, "copy must hold one SQL snapshot before changing the source")
	_, err = db.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,created_at) VALUES($1,$2,NOW())`, ids[1], source.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, ids[0], source.ID)
	require.NoError(t, err)
	require.NoError(t, holder.Commit())
	require.NoError(t, <-done)
	require.Equal(t, []int64{ids[0]}, ratefloorGroupMemberIDs(t, ctx, db, target.ID), "copy must retain its original A snapshot, never empty or combine A/B")
	require.Equal(t, []int64{ids[1]}, ratefloorGroupMemberIDs(t, ctx, db, source.ID))
}

func ratefloorGroupMemberIDs(t *testing.T, ctx context.Context, db *sql.DB, groupID int64) []int64 {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT account_id FROM account_groups WHERE group_id=$1 ORDER BY account_id`, groupID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func ratefloorWaitForLock(t *testing.T, ctx context.Context, db *sql.DB, marker string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE query LIKE $1 AND wait_event_type='Lock')`, "/* "+marker+" */%").Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond, "expected lock wait at %s", marker)
}

func runRatefloorMembershipLockOrder(t *testing.T, client *dbent.Client, db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	groups, accounts := []int64{}, []int64{}
	for _, name := range []string{"source-a", "source-b", "target"} {
		g, err := client.Group.Create().SetName(fmt.Sprintf("locks-%s-%d", name, suffix)).SetPlatform("openai").Save(ctx)
		require.NoError(t, err)
		groups = append(groups, g.ID)
	}
	for _, name := range []string{"A", "B"} {
		a, err := client.Account.Create().SetName(fmt.Sprintf("locks-%s-%d", name, suffix)).SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).Save(ctx)
		require.NoError(t, err)
		accounts = append(accounts, a.ID)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM account_groups WHERE group_id=ANY($1::bigint[])`, pq.Array(groups))
		_, _ = db.Exec(`DELETE FROM accounts WHERE id=ANY($1::bigint[])`, pq.Array(accounts))
		_, _ = db.Exec(`DELETE FROM groups WHERE id=ANY($1::bigint[])`, pq.Array(groups))
	})
	_, err := db.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,created_at) VALUES($1,$2,NOW()),($3,$4,NOW())`, accounts[0], groups[0], accounts[1], groups[1])
	require.NoError(t, err)
	repo := newGroupRepositoryWithSQL(client, db)

	t.Run("same_target_replacements_serialize", func(t *testing.T) {
		// Both transactions overlap on an initially empty target. Keep the first
		// replacement uncommitted until the second is waiting on its advisory lock.
		first, err := client.Tx(ctx)
		require.NoError(t, err)
		defer func() { _ = first.Rollback() }()
		count, err := newGroupRepositoryWithSQL(first.Client(), first).CopyAccountMembers(ctx, groups[2], groups[:1], false, true)
		require.NoError(t, err)
		require.EqualValues(t, 1, count)
		done := make(chan error, 1)
		go func() {
			count, err := repo.CopyAccountMembers(ctx, groups[2], groups[1:2], false, true)
			if err == nil && count != 1 {
				err = fmt.Errorf("second copy count: %d", count)
			}
			done <- err
		}()
		ratefloorWaitForLock(t, ctx, db, "ratefloor_member_replace_lock")
		require.NoError(t, first.Commit())
		require.NoError(t, <-done)
		require.Equal(t, accounts[1:2], ratefloorGroupMemberIDs(t, ctx, db, groups[2]), "second replacement must leave B alone, not A+B")
	})

	t.Run("pin_waits_before_locking_membership", func(t *testing.T) {
		// This models scoped account administration: account first, membership next.
		admin, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = admin.Rollback() }()
		_, err = admin.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, accounts[1])
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- repo.SetMemberPinned(ctx, groups[2], accounts[1], true) }()
		ratefloorWaitForLock(t, ctx, db, "ratefloor_membership_revision")
		_, err = admin.ExecContext(ctx, `SELECT id FROM groups WHERE id=$1 FOR SHARE`, groups[2])
		require.NoError(t, err)
		_, err = admin.ExecContext(ctx, `UPDATE account_groups SET pinned=false WHERE group_id=$1 AND account_id=$2`, groups[2], accounts[1])
		require.NoError(t, err, "admin must acquire membership without a deadlock with pin")
		require.NoError(t, admin.Commit())
		require.NoError(t, <-done)
		var pinned bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT pinned FROM account_groups WHERE group_id=$1 AND account_id=$2`, groups[2], accounts[1]).Scan(&pinned))
		require.True(t, pinned)
	})

	t.Run("target_member_after_account_snapshot_survives", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `DELETE FROM account_groups WHERE group_id=$1`, groups[2])
		require.NoError(t, err)
		holder, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = holder.Rollback() }()
		_, err = holder.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, accounts[0])
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			_, err := repo.CopyAccountMembers(ctx, groups[2], groups[:1], false, true)
			done <- err
		}()
		ratefloorWaitForLock(t, ctx, db, "ratefloor_copy_account_locks")
		// B was absent from the candidate snapshot, and can bind while A waits.
		bind, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = bind.Rollback() }()
		_, err = bind.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR NO KEY UPDATE`, accounts[1])
		require.NoError(t, err)
		_, err = bind.ExecContext(ctx, `SELECT id FROM groups WHERE id=$1 FOR SHARE`, groups[2])
		require.NoError(t, err)
		_, err = bind.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,created_at) VALUES($1,$2,NOW())`, accounts[1], groups[2])
		require.NoError(t, err)
		require.NoError(t, bind.Commit())
		require.NoError(t, holder.Commit())
		require.NoError(t, <-done)
		require.Equal(t, accounts, ratefloorGroupMemberIDs(t, ctx, db, groups[2]), "unlocked late member B must remain")
	})
}

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
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func runRatefloorSourceSnapshotBeforeAccountWait(t *testing.T, client *dbent.Client, db *sql.DB) {
	for _, duplicate := range []bool{false, true} {
		name := "copy_source_changes_while_waiting_for_account"
		if duplicate {
			name = "duplicate_source_changes_while_waiting_for_account"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			suffix := time.Now().UnixNano()
			source, err := client.Group.Create().SetName(fmt.Sprintf("snapshot-source-%d", suffix)).SetPlatform("openai").Save(ctx)
			require.NoError(t, err)
			target, err := client.Group.Create().SetName(fmt.Sprintf("snapshot-target-%d", suffix)).SetPlatform("openai").Save(ctx)
			require.NoError(t, err)
			ids := []int64{}
			for _, name := range []string{"A", "B", "C"} {
				a, err := client.Account.Create().SetName(fmt.Sprintf("snapshot-%s-%d", name, suffix)).SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).Save(ctx)
				require.NoError(t, err)
				ids = append(ids, a.ID)
			}
			clone := &service.Group{Name: fmt.Sprintf("snapshot-clone-%d", suffix), Platform: "openai", RateMultiplier: 1, Status: "inactive", SubscriptionType: "standard"}
			t.Cleanup(func() {
				groups := []int64{source.ID, target.ID, clone.ID}
				_, _ = db.Exec(`DELETE FROM account_groups WHERE group_id=ANY($1::bigint[])`, pq.Array(groups))
				_, _ = db.Exec(`DELETE FROM accounts WHERE id=ANY($1::bigint[])`, pq.Array(ids))
				_, _ = db.Exec(`DELETE FROM groups WHERE id=ANY($1::bigint[])`, pq.Array(groups))
			})
			_, err = db.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,pinned,created_at) VALUES($1,$2,TRUE,NOW()),($3,$4,FALSE,NOW())`, ids[0], source.ID, ids[2], target.ID)
			require.NoError(t, err)
			owner, err := client.Tx(ctx)
			require.NoError(t, err)
			defer func() { _ = owner.Rollback() }()
			_, err = owner.ExecContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, ids[0])
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() {
				repo := newGroupRepositoryWithSQL(client, db)
				if duplicate {
					done <- repo.CreateFromSource(ctx, clone, source.ID)
					return
				}
				count, err := repo.CopyAccountMembers(ctx, target.ID, []int64{source.ID}, false, true)
				if err == nil && count != 1 {
					err = fmt.Errorf("copy count = %d, want 1", count)
				}
				done <- err
			}()
			ratefloorWaitForLock(t, ctx, db, "ratefloor_copy_account_locks")
			// Use the actual binding path: source is {A}, then {A,B}, then {B}.
			require.NoError(t, newAccountRepositoryWithSQL(client, db, nil).BindGroups(ctx, ids[1], []int64{source.ID}))
			require.NoError(t, newAccountRepositoryWithSQL(owner.Client(), owner, nil).BindGroups(ctx, ids[0], nil))
			require.NoError(t, owner.Commit())
			require.NoError(t, <-done)
			targetID := target.ID
			if duplicate {
				targetID = clone.ID
				require.EqualValues(t, 1, clone.AccountCount)
			}
			require.Equal(t, ids[:1], ratefloorGroupMemberIDs(t, ctx, db, targetID), "copy must use frozen A, not empty or A+B")
			require.Equal(t, ids[1:2], ratefloorGroupMemberIDs(t, ctx, db, source.ID))
			var pinned bool
			require.NoError(t, db.QueryRowContext(ctx, `SELECT pinned FROM account_groups WHERE group_id=$1 AND account_id=$2`, targetID, ids[0]).Scan(&pinned))
			require.Equal(t, duplicate, pinned, "whole-group duplication must retain the frozen source pin")
		})
	}
}

func runRatefloorDeleteAccountConcurrentPin(t *testing.T, client *dbent.Client, db *sql.DB) {
	t.Run("delete_account_and_pin_do_not_deadlock", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		suffix := time.Now().UnixNano()
		g, err := client.Group.Create().SetName(fmt.Sprintf("delete-pin-%d", suffix)).SetPlatform("openai").Save(ctx)
		require.NoError(t, err)
		a, err := client.Account.Create().SetName(fmt.Sprintf("delete-pin-%d", suffix)).SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).Save(ctx)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM account_groups WHERE account_id=$1`, a.ID)
			_, _ = db.Exec(`DELETE FROM accounts WHERE id=$1`, a.ID)
			_, _ = db.Exec(`DELETE FROM groups WHERE id=$1`, g.ID)
		})
		_, err = db.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,created_at) VALUES($1,$2,NOW())`, a.ID, g.ID)
		require.NoError(t, err)
		gate, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = gate.Rollback() }()
		_, err = gate.ExecContext(ctx, `SELECT id FROM groups WHERE id=$1 FOR UPDATE`, g.ID)
		require.NoError(t, err)

		pinTx, err := client.Tx(ctx)
		require.NoError(t, err)
		defer func() { _ = pinTx.Rollback() }()
		var pinPID int
		require.NoError(t, scanSingleRow(ctx, pinTx, `SELECT pg_backend_pid()`, nil, &pinPID))
		pinDone := make(chan error, 1)
		go func() {
			err := newGroupRepositoryWithSQL(pinTx.Client(), pinTx).SetMemberPinned(ctx, g.ID, a.ID, true)
			if err == nil {
				err = pinTx.Commit()
			}
			pinDone <- err
		}()
		ratefloorWaitForLock(t, ctx, db, "account_group_live_group_lock")
		// Pin now owns the account but not the member. Exercise the actual service
		// entry point and ordinary repository Delete while that account is locked.
		accountRepo := newAccountRepositoryWithSQL(client, db, nil)
		admin := service.NewAdminService(
			nil, nil, nil, accountRepo, nil, nil,
			nil, nil, nil, nil, nil, nil,
			nil, client, nil, nil, nil, nil,
			nil, nil, nil, nil, nil,
		)
		deleteDone := make(chan error, 1)
		go func() { deleteDone <- admin.DeleteAccount(ctx, a.ID) }()
		require.Eventually(t, func() bool {
			var blocked bool
			err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pinPID).Scan(&blocked)
			return err == nil && blocked
		}, 5*time.Second, 10*time.Millisecond, "delete must be waiting for pin's account lock")
		// The old delete held the member here, causing a cycle when pin resumed.
		require.NoError(t, gate.Commit())
		require.NoError(t, <-pinDone)
		require.NoError(t, <-deleteDone)
		var members, live int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_groups WHERE account_id=$1`, a.ID).Scan(&members))
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id=$1 AND deleted_at IS NULL`, a.ID).Scan(&live))
		require.Zero(t, members)
		require.Zero(t, live)
	})
}

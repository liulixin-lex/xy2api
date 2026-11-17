//go:build unit || integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

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
	var selected int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT account_id FROM account_groups WHERE group_id=$1`, target.ID).Scan(&selected))
	require.Equal(t, ids[0], selected, "copy must retain its original A snapshot, never empty or combine A/B")
	var current int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT account_id FROM account_groups WHERE group_id=$1`, source.ID).Scan(&current))
	require.Equal(t, ids[1], current)
}

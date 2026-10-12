//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	dbmigrations "github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

const shareSchedulingMigration = "270_share_scheduling.sql"

func applyShareSchedulingMigration(ctx context.Context, t *testing.T, tx *sql.Tx) {
	t.Helper()
	body, err := dbmigrations.FS.ReadFile(shareSchedulingMigration)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(body))
	require.NoError(t, err)
}

func insertShareMigrationAccount(ctx context.Context, t *testing.T, tx *sql.Tx, name string, priority int) int64 {
	t.Helper()
	var id int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable,priority,credentials,extra)
VALUES($1,'anthropic','oauth','active',true,$2,'{}'::jsonb,'{}'::jsonb) RETURNING id`, name, priority).Scan(&id))
	return id
}

func TestShareSchedulingMigrationImportsDrafts(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, "DELETE FROM share_scheduling_policies")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "DELETE FROM scheduling_group_policies")
	require.NoError(t, err)

	var groupID, emptyGroupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,platform,rate_multiplier,status) VALUES('share-import','anthropic',1,'active') RETURNING id`).Scan(&groupID))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,platform,rate_multiplier,status) VALUES('share-empty','anthropic',1,'active') RETURNING id`).Scan(&emptyGroupID))
	explicit := insertShareMigrationAccount(ctx, t, tx, "share-explicit", 50)
	inherited := insertShareMigrationAccount(ctx, t, tx, "share-inherited", 7)
	zero := insertShareMigrationAccount(ctx, t, tx, "share-zero", 50)
	outsider := insertShareMigrationAccount(ctx, t, tx, "share-outsider", 50)
	deleted := insertShareMigrationAccount(ctx, t, tx, "share-deleted", 50)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, deleted)
	require.NoError(t, err)
	for _, id := range []int64{explicit, inherited, zero, deleted} {
		_, err = tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, id, groupID)
		require.NoError(t, err)
	}

	legacy := fmt.Sprintf(`{"group_id":%[1]d,"version":4,"accounts":[
		{"account_id":%[2]d,"priority":1,"traffic_weight":30},
		{"account_id":%[3]d,"traffic_weight":5000000},
		{"account_id":%[4]d,"priority":2,"traffic_weight":0},
		{"account_id":%[5]d,"priority":1,"traffic_weight":3},
		{"account_id":%[6]d,"priority":1,"traffic_weight":3},
		{"account_id":"%[2]d","priority":9,"traffic_weight":9},
		{"account_id":%[2]d,"priority":9,"traffic_weight":9},
		"malformed",
		{"account_id":-1,"priority":1,"traffic_weight":1},
		{"account_id":1.5,"priority":"x","traffic_weight":"y"}
	],"first_output_timeout_ms":120000,"total_wait_timeout_ms":240000,"max_attempts":3}`,
		groupID, explicit, inherited, zero, outsider, deleted)
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduling_group_policies(group_id,version,policy) VALUES($1,4,$2::jsonb),($3,1,'{"accounts":"broken"}'::jsonb),(0,2,$4::jsonb)`,
		groupID, legacy, emptyGroupID, fmt.Sprintf(`{"accounts":[{"account_id":%d,"priority":3,"traffic_weight":2}]}`, outsider))
	require.NoError(t, err)

	applyShareSchedulingMigration(ctx, t, tx)
	applyShareSchedulingMigration(ctx, t, tx) // replay keeps the first import

	read := func(group int64) (sharesched.Policy, string, string, int64) {
		var raw, scheduler, importedFrom string
		var version int64
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT policy::text,scheduler,imported_from,version FROM share_scheduling_policies WHERE group_id=$1`, group).
			Scan(&raw, &scheduler, &importedFrom, &version))
		policy, err := sharesched.DecodePolicy([]byte(raw))
		require.NoError(t, err, "imported drafts must decode under the strict policy rules: %s", raw)
		return policy, scheduler, importedFrom, version
	}

	policy, scheduler, importedFrom, version := read(groupID)
	require.Equal(t, "sub2api", scheduler, "imported drafts never change routing")
	require.Equal(t, sharesched.SchedulerSub2API, policy.Scheduler)
	require.Equal(t, "scheduling_group_policies:v4", importedFrom)
	require.Equal(t, int64(1), version)
	require.Equal(t, []sharesched.AccountRule{
		{AccountID: explicit, Tier: 1, Weight: 30},
		{AccountID: inherited, Tier: 7, Weight: 1_000_000},
		{AccountID: zero, Tier: 2, Weight: 1, Drain: true},
	}, policy.Accounts, "members only, first rule wins, weight 0 drains, missing priority inherits")

	policy, _, _, _ = read(emptyGroupID)
	require.Empty(t, policy.Accounts, "a malformed rule list imports as an empty draft")

	policy, _, _, _ = read(0)
	require.Equal(t, []sharesched.AccountRule{{AccountID: outsider, Tier: 3, Weight: 2}}, policy.Accounts, "group 0 keeps any live account")
}

func TestShareSchedulingMigrationWithoutLegacyTable(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, "DROP TABLE scheduling_group_policies")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "DELETE FROM share_scheduling_policies")
	require.NoError(t, err)
	applyShareSchedulingMigration(ctx, t, tx)
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM share_scheduling_policies").Scan(&count))
	require.Zero(t, count)

	_, err = tx.ExecContext(ctx, `INSERT INTO share_scheduling_policies(group_id,version,scheduler,policy) VALUES(1,1,'controlled','{}'::jsonb)`)
	require.Error(t, err, "the scheduler column only accepts known schedulers")
}

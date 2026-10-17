//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

const balanceAlertRulesRemovalMigration = "277_remove_balance_alert_rules.sql"

// 277 removes the two balance alert rules 276 seeded; a rule is removed only
// when its name and metric type both match, so rules an administrator
// renamed or repointed stay.
func TestMigration277RemovesOnlyTheSeededBalanceAlertRules(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	insert := func(name, metric string) {
		t.Helper()
		_, err := tx.ExecContext(ctx, `
INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES ($1, '', true, $2, '>', 0, 5, 1, 'P2', true, 60, NOW(), NOW())`, name, metric)
		require.NoError(t, err)
	}
	count := func(name, metric string) int {
		t.Helper()
		var n int
		require.NoError(t, tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM ops_alert_rules WHERE name = $1 AND metric_type = $2", name, metric).Scan(&n))
		return n
	}
	apply := func() {
		t.Helper()
		content, err := dbmigrations.FS.ReadFile(balanceAlertRulesRemovalMigration)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(content))
		require.NoError(t, err)
	}

	// The harness applied every migration: 276 seeded the rules, 277 removed them.
	require.Zero(t, count("上游余额不足", "account_balance_exhausted_count"))
	require.Zero(t, count("上游余额长期未恢复", "account_balance_unrecovered_count"))

	insert("上游余额不足", "account_balance_exhausted_count")
	insert("上游余额长期未恢复", "account_balance_unrecovered_count")
	insert("自定义余额监控", "account_balance_exhausted_count")
	apply()
	require.Zero(t, count("上游余额不足", "account_balance_exhausted_count"))
	require.Zero(t, count("上游余额长期未恢复", "account_balance_unrecovered_count"))
	require.Equal(t, 1, count("自定义余额监控", "account_balance_exhausted_count"), "a renamed rule stays")

	insert("上游余额不足", "account_error_count")
	apply()
	require.Equal(t, 1, count("上游余额不足", "account_error_count"), "a repointed rule stays")
}

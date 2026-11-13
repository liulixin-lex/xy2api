//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestSchedV2DecisionStateTransactionAndReset(t *testing.T) {
	r, a, g := qualityRepoFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	basis := q.DecisionBasis{RuleVersion: q.RuleVersion, Dims: []q.Dimension{q.DimensionTTFO, q.DimensionError}, AttemptIDs: []string{"synthetic-attempt"}}
	row := q.StateRow{AccountID: a, GroupID: g, Model: "synthetic-model", State: q.StateCooling, Level: 1, Reason: q.ReasonSpeed, Since: at, Until: at.Add(20 * time.Minute), EvidenceSince: at.Add(-time.Hour), Basis: &basis}
	decision := q.Decision{At: at, Scope: q.Scope{AccountID: a, GroupID: g, Model: row.Model}, Action: "enter", Level: 1, Until: row.Until, Basis: basis}
	t.Cleanup(func() {
		_, err := integrationDB.Exec("DELETE FROM sched_decisions WHERE account_id=$1", a)
		require.NoError(t, err)
	})
	failure := errors.New("rollback evidence")
	_, err := r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		if err := tx.Upsert([]q.StateRow{row}); err != nil {
			return err
		}
		if err := tx.InsertDecisions([]q.Decision{decision}); err != nil {
			return err
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	var n int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM sched_states WHERE account_id=$1", a).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM sched_decisions WHERE account_id=$1", a).Scan(&n))
	require.Zero(t, n)
	_, err = r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		if err := tx.Upsert([]q.StateRow{row}); err != nil {
			return err
		}
		return tx.InsertDecisions([]q.Decision{decision})
	})
	require.NoError(t, err)
	rows, _, _, err := r.LoadRows(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, basis.Dims, rows[0].Basis.Dims)
	require.WithinDuration(t, row.EvidenceSince, rows[0].EvidenceSince, time.Microsecond)
	reset, err := r.RunReset(ctx, a)
	require.NoError(t, err)
	require.False(t, reset.Before(at))
	rows, _, _, err = r.LoadRows(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
	var action, version string
	require.NoError(t, integrationDB.QueryRow("SELECT action,rule_version FROM sched_decisions WHERE account_id=$1 ORDER BY id DESC LIMIT 1", a).Scan(&action, &version))
	require.Equal(t, "reset", action)
	require.Equal(t, q.RuleVersion, version)
}

func TestSchedV2MigrationPreservesOldSchemaAndPolicies(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	raw := `{"version":4,"groups":{"1":{"v4":{"enabled":true,"ttft_seconds":12,"max_error_rate":0.01,"min_cache_ratio":0.8,"takeover_idle_seconds":0},"extra":"keep"},"2":{"v3":{"enabled":true,"max_ttft_p90_seconds":15,"max_error_rate":0.02,"min_cache_keep_rate":0.7}},"3":{"enabled":true}}}`
	_, err = tx.Exec("INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", service.SettingKeyGroupQualityGatePolicies, raw)
	require.NoError(t, err)
	sql, err := migrations.FS.ReadFile("301_sv2_policy_values.sql")
	require.NoError(t, err)
	_, err = tx.Exec(string(sql))
	require.NoError(t, err)
	var encoded string
	require.NoError(t, tx.QueryRow("SELECT value FROM settings WHERE key=$1", service.SettingKeyGroupQualityGatePolicies).Scan(&encoded))
	var body struct {
		Version int
		Groups  map[string]map[string]json.RawMessage
	}
	require.NoError(t, json.Unmarshal([]byte(encoded), &body))
	require.Equal(t, 5, body.Version)
	require.Contains(t, body.Groups["2"], "v3")
	require.Contains(t, body.Groups["3"], "enabled")
	for _, id := range []string{"1", "2", "3"} {
		var policy schedQualityPolicyV4
		require.NoError(t, json.Unmarshal(body.Groups[id]["v4"], &policy))
		require.GreaterOrEqual(t, policy.MaxErrorRate, .05)
		if id == "1" {
			require.Equal(t, 0, policy.TakeoverIdleSeconds)
		} else {
			require.Equal(t, 900, policy.TakeoverIdleSeconds)
		}
	}
	for _, table := range []string{"account_quality_states", "account_quality_tests", "account_quality_overrides", "sched_states", "sched_decisions"} {
		var present bool
		require.NoError(t, tx.QueryRow("SELECT to_regclass($1) IS NOT NULL", table).Scan(&present))
		require.True(t, present, table)
	}
	require.NotEqual(t, int64(734891260129), schedQualityCycleLockID)
}

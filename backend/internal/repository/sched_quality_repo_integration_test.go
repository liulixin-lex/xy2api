//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func qualityRepoFixture(t *testing.T) (*schedQualityRepository, int64, int64) {
	t.Helper()
	ctx := context.Background()
	r := &schedQualityRepository{db: integrationDB}
	var account, group int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra) VALUES('quality','openai','apikey','{}','{}') RETURNING id`).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('quality','openai') RETURNING id`).Scan(&group))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, account, group)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, query := range []string{`DELETE FROM account_quality_states WHERE account_id=$1`, `DELETE FROM account_quality_overrides WHERE account_id=$1`, `DELETE FROM account_groups WHERE account_id=$1`, `DELETE FROM accounts WHERE id=$1`} {
			_, err := integrationDB.ExecContext(ctx, query, account)
			require.NoError(t, err)
		}
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, group)
		require.NoError(t, err)
	})
	return r, account, group
}
func TestSchedQualityRunCycleLockAndRollback(t *testing.T) {
	r, a, g := qualityRepoFixture(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	type result struct {
		locked bool
		err    error
	}
	done := make(chan result, 1)
	row := q.StateRow{AccountID: a, GroupID: g, Model: "m", State: q.StateCooling, Level: 1, Reason: q.ReasonSpeed, Since: time.Now(), Until: time.Now().Add(time.Minute)}
	go func() {
		locked, err := r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
			if err := tx.Upsert([]q.StateRow{row}); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
		done <- result{locked, err}
	}()
	<-entered
	locked, err := r.RunCycle(ctx, func(service.SchedQualityTx) error { t.Error("loser ran callback"); return nil })
	require.NoError(t, err)
	require.False(t, locked)
	close(release)
	got := <-done
	require.NoError(t, got.err)
	require.True(t, got.locked)
	states, _, _, err := r.LoadRows(ctx)
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.Equal(t, row.Level, states[0].Level)
	locked, err = r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		require.NoError(t, tx.Delete([]q.StateRow{row}))
		return fmt.Errorf("rollback")
	})
	require.True(t, locked)
	require.Error(t, err)
	states, _, _, err = r.LoadRows(ctx)
	require.NoError(t, err)
	require.Len(t, states, 1)
}
func TestSchedQualityResetUsesDatabaseTimeAfterLock(t *testing.T) {
	r, a, g := qualityRepoFixture(t)
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, schedQualityCycleLockID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO account_quality_states(account_id,model,group_id,state,level,reason,since,until) VALUES($1,'m',$2,1,1,'speed',NOW(),NOW()+INTERVAL '1 minute')`, a, g)
	require.NoError(t, err)
	var begin time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&begin))
	type result struct {
		at  time.Time
		err error
	}
	done := make(chan result, 1)
	go func() { at, err := r.RunReset(ctx, a); done <- result{at, err} }()
	// Observe PostgreSQL's actual wait rather than scheduling by sleeping.
	deadline := time.Now().Add(2 * time.Second)
	waiting := false
	for !waiting && time.Now().Before(deadline) {
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted)`).Scan(&waiting))
	}
	require.True(t, waiting, "reset must wait for the evaluation lock")
	var releaseAt time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&releaseAt))
	require.NoError(t, tx.Commit())
	got := <-done
	require.NoError(t, got.err)
	require.True(t, got.at.After(begin))
	require.False(t, got.at.Before(releaseAt))
	states, _, resets, err := r.LoadRows(ctx)
	require.NoError(t, err)
	require.Empty(t, states)
	require.True(t, got.at.Equal(resets[a]))
}
func TestSchedQualityPolicyConflictAndPreservesRawJSON(t *testing.T) {
	r, _, g := qualityRepoFixture(t)
	ctx := context.Background()
	var g2, other int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('empty quality','openai') RETURNING id`).Scan(&g2))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('other','anthropic') RETURNING id`).Scan(&other))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key=$1`, service.SettingKeyGroupQualityGatePolicies)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id IN ($1,$2)`, g2, other)
		require.NoError(t, err)
	})
	v3 := `{ "enabled" : true, "max_ttft_p90_seconds":15, "max_error_rate":0.10, "min_cache_keep_rate":0.50, "unknown" : [ 1, 2 ] }`
	unknown := `{ "legacy" : [ true, {"x": 2} ] }`
	raw := fmt.Sprintf(`{"version":7,"groups":{"%d":{"v3":%s,"future":%s},"%d":{"v3":%s}},"root_unknown": %s}`, g, v3, unknown, g2, v3, unknown)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, service.SettingKeyGroupQualityGatePolicies, raw)
	require.NoError(t, err)
	policies, err := r.LoadPolicies(ctx)
	require.NoError(t, err)
	require.True(t, policies[g].FromV3)
	require.Equal(t, int64(7), policies[g].Version)
	require.Equal(t, float64(15), policies[g].Policy.TTFTSeconds)
	p := q.Policy{Enabled: true, TTFTSeconds: 12.5, MaxErrorRate: .05, MinCacheRatio: .8, TakeoverIdleSeconds: 900}
	saved, err := r.SavePolicy(ctx, g, 7, p)
	require.NoError(t, err)
	require.Equal(t, int64(8), saved.Version)
	_, err = r.SavePolicy(ctx, g2, 7, p)
	require.ErrorIs(t, err, service.ErrSchedPolicyConflict)
	_, err = r.SavePolicy(ctx, other, 8, p)
	require.Error(t, err)
	var after string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyGroupQualityGatePolicies).Scan(&after))
	require.Equal(t, v3, gjson.Get(after, fmt.Sprintf("groups.%d.v3", g)).Raw)
	require.Equal(t, unknown, gjson.Get(after, fmt.Sprintf("groups.%d.future", g)).Raw)
	require.Equal(t, unknown, gjson.Get(after, "root_unknown").Raw)
	policies, err = r.LoadPolicies(ctx)
	require.NoError(t, err)
	require.Equal(t, p, policies[g].Policy)
	require.False(t, policies[g].FromV3)
	require.Equal(t, int64(8), policies[g2].Version)
	members, err := r.LoadMembers(ctx)
	require.NoError(t, err)
	require.Contains(t, members, g2)
	require.Empty(t, members[g2])
}
func TestSchedQualityConcurrentPolicyCAS(t *testing.T) {
	r, _, g := qualityRepoFixture(t)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key=$1`, service.SettingKeyGroupQualityGatePolicies)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key=$1`, service.SettingKeyGroupQualityGatePolicies)
		require.NoError(t, err)
	})
	p := q.Policy{TTFTSeconds: 10}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; _, err := r.SavePolicy(ctx, g, 0, p); results <- err }()
	}
	close(start)
	var passed, conflicts int
	for range 2 {
		err := <-results
		if err == nil {
			passed++
		} else {
			require.ErrorIs(t, err, service.ErrSchedPolicyConflict)
			conflicts++
		}
	}
	require.Equal(t, 1, passed)
	require.Equal(t, 1, conflicts)
}
func TestSchedQualityOverrideCleanupComparesExpiry(t *testing.T) {
	r, a, g := qualityRepoFixture(t)
	ctx := context.Background()
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	old := service.SchedOverride{AccountID: a, GroupID: g, Mode: q.OverrideDemote, ExpiresAt: &past}
	require.NoError(t, r.UpsertOverride(ctx, old))
	updated := old
	updated.ExpiresAt = &future
	require.NoError(t, r.UpsertOverride(ctx, updated))
	locked, err := r.RunCycle(ctx, func(tx service.SchedQualityTx) error { return tx.DeleteOverrides([]service.SchedOverride{old}) })
	require.True(t, locked)
	require.NoError(t, err)
	_, overrides, _, err := r.LoadRows(ctx)
	require.NoError(t, err)
	require.Len(t, overrides, 1)
	require.WithinDuration(t, future, *overrides[0].ExpiresAt, time.Microsecond)
	require.NoError(t, r.UpsertOverride(ctx, old))
	_, err = r.RunCycle(ctx, func(tx service.SchedQualityTx) error { return tx.DeleteOverrides([]service.SchedOverride{old}) })
	require.NoError(t, err)
	_, overrides, _, err = r.LoadRows(ctx)
	require.NoError(t, err)
	require.Empty(t, overrides)
	// Leaving the group also deletes its permanent override; the all-group one remains.
	require.NoError(t, r.UpsertOverride(ctx, service.SchedOverride{AccountID: a, GroupID: g, Mode: q.OverrideExempt}))
	require.NoError(t, r.UpsertOverride(ctx, service.SchedOverride{AccountID: a, Mode: q.OverrideDemote}))
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, a)
	require.NoError(t, err)
	_, err = r.RunCycle(ctx, func(tx service.SchedQualityTx) error {
		return tx.DeleteOverrides([]service.SchedOverride{{AccountID: a, GroupID: g}})
	})
	require.NoError(t, err)
	_, overrides, _, err = r.LoadRows(ctx)
	require.NoError(t, err)
	require.Len(t, overrides, 1)
	require.Zero(t, overrides[0].GroupID)
}
func TestSchedQualityLoadRowsConsistentRead(t *testing.T) {
	r, a, g := qualityRepoFixture(t)
	ctx := context.Background()
	write := func(level int) error {
		tx, err := integrationDB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		at := time.Unix(int64(level), 0).UTC()
		_, err = tx.ExecContext(ctx, `INSERT INTO account_quality_states(account_id,model,group_id,state,level,reason,since) VALUES($1,'consistent',$2,0,$3,'',$4) ON CONFLICT(account_id,model,group_id) DO UPDATE SET level=$3,since=$4`, a, g, level, at)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_quality_overrides(account_id,group_id,mode,reason) VALUES($1,0,1,$2) ON CONFLICT(account_id,group_id) DO UPDATE SET reason=$2`, a, fmt.Sprint(level))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{group_quality_reset_at}',to_jsonb($2::text)) WHERE id=$1`, a, at.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	require.NoError(t, write(1))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	writerErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := write(i%2 + 1); err != nil {
				writerErr <- err
				return
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
		select {
		case err := <-writerErr:
			require.NoError(t, err)
		default:
		}
	}()
	for range 50 {
		states, overrides, resets, err := r.LoadRows(ctx)
		require.NoError(t, err)
		require.Len(t, states, 1)
		require.Len(t, overrides, 1)
		require.Equal(t, fmt.Sprint(states[0].Level), overrides[0].Reason)
		require.Equal(t, int64(states[0].Level), resets[a].Unix())
	}
}

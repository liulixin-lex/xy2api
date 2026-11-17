//go:build unit || integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func runRatefloorMemberPersistence(t *testing.T, client *dbent.Client, exec sqlExecutor) {
	t.Helper()
	ctx := context.Background()
	_, err := exec.ExecContext(ctx, `CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT,account_id BIGINT,group_id BIGINT,payload JSONB,dedup_key TEXT,created_at TIMESTAMPTZ DEFAULT NOW());CREATE UNIQUE INDEX ratefloor_outbox_dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL`)
	require.NoError(t, err)
	g1, err := client.Group.Create().SetName("source").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	g2, err := client.Group.Create().SetName("target").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	g3, err := client.Group.Create().SetName("removed").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	a, err := client.Account.Create().SetName("synthetic").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).Save(ctx)
	require.NoError(t, err)
	repo := newAccountRepositoryWithSQL(client, exec, nil)
	groups := newGroupRepositoryWithSQL(client, exec)
	require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{g1.ID, g3.ID}))
	require.NoError(t, groups.SetMemberPinned(ctx, g1.ID, a.ID, true))
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = exec.ExecContext(ctx, `UPDATE account_groups SET created_at=$3 WHERE account_id=$1 AND group_id=$2`, a.ID, g1.ID, created)
	require.NoError(t, err)
	require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{g1.ID, g2.ID, g2.ID}))
	var pin bool
	var at time.Time
	require.NoError(t, scanSingleRow(ctx, exec, `SELECT pinned,created_at FROM account_groups WHERE account_id=$1 AND group_id=$2`, []any{a.ID, g1.ID}, &pin, &at))
	require.True(t, pin)
	require.True(t, at.Equal(created))
	loaded, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, loaded.PinnedIn(g1.ID))
	require.False(t, loaded.PinnedIn(g2.ID))
	require.False(t, loaded.PinnedIn(g3.ID))
	byGroup, err := repo.queryAccountsByGroup(ctx, g1.ID, accountGroupQueryOptions{})
	require.NoError(t, err)
	require.True(t, byGroup[0].PinnedIn(g1.ID))
	slim := buildSchedulerMetadataAccount(*loaded)
	require.True(t, slim.PinnedIn(g1.ID))
	require.NoError(t, groups.CopyAccountMembers(ctx, g2.ID, []int64{g1.ID}, []int64{a.ID}, true))
	loaded, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, loaded.PinnedIn(g2.ID), "retained unpinned target must not inherit source pin")
	require.NoError(t, groups.CopyAccountMembers(ctx, g3.ID, []int64{g1.ID}, []int64{a.ID}, false))
	loaded, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, loaded.PinnedIn(g3.ID), "new target membership starts unpinned")
	require.NoError(t, groups.SetMemberPinned(ctx, g2.ID, a.ID, true))
	require.NoError(t, groups.SetMemberPinned(ctx, g1.ID, a.ID, false))
	require.NoError(t, groups.CopyAccountMembers(ctx, g2.ID, []int64{g1.ID}, []int64{a.ID}, true))
	loaded, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, loaded.PinnedIn(g2.ID), "target pin survives an unpinned source")
	clone := &service.Group{Name: "copy", Platform: "openai", RateMultiplier: 1, Status: "inactive", SubscriptionType: "standard"}
	require.NoError(t, groups.CreateFromSource(ctx, clone, g2.ID))
	loaded, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, loaded.PinnedIn(clone.ID))
	require.NoError(t, repo.BindGroups(ctx, a.ID, nil))
	loaded, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, loaded.AccountGroups)
	var events int
	require.NoError(t, scanSingleRow(ctx, exec, `SELECT count(*) FROM scheduler_outbox WHERE event_type=$1 AND account_id=$2`, []any{service.SchedulerOutboxEventAccountGroupsChanged, a.ID}, &events))
	require.Positive(t, events)
	// The actual migration invalidates the auth snapshot even for direct SQL.
	_, err = exec.ExecContext(ctx, `CREATE TABLE auth_cache_invalidation_outbox(cache_key TEXT); CREATE TABLE api_keys(key TEXT,group_id BIGINT,deleted_at TIMESTAMPTZ)`)
	require.NoError(t, err)
	body, err := migrations.FS.ReadFile("318_group_ratefloor_auth_cache_invalidation.sql")
	require.NoError(t, err)
	_, err = exec.ExecContext(ctx, string(body))
	require.NoError(t, err)
	_, err = exec.ExecContext(ctx, `CREATE TRIGGER ratefloor_auth AFTER UPDATE ON groups FOR EACH ROW EXECUTE FUNCTION enqueue_group_auth_cache_invalidation()`)
	require.NoError(t, err)
	_, err = exec.ExecContext(ctx, `INSERT INTO api_keys(key,group_id) VALUES ('synthetic-key',$1)`, g1.ID)
	require.NoError(t, err)
	_, err = exec.ExecContext(ctx, `UPDATE groups SET name='renamed' WHERE id=$1`, g1.ID)
	require.NoError(t, err)
	var invalidations int
	require.NoError(t, scanSingleRow(ctx, exec, `SELECT count(*) FROM auth_cache_invalidation_outbox`, []any{}, &invalidations))
	require.Zero(t, invalidations)
	_, err = exec.ExecContext(ctx, `UPDATE groups SET min_upstream_rate=0.4 WHERE id=$1`, g1.ID)
	require.NoError(t, err)
	require.NoError(t, scanSingleRow(ctx, exec, `SELECT count(*) FROM auth_cache_invalidation_outbox`, []any{}, &invalidations))
	require.Equal(t, 1, invalidations)
	readGroup, err := groups.GetByIDLite(ctx, g1.ID)
	require.NoError(t, err)
	require.Equal(t, 0.4, *readGroup.MinUpstreamRate)
	readGroup.MinUpstreamRate = nil
	require.NoError(t, groups.Update(ctx, readGroup))
	readGroup, err = groups.GetByIDLite(ctx, g1.ID)
	require.NoError(t, err)
	require.Nil(t, readGroup.MinUpstreamRate)

	t.Log("retained pin and created_at survive BindGroups; source copy, group duplicate, scheduler projection and removal verified")
}

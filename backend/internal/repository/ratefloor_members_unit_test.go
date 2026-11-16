//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/migrate"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestRatefloorMemberPersistence(t *testing.T) {
	for _, bin := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("local PostgreSQL executables unavailable")
		}
	}
	dir, err := os.MkdirTemp("", "ratefloor-pg-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data := filepath.Join(dir, "data")
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("initdb", "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", fmt.Sprintf("-h '' -k %s -p %d", dir, port), "-w", "start")
	t.Cleanup(func() { run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop") })
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d dbname=postgres sslmode=disable", dir, port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	ctx := context.Background()
	require.NoError(t, migrate.Create(ctx, migrate.NewSchema(driver), []*schema.Table{migrate.ProxiesTable, migrate.AccountsTable, migrate.GroupsTable, migrate.AccountGroupsTable}, migrate.WithForeignKeys(false)))
	_, err = db.Exec(`CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT,account_id BIGINT,group_id BIGINT,payload JSONB,dedup_key TEXT,created_at TIMESTAMPTZ DEFAULT NOW());CREATE UNIQUE INDEX ratefloor_outbox_dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL`)
	require.NoError(t, err)
	g1, err := client.Group.Create().SetName("source").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	g2, err := client.Group.Create().SetName("target").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	g3, err := client.Group.Create().SetName("removed").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	a, err := client.Account.Create().SetName("synthetic").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).Save(ctx)
	require.NoError(t, err)
	repo := newAccountRepositoryWithSQL(client, db, nil)
	groups := newGroupRepositoryWithSQL(client, db)
	require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{g1.ID, g3.ID}))
	require.NoError(t, groups.SetMemberPinned(ctx, g1.ID, a.ID, true))
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = db.Exec(`UPDATE account_groups SET created_at=$3 WHERE account_id=$1 AND group_id=$2`, a.ID, g1.ID, created)
	require.NoError(t, err)
	require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{g1.ID, g2.ID, g2.ID}))
	var pin bool
	var at time.Time
	require.NoError(t, db.QueryRow(`SELECT pinned,created_at FROM account_groups WHERE account_id=$1 AND group_id=$2`, a.ID, g1.ID).Scan(&pin, &at))
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
	require.True(t, loaded.PinnedIn(g2.ID))
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
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM scheduler_outbox WHERE event_type=$1 AND account_id=$2`, service.SchedulerOutboxEventAccountGroupsChanged, a.ID).Scan(&events))
	require.Positive(t, events)
	// The actual migration invalidates the auth snapshot even for direct SQL.
	_, err = db.Exec(`CREATE TABLE auth_cache_invalidation_outbox(cache_key TEXT); CREATE TABLE api_keys(key TEXT,group_id BIGINT,deleted_at TIMESTAMPTZ)`)
	require.NoError(t, err)
	body, err := migrations.FS.ReadFile("318_group_ratefloor_auth_cache_invalidation.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(body))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER ratefloor_auth AFTER UPDATE ON groups FOR EACH ROW EXECUTE FUNCTION enqueue_group_auth_cache_invalidation()`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO api_keys(key,group_id) VALUES ('synthetic-key',$1)`, g1.ID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE groups SET name='renamed' WHERE id=$1`, g1.ID)
	require.NoError(t, err)
	var invalidations int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auth_cache_invalidation_outbox`).Scan(&invalidations))
	require.Zero(t, invalidations)
	_, err = db.Exec(`UPDATE groups SET min_upstream_rate=0.4 WHERE id=$1`, g1.ID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auth_cache_invalidation_outbox`).Scan(&invalidations))
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

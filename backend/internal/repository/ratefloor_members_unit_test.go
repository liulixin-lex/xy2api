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

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/ent/migrate"
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
	runRatefloorMemberPersistence(t, client, db)
	runRatefloorCopyConcurrentSourceChange(t, client, db)
}

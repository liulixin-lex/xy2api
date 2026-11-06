//go:build integration

package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReaderPostgresStartupRejectsWritableIdentities(t *testing.T) {
	writer, u := coreLocalPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// This positive case uses the full migrated application, including ordinary
	// PUBLIC-executable trigger functions, and the real deployment column grants.
	_, e := writer.ExecContext(ctx, `DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='xy2api_mcp_reader') THEN CREATE ROLE xy2api_mcp_reader LOGIN PASSWORD 'mcp-reader-local' NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE; END IF; END $$; GRANT CONNECT ON DATABASE mcp_readonly_test TO xy2api_mcp_reader; GRANT USAGE ON SCHEMA public TO xy2api_mcp_reader`)
	require.NoError(t, e)
	grants, e := os.ReadFile("../../../deploy/mcp-reader-grants.sql")
	require.NoError(t, e)
	_, e = writer.ExecContext(ctx, string(grants))
	require.NoError(t, e)
	functions, e := os.ReadFile("../../../deploy/mcp-reader-function-privileges.sql")
	require.NoError(t, e)
	_, e = writer.ExecContext(ctx, `SELECT set_config('mcp_setup.writer_role',current_user,false);`+string(functions))
	require.NoError(t, e)
	openRole := func(role string) *sql.DB {
		t.Helper()
		copy := *u
		copy.User = url.UserPassword(role, "mcp-reader-local")
		db, e := sql.Open("postgres", copy.String())
		require.NoError(t, e)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	require.NoError(t, verifyReader(ctx, openRole("xy2api_mcp_reader"), writer))
	require.Error(t, verifyReader(ctx, writer, writer))
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1000000000)
	schema := "mcp_guard_" + suffix
	_, e = writer.ExecContext(ctx, `CREATE SCHEMA `+schema+`; CREATE TABLE `+schema+`.records(id integer,payload text); CREATE FUNCTION `+schema+`.dangerous() RETURNS void LANGUAGE sql SECURITY DEFINER AS 'INSERT INTO `+schema+`.records(id) VALUES(1)'; REVOKE ALL ON FUNCTION `+schema+`.dangerous() FROM PUBLIC`)
	require.NoError(t, e)
	roles := []string{}
	t.Cleanup(func() {
		_, _ = writer.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		for _, role := range roles {
			_, _ = writer.Exec(`DROP OWNED BY ` + role + `; DROP ROLE ` + role)
		}
	})
	newRole := func(name string) string {
		role := "mcp_guard_" + name + "_" + suffix
		roles = append(roles, role)
		_, e = writer.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD 'mcp-reader-local' NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE; GRANT CONNECT ON DATABASE mcp_readonly_test TO `+role+`; GRANT USAGE ON SCHEMA `+schema+` TO `+role)
		require.NoError(t, e)
		return role
	}
	for _, tc := range []struct{ name, grant string }{
		{"column", `GRANT UPDATE(payload) ON ` + schema + `.records TO %s`},
		{"schema", `GRANT CREATE ON SCHEMA ` + schema + ` TO %s`},
		{"owner", `CREATE TABLE ` + schema + `.owned_%s(id integer)`},
		{"function", `GRANT EXECUTE ON FUNCTION ` + schema + `.dangerous() TO %s`},
		{"member", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role := newRole(tc.name)
			switch tc.name {
			case "owner":
				_, e = writer.ExecContext(ctx, `CREATE TABLE `+schema+`.owned(id integer); ALTER TABLE `+schema+`.owned OWNER TO `+role+`; REVOKE ALL ON `+schema+`.owned FROM `+role)
			case "member":
				parent := newRole("writer")
				_, e = writer.ExecContext(ctx, `GRANT UPDATE ON `+schema+`.records TO `+parent+`; GRANT `+parent+` TO `+role)
			default:
				_, e = writer.ExecContext(ctx, fmt.Sprintf(tc.grant, role))
			}
			require.NoError(t, e)
			require.ErrorContains(t, verifyReader(ctx, openRole(role), writer), "reader")
		})
	}
	// SECURITY DEFINER executability on a shared PUBLIC grant is rejected too;
	// no runtime REVOKE is performed by the application.
	_, e = writer.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schema+`.dangerous() TO PUBLIC`)
	require.NoError(t, e)
	require.Error(t, verifyReader(ctx, openRole("xy2api_mcp_reader"), writer))
}

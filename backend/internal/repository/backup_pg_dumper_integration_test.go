//go:build integration

package repository

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPgDumperRealPostgresBackupAndAtomicRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("backup_test"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("fixture"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	dumper := &PgDumper{
		cfg: &config.DatabaseConfig{Host: "127.0.0.1", Port: 5432, DBName: "backup_test", User: "postgres", Password: "fixture", SSLMode: "disable"},
		db:  db,
		commandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			// Execute the actual version-matched client inside the dedicated test
			// database container; no host PostgreSQL tools are required in CI.
			if name == "pg_restore" {
				// SQL rendering needs no database connection. Bind only the two
				// private test files, never the host's whole temporary directory.
				dockerArgs := []string{"run", "--rm", "--network", "none", "--read-only", "--entrypoint", name}
				for _, arg := range args {
					path := strings.TrimPrefix(arg, "--use-list=")
					if strings.HasSuffix(path, ".dump") || strings.HasPrefix(arg, "--use-list=") {
						dockerArgs = append(dockerArgs, "--volume", path+":"+path+":ro")
					}
				}
				dockerArgs = append(dockerArgs, postgresImageTag)
				return exec.CommandContext(ctx, "docker", append(dockerArgs, args...)...)
			}
			dockerArgs := []string{"exec", "-i", "--env", "PGPASSWORD", container.GetContainerID(), name}
			return exec.CommandContext(ctx, "docker", append(dockerArgs, args...)...)
		},
	}
	_, err = db.ExecContext(ctx, `CREATE EXTENSION pg_trgm;
		CREATE TABLE public.backup_marker(id integer PRIMARY KEY);
		INSERT INTO public.backup_marker VALUES (1);
		CREATE TABLE public.extension_marker(value text);
		INSERT INTO public.extension_marker VALUES ('DROP SCHEMA public;');
		CREATE INDEX extension_marker_trgm ON public.extension_marker USING gin(value gin_trgm_ops);
		CREATE FUNCTION public.backup_function() RETURNS text LANGUAGE sql AS $$SELECT 'CREATE SCHEMA public;'$$;
		CREATE TYPE public.backup_kind AS ENUM ('first', 'second');
		CREATE SEQUENCE public.backup_sequence START 42;
		CREATE SCHEMA external;
		CREATE TABLE external.extra_marker(id integer PRIMARY KEY);
		INSERT INTO external.extra_marker VALUES (2);`)
	require.NoError(t, err)
	reader, err := dumper.Dump(ctx)
	require.NoError(t, err)
	dump, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Contains(t, string(dump), "external.extra_marker", "backup must retain non-public schemas")

	_, err = db.ExecContext(ctx, "DELETE FROM public.backup_marker; DELETE FROM external.extra_marker")
	require.NoError(t, err)
	require.NoError(t, dumper.Restore(ctx, bytes.NewReader(dump)))
	var id int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM public.backup_marker").Scan(&id))
	require.Equal(t, 1, id)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM external.extra_marker").Scan(&id))
	require.Equal(t, 2, id)

	err = dumper.Restore(ctx, strings.NewReader("INSERT INTO public.backup_marker VALUES (99);\nSELECT * FROM missing_restore_relation;\n"))
	require.ErrorContains(t, err, "missing_restore_relation")
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM public.backup_marker").Scan(&count))
	require.Equal(t, 1, count, "failed restore must roll back the preceding INSERT")

	_, err = db.ExecContext(ctx, `CREATE ROLE application_owner LOGIN PASSWORD 'fixture';
		ALTER DATABASE backup_test OWNER TO application_owner;
		ALTER TABLE public.backup_marker OWNER TO application_owner;
		ALTER TABLE public.extension_marker OWNER TO application_owner;
		ALTER FUNCTION public.backup_function() OWNER TO application_owner;
		ALTER TYPE public.backup_kind OWNER TO application_owner;
		ALTER SEQUENCE public.backup_sequence OWNER TO application_owner;
		CREATE ROLE backup_limited LOGIN PASSWORD 'fixture';
		GRANT CONNECT ON DATABASE backup_test TO backup_limited;
		GRANT USAGE ON SCHEMA public TO backup_limited;
		GRANT SELECT ON ALL TABLES IN SCHEMA public TO backup_limited;
		GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO backup_limited;`)
	require.NoError(t, err)
	dumper.cfg.User = "backup_limited"
	reader, err = dumper.Dump(ctx)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, reader)
	require.NoError(t, err)
	err = reader.Close()
	require.ErrorContains(t, err, "permission denied for schema external")
	require.NotContains(t, err.Error(), "fixture")

	// An explicit public-only backup works with the application role even when
	// unrelated schemas are inaccessible. Its restore leaves external data intact.
	reader, err = dumper.DumpSchema(ctx, "public")
	require.NoError(t, err)
	publicDump, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Contains(t, string(publicDump), "public.backup_marker")
	require.NotContains(t, string(publicDump), "external.extra_marker")
	_, err = db.ExecContext(ctx, "UPDATE public.backup_marker SET id=99; UPDATE external.extra_marker SET id=77")
	require.NoError(t, err)
	dumper.cfg.User = "application_owner"
	require.NoError(t, dumper.Restore(ctx, bytes.NewReader(publicDump)))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM public.backup_marker").Scan(&id))
	require.Equal(t, 1, id)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT id FROM external.extra_marker").Scan(&id))
	require.Equal(t, 77, id, "public restore must not modify unrelated schemas")
	var extensionOwner string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT extowner::regrole::text FROM pg_extension WHERE extname='pg_trgm'").Scan(&extensionOwner))
	require.Equal(t, "postgres", extensionOwner, "application restore must not replace an administrator-owned extension")

	// A replacement target is initialized with the same schema and extension
	// prerequisites as XY2API startup, but no application tables or data.
	_, err = db.ExecContext(ctx, "CREATE DATABASE initialized_restore OWNER application_owner")
	require.NoError(t, err)
	targetURL, err := url.Parse(dsn)
	require.NoError(t, err)
	targetURL.Path = "/initialized_restore"
	targetDB, err := sql.Open("postgres", targetURL.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetDB.Close() })
	_, err = targetDB.ExecContext(ctx, "CREATE EXTENSION pg_trgm")
	require.NoError(t, err)
	dumper.cfg.DBName = "initialized_restore"
	require.NoError(t, dumper.Restore(ctx, bytes.NewReader(publicDump)))
	require.NoError(t, targetDB.QueryRowContext(ctx, "SELECT id FROM public.backup_marker").Scan(&id))
	require.Equal(t, 1, id)
	var indexExists bool
	require.NoError(t, targetDB.QueryRowContext(ctx, "SELECT to_regclass('public.extension_marker_trgm') IS NOT NULL").Scan(&indexExists))
	require.True(t, indexExists)
	var text string
	require.NoError(t, targetDB.QueryRowContext(ctx, "SELECT value FROM public.extension_marker").Scan(&text))
	require.Equal(t, "DROP SCHEMA public;", text, "data resembling SQL must remain unchanged")
	require.NoError(t, targetDB.QueryRowContext(ctx, "SELECT public.backup_function()").Scan(&text))
	require.Equal(t, "CREATE SCHEMA public;", text)
	require.NoError(t, targetDB.QueryRowContext(ctx, "SELECT 'first'::public.backup_kind::text").Scan(&text))
	require.Equal(t, "first", text)
	require.NoError(t, targetDB.QueryRowContext(ctx, "SELECT nextval('public.backup_sequence')").Scan(&id))
	require.Equal(t, 42, id)

	_, err = db.ExecContext(ctx, "DROP SCHEMA public CASCADE")
	require.NoError(t, err)
	dumper.cfg.DBName = "backup_test"
	reader, err = dumper.DumpSchema(ctx, "public")
	require.Nil(t, reader)
	require.ErrorContains(t, err, "no matching schemas were found")
}

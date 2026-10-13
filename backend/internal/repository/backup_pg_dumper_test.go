package repository

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newTestPgDumper(t *testing.T, commandContext func(context.Context, string, ...string) *exec.Cmd) (*PgDumper, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &PgDumper{
		cfg: &config.DatabaseConfig{
			Host:     "db.example.test",
			Port:     5432,
			User:     "sub2api",
			Password: "secret",
			DBName:   "sub2api",
			SSLMode:  "require",
		},
		db:             db,
		commandContext: commandContext,
	}, mock
}

func expectBackupMigrationLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
}

func expectBackupMigrationUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestPgDumperHoldsMigrationLockThroughReaderClose(t *testing.T) {
	var mock sqlmock.Sqlmock
	commandCreated := false
	dumper, createdMock := newTestPgDumper(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		commandCreated = true
		require.Equal(t, "pg_dump", name)
		require.Contains(t, args, "--clean")
		require.NoError(t, mock.ExpectationsWereMet(), "migration lock must be acquired before pg_dump is created")
		return exec.CommandContext(ctx, "sh", "-c", "printf backup-data")
	})
	mock = createdMock
	expectBackupMigrationLock(mock)

	reader, err := dumper.Dump(context.Background())
	require.NoError(t, err)
	require.True(t, commandCreated)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "backup-data", string(data))

	expectBackupMigrationUnlock(mock)
	require.Error(t, mock.ExpectationsWereMet(), "migration lock was released before the reader closed")
	require.NoError(t, reader.Close())
	require.NoError(t, mock.ExpectationsWereMet())
	require.NoError(t, reader.Close(), "reader close must be idempotent")
}

func TestPgDumperReleasesMigrationLockWhenStdoutPipeSetupFails(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", "true")
		cmd.Stdout = io.Discard
		return cmd
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)

	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "create stdout pipe")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperReleasesMigrationLockWhenProcessStartFails(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/path/that/does/not/exist/pg_dump")
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)

	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "start pg_dump")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperReleasesMigrationLockWhenProcessFails(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf partial-backup; exit 7")
	})
	expectBackupMigrationLock(mock)

	reader, err := dumper.Dump(context.Background())
	require.NoError(t, err)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "partial-backup", string(data))
	expectBackupMigrationUnlock(mock)
	require.ErrorContains(t, reader.Close(), "pg_dump exited with error")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperReportsUnlockFailureAndDiscardsConnection(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf backup-data")
	})
	expectBackupMigrationLock(mock)

	reader, err := dumper.Dump(context.Background())
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnError(errors.New("unlock unavailable"))
	require.ErrorContains(t, reader.Close(), "release backup migration lock")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperDoesNotStartProcessWhenMigrationLockFails(t *testing.T) {
	commandCreated := false
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		commandCreated = true
		return exec.CommandContext(ctx, "sh", "-c", "true")
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnError(errors.New("database unavailable"))

	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "acquire backup migration lock")
	require.False(t, commandCreated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperRejectsNilDatabase(t *testing.T) {
	dumper := &PgDumper{cfg: &config.DatabaseConfig{}}
	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "nil sql db")
}

func TestPgDumperPublicScopeIsExplicitAndStrict(t *testing.T) {
	var temporaryPaths []string
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "pg_dump" {
			require.Contains(t, args, "--schema=public")
			require.Contains(t, args, "--strict-names")
			require.Contains(t, args, "--format=custom")
			require.NotContains(t, args, "--blobs")
		} else {
			require.Equal(t, "pg_restore", name)
			if args[0] == "--list" {
				temporaryPaths = append(temporaryPaths, args[1])
				return exec.CommandContext(ctx, "sh", "-c", "printf '5; 2615 2200 SCHEMA - public postgres\n6; 1259 20000 TABLE public app postgres\n'")
			}
			listPath := strings.TrimPrefix(args[0], "--use-list=")
			temporaryPaths = append(temporaryPaths, listPath)
			list, err := os.ReadFile(listPath)
			require.NoError(t, err)
			require.Contains(t, string(list), "TABLE public app")
			require.NotContains(t, string(list), "SCHEMA - public")
			require.Contains(t, args, "--no-owner")
		}
		return exec.CommandContext(ctx, "sh", "-c", "printf schema-data")
	})
	reader, err := dumper.DumpSchema(context.Background(), "*")
	require.ErrorContains(t, err, "unsupported backup schema")
	require.Nil(t, reader)
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)
	reader, err = dumper.DumpSchema(context.Background(), "public")
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.NoError(t, mock.ExpectationsWereMet())
	for _, path := range temporaryPaths {
		_, err := os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestPgDumperPublicScopeCleansTemporaryArchiveOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf 'pg_dump: error: permission denied' >&2; exit 1")
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)
	reader, err := dumper.DumpSchema(context.Background(), "public")
	require.Nil(t, reader)
	require.ErrorContains(t, err, "permission denied")
	files, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	require.Empty(t, files)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFilterPublicBackupTOCKeepsApplicationObjects(t *testing.T) {
	input := "; PostgreSQL archive\n1; 2615 2200 SCHEMA - public pg_database_owner\n" +
		"2; 0 0 COMMENT - SCHEMA public postgres\n3; 0 0 ACL - SCHEMA public postgres\n" +
		"4; 1259 100 TABLE public app postgres\n5; 0 100 TABLE DATA public app postgres\n" +
		"6; 1255 200 FUNCTION public app_fn() postgres\n7; 1247 201 TYPE public app_type postgres\n" +
		"8; 1259 202 SEQUENCE public app_seq postgres\n9; 0 100 COMMENT public TABLE app postgres\n"
	var out strings.Builder
	require.NoError(t, filterPublicBackupTOC(strings.NewReader(input), &out))
	require.NotContains(t, out.String(), "SCHEMA")
	for _, object := range []string{"TABLE public app", "TABLE DATA public app", "FUNCTION public app_fn()", "TYPE public app_type", "SEQUENCE public app_seq", "COMMENT public TABLE app"} {
		require.Contains(t, out.String(), object)
	}
	for _, invalid := range []string{"", "unknown TOC format", "1; 1259 100 TABLE public app postgres\n"} {
		require.ErrorContains(t, filterPublicBackupTOC(strings.NewReader(invalid), io.Discard), "expected one public schema entry")
	}
}

func TestPgDumperIncludesSafeFailureDiagnostic(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		require.Equal(t, "pg_dump", name)
		require.Contains(t, args, "--no-password")
		// The password is passed only via the environment, never argv.
		require.NotContains(t, strings.Join(args, " "), "secret")
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' 'pg_dump: error: query failed: ERROR: permission denied for schema legacy' >&2; printf 'pg_dump: detail: Query was: SELECT private_data\npassword=%s\n' "$PGPASSWORD" >&2; exit 1`)
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)
	reader, err := dumper.Dump(context.Background())
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	err = reader.Close()
	require.ErrorContains(t, err, "permission denied for schema legacy")
	require.NotContains(t, err.Error(), "secret")
	require.NotContains(t, err.Error(), "private_data")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperCloseStopsUnfinishedProcess(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf started; exec sleep 60")
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reader, err := dumper.Dump(ctx)
	require.NoError(t, err)
	_, err = io.ReadFull(reader, make([]byte, len("started")))
	require.NoError(t, err)
	start := time.Now()
	err = reader.Close()
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 3*time.Second)
	require.Equal(t, err, reader.Close(), "close must return the same error without unlocking twice")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperContextCancellationReleasesMigrationLock(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf started; exec sleep 60")
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, err := dumper.Dump(ctx)
	require.NoError(t, err)
	_, err = io.ReadFull(reader, make([]byte, len("started")))
	require.NoError(t, err)
	cancel()
	_, _ = io.ReadAll(reader)
	require.ErrorIs(t, reader.Close(), context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperRestoreUsesFailFastTransaction(t *testing.T) {
	dumper, _ := newTestPgDumper(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		require.Equal(t, "psql", name)
		for _, expected := range []string{"--no-password", "--no-psqlrc", "--single-transaction", "--set=ON_ERROR_STOP=on", "--set=VERBOSITY=terse", "--file=-"} {
			require.Contains(t, args, expected)
		}
		return exec.CommandContext(ctx, "sh", "-c", `cat >/dev/null; printf '%s\n' 'psql:<stdin>:2: ERROR: relation missing does not exist' 'LINE 1: INSERT INTO private_data' >&2; exit 3`)
	})
	err := dumper.Restore(context.Background(), strings.NewReader("SELECT 1;"))
	require.ErrorContains(t, err, "psql exited with error")
	require.ErrorContains(t, err, "relation missing does not exist")
	require.NotContains(t, err.Error(), "private_data")
}

func TestPgDumperRestoreSuccessDiscardsOutput(t *testing.T) {
	dumper, _ := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `cat >/dev/null; printf 'CREATE TABLE\n'`)
	})
	require.NoError(t, dumper.Restore(context.Background(), strings.NewReader("SELECT 1;")))
}

func TestPostgresDiagnosticBoundsAndRedacts(t *testing.T) {
	t.Run("chunked password and URI", func(t *testing.T) {
		diagnostic := &postgresDiagnostic{password: "special p@ss"}
		for _, chunk := range []string{"pg_dump: error: special p", "@ss; postgresql://user:other@db/x; special%20p@ss; password=other\n"} {
			n, err := diagnostic.Write([]byte(chunk))
			require.NoError(t, err)
			require.Equal(t, len(chunk), n)
		}
		message := diagnostic.String()
		require.Contains(t, message, "pg_dump: error:")
		for _, secret := range []string{"special p@ss", "other", "special%20p@ss"} {
			require.NotContains(t, message, secret)
		}
	})
	t.Run("oversized output keeps reason and drops incomplete line", func(t *testing.T) {
		diagnostic := &postgresDiagnostic{password: "secret"}
		text := "pg_dump: error: permission denied\n" + strings.Repeat("private", postgresDiagnosticLimit)
		n, err := diagnostic.Write([]byte(text))
		require.NoError(t, err)
		require.Equal(t, len(text), n)
		require.Equal(t, postgresDiagnosticLimit, diagnostic.buf.Len())
		require.Contains(t, diagnostic.String(), "permission denied")
		require.Contains(t, diagnostic.String(), "stderr truncated")
		require.NotContains(t, diagnostic.String(), "private")
	})
}

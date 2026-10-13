package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// PgDumper implements service.DBDumper using pg_dump/psql
type PgDumper struct {
	cfg            *config.DatabaseConfig
	db             *sql.DB
	commandContext func(context.Context, string, ...string) *exec.Cmd
}

// NewPgDumper creates a new PgDumper
func NewPgDumper(cfg *config.Config, db *sql.DB) service.DBDumper {
	return &PgDumper{
		cfg:            &cfg.Database,
		db:             db,
		commandContext: exec.CommandContext,
	}
}

// Dump executes pg_dump and returns a streaming reader of the output
func (d *PgDumper) Dump(ctx context.Context) (io.ReadCloser, error) {
	return d.dump(ctx, "")
}

// DumpSchema is used only after an administrator explicitly selects the public
// application scope. Never retry an unsuccessful full dump with this scope.
func (d *PgDumper) DumpSchema(ctx context.Context, schema string) (io.ReadCloser, error) {
	if schema != "public" {
		return nil, errors.New("unsupported backup schema: only public is supported")
	}
	return d.dump(ctx, schema)
}

func (d *PgDumper) dump(ctx context.Context, schema string) (io.ReadCloser, error) {
	if d.db == nil {
		return nil, errors.New("acquire backup migration lock: nil sql db")
	}
	lockConn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire backup migration lock connection: %w", err)
	}
	if err := pgAdvisoryLock(ctx, lockConn); err != nil {
		// Lock acquisition can fail after PostgreSQL accepted the request. Discard
		// the session because ownership is ambiguous and advisory locks are session-scoped.
		discardSQLConnection(lockConn)
		return nil, fmt.Errorf("acquire backup migration lock: %w", err)
	}
	releaseLock := func() error {
		return releaseBackupMigrationLock(lockConn)
	}
	if schema != "" {
		return d.dumpPublicSchema(ctx, releaseLock)
	}

	args := []string{
		"--no-password",
		"-h", d.cfg.Host,
		"-p", fmt.Sprintf("%d", d.cfg.Port),
		"-U", d.cfg.User,
		"-d", d.cfg.DBName,
		"--no-owner",
		"--no-acl",
		"--clean",
		"--if-exists",
	}

	// The reader owns this child context, so closing an unfinished dump also
	// stops pg_dump (e.g. when creating/writing the temporary archive fails).
	processCtx, cancel := context.WithCancel(ctx)
	cmd, stderr := d.newCommand(processCtx, "pg_dump", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, errors.Join(fmt.Errorf("create stdout pipe: %w", err), releaseLock())
	}

	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdout.Close()
		return nil, errors.Join(fmt.Errorf("start pg_dump: %w", err), releaseLock())
	}

	return &cmdReadCloser{
		ReadCloser: stdout, cmd: cmd, stderr: stderr,
		ctx: processCtx, cancel: cancel, release: releaseLock,
	}, nil
}

func releaseBackupMigrationLock(conn *sql.Conn) error {
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pgAdvisoryUnlock(unlockCtx, conn); err != nil {
		// A failed unlock must not return a possibly lock-owning session to the pool.
		discardSQLConnection(conn)
		return fmt.Errorf("release backup migration lock: %w", err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("close backup migration lock connection: %w", err)
	}
	return nil
}

func discardSQLConnection(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// Restore executes psql to restore from a streaming reader
func (d *PgDumper) Restore(ctx context.Context, data io.Reader) error {
	args := []string{
		"--no-password",
		"--no-psqlrc",
		"-h", d.cfg.Host,
		"-p", fmt.Sprintf("%d", d.cfg.Port),
		"-U", d.cfg.User,
		"-d", d.cfg.DBName,
		"--single-transaction",
		"--set=ON_ERROR_STOP=on",
		"--set=VERBOSITY=terse",
		"--file=-",
	}

	cmd, stderr := d.newCommand(ctx, "psql", args...)
	cmd.Stdin = data
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return postgresCommandError(ctx, "psql", err, stderr)
	}
	return nil
}

// cmdReadCloser wraps a command stdout pipe and waits for the process on Close
type cmdReadCloser struct {
	io.ReadCloser
	cmd       *exec.Cmd
	command   string
	stderr    *postgresDiagnostic
	ctx       context.Context
	cancel    context.CancelFunc
	release   func() error
	eof       atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func (c *cmdReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		c.eof.Store(true)
	}
	return n, err
}

func (c *cmdReadCloser) Close() error {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			defer c.cancel()
			if !c.eof.Load() {
				c.cancel()
			}
		}
		var closeErrs []error
		if err := c.ReadCloser.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close pg_dump stdout: %w", err))
		}
		if err := c.cmd.Wait(); err != nil {
			name := c.command
			if name == "" {
				name = "pg_dump"
			}
			closeErrs = append(closeErrs, postgresCommandError(c.ctx, name, err, c.stderr))
		}
		if c.release != nil {
			if err := c.release(); err != nil {
				closeErrs = append(closeErrs, err)
			}
		}
		c.closeErr = errors.Join(closeErrs...)
	})
	return c.closeErr
}

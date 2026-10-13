package repository

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// dumpPublicSchema uses PostgreSQL's archive table of contents to omit only the
// public namespace definition. A plain --schema=public --clean --no-owner dump
// would DROP SCHEMA public and fail on extensions (e.g. pg_trgm) owned by the
// database administrator. Never filter generated SQL: COPY data and function
// bodies can contain text indistinguishable from DDL.
//
// The result remains plain SQL for the existing gzip/restore pipeline. Restore
// targets must first initialize XY2API's public schema and required extensions.
func (d *PgDumper) dumpPublicSchema(ctx context.Context, releaseLock func() error) (reader io.ReadCloser, err error) {
	processCtx, cancel := context.WithCancel(ctx)
	var paths []string
	cleanup := func() error {
		var errs []error
		for _, path := range paths {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove temporary backup archive: %w", removeErr))
			}
		}
		return errors.Join(errs...)
	}
	defer func() {
		if reader == nil {
			cancel()
			err = errors.Join(err, cleanup(), releaseLock())
		}
	}()
	createTemp := func(suffix string) (*os.File, error) {
		file, createErr := os.CreateTemp("", "xy2api-public-backup-*"+suffix)
		if createErr != nil {
			return nil, fmt.Errorf("create temporary backup archive: %w", createErr)
		}
		paths = append(paths, file.Name())
		return file, nil
	}
	archive, err := createTemp(".dump")
	if err != nil {
		return nil, err
	}
	cmd, stderr := d.newCommand(processCtx, "pg_dump",
		"--no-password", "-h", d.cfg.Host, "-p", fmt.Sprint(d.cfg.Port),
		"-U", d.cfg.User, "-d", d.cfg.DBName,
		"--format=custom", "--schema=public", "--strict-names", "--no-acl")
	cmd.Stdout = archive
	runErr := cmd.Run()
	closeErr := archive.Close()
	if runErr != nil {
		return nil, errors.Join(postgresCommandError(processCtx, "pg_dump", runErr, stderr), closeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close temporary backup archive: %w", closeErr)
	}

	toc, err := createTemp(".toc")
	if err != nil {
		return nil, err
	}
	cmd, stderr = d.newCommand(processCtx, "pg_restore", "--list", archive.Name())
	cmd.Stdout = toc
	if runErr := cmd.Run(); runErr != nil {
		return nil, errors.Join(postgresCommandError(processCtx, "pg_restore", runErr, stderr), toc.Close())
	}
	if _, err := toc.Seek(0, io.SeekStart); err != nil {
		return nil, errors.Join(fmt.Errorf("read backup table of contents: %w", err), toc.Close())
	}
	selected, err := createTemp(".list")
	if err != nil {
		return nil, errors.Join(err, toc.Close())
	}
	err = filterPublicBackupTOC(toc, selected)
	err = errors.Join(err, toc.Close(), selected.Close())
	if err != nil {
		return nil, fmt.Errorf("select public backup objects: %w", err)
	}

	cmd, stderr = d.newCommand(processCtx, "pg_restore", "--use-list="+selected.Name(),
		"--clean", "--if-exists", "--no-owner", "--no-acl", "--file=-", archive.Name())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create pg_restore stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start pg_restore: %w", err), stdout.Close())
	}
	return &cmdReadCloser{
		ReadCloser: stdout, cmd: cmd, command: "pg_restore", stderr: stderr,
		ctx: processCtx, cancel: cancel,
		release: func() error { return errors.Join(cleanup(), releaseLock()) },
	}, nil
}

func filterPublicBackupTOC(src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	schemaEntries := 0
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		// PostgreSQL's TOC entry format is: dumpID; catalogOID objectOID
		// description namespace name owner. Only public namespace entries are
		// excluded; all its tables, sequences, functions and types remain.
		if len(fields) >= 6 && fields[3] == "SCHEMA" && fields[4] == "-" && fields[5] == "public" {
			schemaEntries++
			continue
		}
		if len(fields) >= 7 && (fields[3] == "COMMENT" || fields[3] == "ACL") &&
			fields[4] == "-" && fields[5] == "SCHEMA" && fields[6] == "public" {
			continue
		}
		if _, err := fmt.Fprintln(dst, line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// An explicit --schema=public dump includes this entry. Refuse an unknown
	// TOC format instead of emitting a backup that could still drop the schema.
	if schemaEntries != 1 {
		return fmt.Errorf("expected one public schema entry in backup table of contents, found %d", schemaEntries)
	}
	return nil
}

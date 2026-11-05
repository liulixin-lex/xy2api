package mcp

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/wire"
	"github.com/liulixin-lex/xy2api/internal/config"
)

var ProviderSet = wire.NewSet(ProvideReaderDB, NewQueryReader, NewRuntimeReader, NewServer)

func canonicalPublicURL(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("mcp.public_url must be an HTTPS origin")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// ProvideReaderDB never falls back to the business writer, even when MCP is
// disabled or its dedicated DSN is missing. It uses only its own connection.
func ProvideReaderDB(cfg *config.Config, writer *sql.DB) (*ReaderDB, error) {
	if cfg == nil || !cfg.MCP.Enabled {
		return &ReaderDB{}, nil
	}
	if cfg.MCP.ReaderDSN == "" {
		return nil, errors.New("MCP requires a dedicated read-only DSN")
	}
	db, e := sql.Open("postgres", cfg.MCP.ReaderDSN)
	if e != nil {
		return nil, errors.New("MCP reader configuration is invalid")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(15 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = verifyReader(ctx, db, writer); e != nil {
		_ = db.Close()
		return nil, e
	}
	return &ReaderDB{DB: db}, nil
}
func verifyReader(ctx context.Context, reader, writer *sql.DB) error {
	if reader == nil || writer == nil {
		return errors.New("MCP database connections unavailable")
	}
	var ruser, sessionUser, wuser string
	var privileged bool
	if e := reader.QueryRowContext(ctx, `SELECT current_user,session_user,rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&ruser, &sessionUser, &privileged); e != nil {
		return errors.New("MCP reader login verification failed")
	}
	if e := writer.QueryRowContext(ctx, `SELECT current_user`).Scan(&wuser); e != nil {
		return errors.New("MCP writer login verification failed")
	}
	if ruser == wuser || ruser != sessionUser || privileged {
		return errors.New("MCP reader must use a separate unprivileged login")
	}
	var writable bool
	// A dedicated reader has direct SELECT grants and no role memberships.
	// Checking all non-system schemas and partitions also catches column-only
	// writes, ownership that can re-grant rights, and NOINHERIT SET ROLE paths.
	// Trigger-only functions cannot be called by SELECT; without table DML or
	// schema CREATE grants they introduce no callable write path for this role.
	if e := reader.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname=current_user)
 OR EXISTS(SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND (d.datdba=(SELECT oid FROM pg_roles WHERE rolname=current_user) OR has_database_privilege(current_user,d.oid,'CREATE')))
 OR EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND (n.nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) OR has_schema_privilege(current_user,n.oid,'CREATE')))
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND (c.relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) OR (c.relkind IN ('r','p','v','m','f') AND (has_table_privilege(current_user,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege(current_user,c.oid,'INSERT,UPDATE,REFERENCES'))) OR (c.relkind='S' AND has_sequence_privilege(current_user,c.oid,'USAGE,UPDATE'))))
 OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND p.prorettype NOT IN ('pg_catalog.trigger'::regtype,'pg_catalog.event_trigger'::regtype) AND has_function_privilege(current_user,p.oid,'EXECUTE') AND (p.prosecdef OR p.provolatile<>'i'))`).Scan(&writable); e != nil || writable {
		return errors.New("MCP reader login has write privileges or cannot be verified")
	}
	return nil
}

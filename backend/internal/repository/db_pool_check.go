package repository

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// Database connection budget check (xy2api; production readiness review
// P1-D). A pre-launch instance ran a pool of 256 connections against a
// PostgreSQL limit of 100. The check only warns: the pool is a ceiling, not
// what the process opens, and some deployments size the server separately.

// dbConnectionBudget is what one process's pool may use: the server limit
// minus the reserved slots and the connections of other database users.
type dbConnectionBudget struct {
	MaxConnections   int
	SuperuserReserve int
	Reserved         int // reserved_connections (PostgreSQL 16+), 0 before
	OtherUsers       int
}

// available is the connections left for this application's pools.
func (b dbConnectionBudget) available() int {
	return b.MaxConnections - b.SuperuserReserve - b.Reserved - b.OtherUsers
}

// poolExceeds reports whether two pools of maxOpen (a blue-green overlap
// runs both) can exceed the available connections.
func (b dbConnectionBudget) poolExceeds(maxOpen int) bool {
	return maxOpen > 0 && 2*maxOpen > b.available()
}

const dbPoolCheckTimeout = 3 * time.Second

// warnDBPoolAboveServerLimit logs a warning when the configured pool can
// take more connections than PostgreSQL has for this application. Any query
// failure skips the check silently.
func warnDBPoolAboveServerLimit(ctx context.Context, db *sql.DB, maxOpen int) {
	if db == nil || maxOpen <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbPoolCheckTimeout)
	defer cancel()
	var b dbConnectionBudget
	if err := db.QueryRowContext(ctx, `
		SELECT current_setting('max_connections')::int,
			current_setting('superuser_reserved_connections')::int,
			COALESCE(NULLIF(current_setting('reserved_connections', true), ''), '0')::int,
			(SELECT count(*) FROM pg_stat_activity
				WHERE backend_type = 'client backend' AND usename IS DISTINCT FROM current_user)::int
	`).Scan(&b.MaxConnections, &b.SuperuserReserve, &b.Reserved, &b.OtherUsers); err != nil {
		return
	}
	if !b.poolExceeds(maxOpen) {
		return
	}
	slog.Warn("database connection pool exceeds the server limit",
		"max_open_conns", maxOpen,
		"pools_during_blue_green", 2,
		"max_connections", b.MaxConnections,
		"superuser_reserved_connections", b.SuperuserReserve,
		"reserved_connections", b.Reserved,
		"other_users_connections", b.OtherUsers,
		"available", b.available(),
		"hint", "set database.max_open_conns to at most (available - maintenance headroom) / 2, or raise max_connections",
	)
}

package mcp

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

type controlCleanup struct {
	db     *sql.DB
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func newControlCleanup(db *sql.DB) *controlCleanup {
	ctx, cancel := context.WithCancel(context.Background())
	c := &controlCleanup{db: db, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				ctxRun, stop := context.WithTimeout(ctx, 2*time.Second)
				if cleanupControlBatch(ctxRun, db) != nil {
					slog.Warn("mcp.control_cleanup_failed")
				}
				stop()
			}
		}
	}()
	return c
}

func (c *controlCleanup) close() {
	if c == nil {
		return
	}
	c.once.Do(func() { c.cancel(); <-c.done })
}

// cleanupControlBatch uses bounded deletes and preserves credentials until
// their natural expiry, so refresh reuse remains detectable for its lifetime.
func cleanupControlBatch(ctx context.Context, db *sql.DB) error {
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM mcp_oauth_pending WHERE id IN (SELECT id FROM mcp_oauth_pending WHERE consumed_at IS NOT NULL OR expires_at<NOW() ORDER BY expires_at LIMIT 500)`,
		`DELETE FROM mcp_oauth_sessions WHERE (kind,signature) IN (SELECT kind,signature FROM mcp_oauth_sessions WHERE expires_at<NOW() ORDER BY expires_at LIMIT 1000)`,
		`DELETE FROM mcp_oauth_grants g WHERE g.id IN (SELECT g.id FROM mcp_oauth_grants g WHERE g.expires_at<NOW() AND NOT EXISTS (SELECT 1 FROM mcp_oauth_sessions s WHERE s.request_id=g.id) ORDER BY g.expires_at LIMIT 500)`,
		`DELETE FROM mcp_oauth_clients c WHERE c.id IN (SELECT c.id FROM mcp_oauth_clients c WHERE c.expires_at<NOW() AND NOT EXISTS(SELECT 1 FROM mcp_oauth_grants g WHERE g.client_id=c.id) AND NOT EXISTS(SELECT 1 FROM mcp_oauth_pending p WHERE p.client_id=c.id) ORDER BY c.expires_at LIMIT 500)`,
		`DELETE FROM mcp_access_audit WHERE id IN (SELECT id FROM mcp_access_audit WHERE occurred_at<NOW()-INTERVAL '90 days' ORDER BY occurred_at LIMIT 1000)`,
	} {
		if _, e = tx.ExecContext(ctx, stmt); e != nil {
			return e
		}
	}
	return tx.Commit()
}

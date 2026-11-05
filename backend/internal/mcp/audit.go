package mcp

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type auditEvent struct {
	QueryID                                      string
	UserID                                       int64
	ClientID, GrantID, Tool, Decision, ErrorCode string
	DurationMS                                   int64
	Rows                                         int
}
type auditSink struct {
	db      *sql.DB
	queue   chan auditEvent
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
	dropped atomic.Uint64
	failed  atomic.Uint64
}

func newAuditSink(db *sql.DB) *auditSink {
	ctx, cancel := context.WithCancel(context.Background())
	a := &auditSink{db: db, queue: make(chan auditEvent, 256), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go a.run()
	return a
}
func (a *auditSink) enqueue(e auditEvent) {
	select {
	case <-a.ctx.Done():
		a.dropped.Add(1)
		return
	default:
	}
	select {
	case <-a.ctx.Done():
		a.dropped.Add(1)
	case a.queue <- e:
	default:
		a.dropped.Add(1)
	}
}
func (a *auditSink) run() {
	defer close(a.done)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	var lastDropped, lastFailed uint64
	report := func() {
		d, f := a.dropped.Load(), a.failed.Load()
		if d != lastDropped || f != lastFailed {
			slog.Warn("mcp.audit_health", "dropped_total", d, "failed_total", f, "queued", len(a.queue))
			lastDropped, lastFailed = d, f
		}
	}
	for {
		select {
		case <-a.ctx.Done():
			a.dropped.Add(uint64(len(a.queue)))
			report()
			return
		case <-tick.C:
			report()
		case e := <-a.queue:
			ctx, cancel := context.WithTimeout(a.ctx, time.Second)
			_, err := a.db.ExecContext(ctx, `INSERT INTO mcp_access_audit(query_id,user_id,client_id,grant_id,tool,decision,error_code,duration_ms,rows_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, e.QueryID, e.UserID, e.ClientID, e.GrantID, e.Tool, e.Decision, e.ErrorCode, e.DurationMS, e.Rows)
			cancel()
			if err != nil {
				a.failed.Add(1)
			}
		}
	}
}
func (a *auditSink) close() {
	a.once.Do(func() { a.cancel(); <-a.done })
}

package usageexport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/disk"
)

const admissionLock int64 = 827193010
const workerLockBase int64 = 827194000
const taskJSON = `data || (to_jsonb(usage_export_tasks) - 'data' - 'fingerprint' - 'cleaned' - 'reserved_bytes' - 'heartbeat_at')`

type Engine struct {
	DB       *sql.DB
	Config   Config
	Snapshot Snapshot
	Store    Store
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func New(db *sql.DB, cfg Config, snapshot Snapshot, store Store) (*Engine, error) {
	if cfg.Concurrency < 1 || cfg.Concurrency > 32 || cfg.QueueLimit < 1 || cfg.MaxRows < 1 || cfg.MaxRows > 1000000 || cfg.MaxFileBytes < 1 || cfg.TaskDiskBytes < cfg.MaxFileBytes || cfg.StorageBytes < cfg.TaskDiskBytes || cfg.FreeBytes < 0 || cfg.ReadSeconds < 1 || cfg.RunSeconds < cfg.ReadSeconds || cfg.QueueSeconds < 1 || cfg.RetentionSeconds < 1 || cfg.MetadataSeconds < cfg.RetentionSeconds || cfg.CreateRPM < 1 || cfg.StatusRPM < 1 || cfg.TicketRPM < 1 || cfg.DownloadConcurrency < 1 || cfg.DownloadConcurrency > 32 {
		return nil, fmt.Errorf("invalid usage_export resource limits")
	}
	if err := os.MkdirAll(cfg.Directory, 0700); err != nil {
		return nil, err
	}
	if store == nil {
		store = LocalStore{Root: filepath.Join(cfg.Directory, "files")}
	}
	return &Engine{DB: db, Config: cfg, Snapshot: snapshot, Store: store}, nil
}
func (e *Engine) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	for slot := 0; slot < e.Config.Concurrency; slot++ {
		e.wg.Add(1)
		go func(slot int) {
			defer e.wg.Done()
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					e.runSlot(ctx, slot)
				}
			}
		}(slot)
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := e.Cleanup(ctx); err != nil {
					slog.Warn("usage export cleanup failed")
				}
			}
		}
	}()
}
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
		e.wg.Wait()
	}
}
func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var b []byte
	if err := row.Scan(&b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fail(404, "EXPORT_NOT_FOUND")
		}
		return nil, err
	}
	var t Task
	err := json.Unmarshal(b, &t)
	return &t, err
}
func (e *Engine) Get(ctx context.Context, owner int64, scope, id string) (*Task, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, fail(404, "EXPORT_NOT_FOUND")
	}
	return scanTask(e.DB.QueryRowContext(ctx, `SELECT `+taskJSON+` FROM usage_export_tasks WHERE id=$1 AND owner_id=$2 AND scope=$3`, id, owner, scope))
}
func (e *Engine) List(ctx context.Context, owner int64, scope string, page, size int) ([]Task, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	var total int64
	if err := e.DB.QueryRowContext(ctx, `SELECT count(*) FROM usage_export_tasks WHERE owner_id=$1 AND scope=$2 AND created_at>now()-interval '24 hours' AND status<>'deleted'`, owner, scope).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := e.DB.QueryContext(ctx, `SELECT `+taskJSON+` FROM usage_export_tasks WHERE owner_id=$1 AND scope=$2 AND created_at>now()-interval '24 hours' AND status<>'deleted' ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, owner, scope, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	return out, total, rows.Err()
}
func rate(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, owner int64, scope string, limit int) error {
	var n int
	err := q.QueryRowContext(ctx, `INSERT INTO usage_export_rate(owner_id,scope,window_at,count) VALUES($1,$2,date_trunc('minute',now()),1) ON CONFLICT(owner_id,scope) DO UPDATE SET window_at=excluded.window_at,count=CASE WHEN usage_export_rate.window_at=excluded.window_at THEN usage_export_rate.count+1 ELSE 1 END RETURNING count`, owner, scope).Scan(&n)
	if err != nil {
		return err
	}
	if n > limit {
		return &Fault{Status: 429, Code: "EXPORT_RATE_LIMITED", Retry: 60}
	}
	return nil
}
func (e *Engine) Rate(ctx context.Context, owner int64, scope string, limit int) error {
	return rate(ctx, e.DB, owner, scope, limit)
}
func (e *Engine) Create(ctx context.Context, owner int64, scope, key string, o Options) (*Task, error) {
	if owner <= 0 || (scope != "user" && scope != "admin") || (scope == "user" && (o.Format != "csv" || o.Filters.UserID != owner)) || (scope == "admin" && o.Format != "xlsx") || o.Version != 1 {
		return nil, fail(400, "EXPORT_INVALID_OPTIONS")
	}
	if len(key) < 8 || len(key) > 128 {
		return nil, fail(400, "EXPORT_IDEMPOTENCY_KEY_REQUIRED")
	}
	b, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	fingerprint := hex.EncodeToString(sum[:])
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, admissionLock); err != nil {
		return nil, err
	}
	var id, prior string
	err = tx.QueryRowContext(ctx, `SELECT task_id,fingerprint FROM usage_export_keys WHERE owner_id=$1 AND scope=$2 AND key=$3`, owner, scope, key).Scan(&id, &prior)
	if err == nil {
		if prior != fingerprint {
			return nil, fail(409, "EXPORT_IDEMPOTENCY_CONFLICT")
		}
		return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskJSON+` FROM usage_export_tasks WHERE id=$1`, id))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	active, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskJSON+` FROM usage_export_tasks WHERE owner_id=$1 AND status IN ('queued','running')`, owner))
	if err == nil {
		var fp string
		if err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM usage_export_tasks WHERE id=$1`, active.ID).Scan(&fp); err != nil {
			return nil, err
		}
		if fp != fingerprint || active.Scope != scope {
			return nil, fail(409, "EXPORT_ALREADY_ACTIVE")
		}
		id = active.ID
	} else {
		var f *Fault
		if !errors.As(err, &f) || f.Status != 404 {
			return nil, err
		}
		if !e.Config.Enabled || (len(e.Config.AllowedUserIDs) > 0 && !slices.Contains(e.Config.AllowedUserIDs, owner)) {
			return nil, fail(503, "EXPORT_DISABLED")
		}
		var queued int
		var used, reserved int64
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE status='queued'),COALESCE(sum(reserved_bytes+size_bytes) FILTER (WHERE NOT cleaned),0),COALESCE(sum(reserved_bytes) FILTER (WHERE NOT cleaned),0) FROM usage_export_tasks`).Scan(&queued, &used, &reserved); err != nil {
			return nil, err
		}
		if queued >= e.Config.QueueLimit {
			return nil, fail(503, "EXPORT_QUEUE_FULL")
		}
		if used+e.Config.TaskDiskBytes > e.Config.StorageBytes {
			return nil, fail(503, "EXPORT_STORAGE_FULL")
		}
		free, err := disk.UsageWithContext(ctx, e.Config.Directory)
		if err != nil {
			return nil, err
		}
		if free.Free < uint64(reserved+e.Config.TaskDiskBytes+e.Config.FreeBytes) {
			return nil, fail(503, "EXPORT_STORAGE_FULL")
		}
		if err = rate(ctx, tx, owner, "create", e.Config.CreateRPM); err != nil {
			return nil, err
		}
		id = uuid.NewString()
		data, _ := json.Marshal(map[string]any{"options": o})
		if _, err = tx.ExecContext(ctx, `INSERT INTO usage_export_tasks(id,owner_id,scope,fingerprint,status,reserved_bytes,data) VALUES($1,$2,$3,$4,'queued',$5,$6)`, id, owner, scope, fingerprint, e.Config.TaskDiskBytes, data); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_export_keys(owner_id,scope,key,fingerprint,task_id) VALUES($1,$2,$3,$4,$5)`, owner, scope, key, fingerprint, id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return e.Get(ctx, owner, scope, id)
}
func (e *Engine) Cancel(ctx context.Context, owner int64, scope, id string) error {
	t, err := e.Get(ctx, owner, scope, id)
	if err != nil {
		return err
	}
	if t.Status == "canceled" {
		return nil
	}
	if t.Status != "queued" && t.Status != "running" {
		return fail(409, "EXPORT_CANCEL_CONFLICT")
	}
	r, err := e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET status='canceled',finished_at=now() WHERE id=$1 AND status IN ('queued','running')`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		if current, er := e.Get(ctx, owner, scope, id); er == nil && current.Status == "canceled" {
			return nil
		}
		return fail(409, "EXPORT_CANCEL_CONFLICT")
	}
	return nil
}
func (e *Engine) Delete(ctx context.Context, owner int64, scope, id string) error {
	t, err := e.Get(ctx, owner, scope, id)
	if err != nil {
		return err
	}
	if t.Status == "queued" || t.Status == "running" {
		return fail(409, "EXPORT_CANCEL_FIRST")
	}
	_, err = e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET status='deleted' WHERE id=$1 AND status NOT IN ('queued','running')`, id)
	return err
}
func (e *Engine) Ticket(ctx context.Context, owner int64, scope, id string, identity TicketIdentity) (string, error) {
	if identity.Owner != owner {
		return "", fail(403, "EXPORT_INVALID_TICKET")
	}
	t, err := e.Get(ctx, owner, scope, id)
	if err != nil {
		return "", err
	}
	if err = downloadable(t); err != nil {
		return "", err
	}
	if err = e.Rate(ctx, owner, "ticket", e.Config.TicketRPM); err != nil {
		return "", err
	}
	var b [32]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	h := sha256.Sum256([]byte(token))
	data, _ := json.Marshal(identity)
	_, err = e.DB.ExecContext(ctx, `INSERT INTO usage_export_tickets(hash,task_id,identity,expires_at) VALUES($1,$2,$3,now()+interval '60 seconds')`, hex.EncodeToString(h[:]), id, data)
	return token, err
}
func downloadable(t *Task) error {
	if t.Status == "expired" || t.Status == "deleted" || t.ExpiresAt != nil && !time.Now().Before(*t.ExpiresAt) {
		return fail(410, "EXPORT_EXPIRED")
	}
	if t.Status != "succeeded" {
		return fail(409, "EXPORT_NOT_READY")
	}
	return nil
}
func (e *Engine) Redeem(ctx context.Context, scope, id, token string) (*Task, TicketIdentity, error) {
	var identity TicketIdentity
	if len(token) != 64 {
		return nil, identity, fail(401, "EXPORT_INVALID_TICKET")
	}
	h := sha256.Sum256([]byte(token))
	var b []byte
	err := e.DB.QueryRowContext(ctx, `DELETE FROM usage_export_tickets WHERE hash=$1 AND task_id=$2 AND expires_at>now() RETURNING identity`, hex.EncodeToString(h[:]), id).Scan(&b)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = fail(401, "EXPORT_INVALID_TICKET")
		}
		return nil, identity, err
	}
	if err = json.Unmarshal(b, &identity); err != nil {
		return nil, identity, err
	}
	t, err := e.Get(ctx, identity.Owner, scope, id)
	if err == nil {
		err = downloadable(t)
	}
	return t, identity, err
}

func (e *Engine) lock(ctx context.Context, key int64) (*sql.Conn, error) {
	conn, err := e.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var ok bool
	err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok)
	if err != nil || !ok {
		_ = conn.Close()
		if err == nil {
			err = fail(429, "EXPORT_BUSY")
		}
		return nil, err
	}
	return conn, nil
}
func unlock(conn *sql.Conn, key int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, key).Scan(&ok); err != nil || !ok {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = conn.Close()
}
func (e *Engine) DownloadSlot(ctx context.Context, owner int64) (func(), error) {
	for i := 0; i < e.Config.DownloadConcurrency; i++ {
		key := int64(1<<50) + owner*64 + int64(i)
		c, err := e.lock(ctx, key)
		if err == nil {
			return func() { unlock(c, key) }, nil
		}
		var f *Fault
		if !errors.As(err, &f) {
			return nil, err
		}
	}
	return nil, &Fault{Status: 429, Code: "EXPORT_DOWNLOAD_BUSY", Retry: 5}
}

func (e *Engine) OpenDownload(ctx context.Context, owner int64, scope, id string) (*Task, io.ReadCloser, error) {
	t, err := e.Get(ctx, owner, scope, id)
	if err == nil {
		err = downloadable(t)
	}
	if err != nil {
		return nil, nil, err
	}
	r, err := e.Store.Open(ctx, t.StorageKey)
	return t, r, err
}

package usageexport

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/xuri/excelize/v2"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func (e *Engine) runSlot(parent context.Context, slot int) {
	key := workerLockBase + int64(slot)
	lock, err := e.lock(parent, key)
	if err != nil {
		return
	}
	defer unlock(lock, key)
	ctx, cancel := context.WithTimeout(parent, time.Duration(e.Config.RunSeconds)*time.Second)
	defer cancel()
	t, err := scanTask(e.DB.QueryRowContext(ctx, `UPDATE usage_export_tasks SET status='running',phase='reading',generation=generation+1,heartbeat_at=now() WHERE id=(SELECT id FROM usage_export_tasks WHERE status='queued' AND created_at>now()-($1*interval '1 second') ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+taskJSON, e.Config.QueueSeconds))
	if err != nil {
		return
	}
	started := time.Now()
	slog.Info("usage export started", "task_id", t.ID, "generation", t.Generation, "queue_ms", started.Sub(t.CreatedAt).Milliseconds())
	dir := filepath.Join(e.Config.Directory, "tmp", fmt.Sprintf("%s-%d", t.ID, t.Generation))
	var stopCode string
	var count atomic.Int64
	done := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				er := lock.PingContext(pingCtx)
				if er == nil {
					var res sql.Result
					res, er = e.DB.ExecContext(pingCtx, `UPDATE usage_export_tasks SET heartbeat_at=now(),processed_rows=$3 WHERE id=$1 AND generation=$2 AND status='running'`, t.ID, t.Generation, count.Load())
					if er == nil {
						n, _ := res.RowsAffected()
						if n == 0 {
							er = context.Canceled
						}
					}
				}
				stop()
				if er != nil {
					cancel()
					return
				}
				var used int64
				_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, er error) error {
					if er == nil && !d.IsDir() {
						i, er := d.Info()
						if er == nil {
							used += i.Size()
						}
					}
					return nil
				})
				if free, er := disk.UsageWithContext(ctx, e.Config.Directory); er != nil || free.Free < uint64(e.Config.FreeBytes) {
					stopCode = "EXPORT_STORAGE_FULL"
					cancel()
					return
				}
				if used > e.Config.TaskDiskBytes {
					stopCode = "EXPORT_SIZE_LIMIT"
					cancel()
					return
				}
			}
		}
	}()
	err = e.execute(ctx, lock, t, dir, &count)
	close(done)
	<-monitorDone
	if err != nil {
		code := "EXPORT_GENERATION_FAILED"
		var f *Fault
		if errors.As(err, &f) {
			code = f.Code
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			code = "EXPORT_TIMEOUT"
		} else if ctx.Err() != nil {
			code = "EXPORT_INTERRUPTED"
		}
		if stopCode != "" {
			code = stopCode
		}
		finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_, updateErr := e.DB.ExecContext(finishCtx, `UPDATE usage_export_tasks SET status='failed',error_code=$3,finished_at=now(),processed_rows=$4 WHERE id=$1 AND generation=$2 AND status='running'`, t.ID, t.Generation, code, count.Load())
		stop()
		if updateErr != nil {
			slog.Warn("usage export failure update failed", "task_id", t.ID)
		}
		slog.Warn("usage export failed", "task_id", t.ID, "code", code, "rows", count.Load(), "duration_ms", time.Since(started).Milliseconds(), "size_bytes", t.Size)
	} else {
		slog.Info("usage export succeeded", "task_id", t.ID, "rows", count.Load(), "duration_ms", time.Since(started).Milliseconds(), "size_bytes", t.Size)
	}
	if er := os.RemoveAll(dir); er != nil {
		slog.Warn("usage export temporary cleanup failed", "task_id", t.ID)
	} else {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err == nil {
			_, _ = e.DB.ExecContext(cleanupCtx, `UPDATE usage_export_tasks SET reserved_bytes=0 WHERE id=$1 AND generation=$2 AND status='succeeded'`, t.ID, t.Generation)
		}
		if err != nil {
			objectKey := fmt.Sprintf("%s/%d.%s", t.ID, t.Generation, t.Options.Format)
			if deleteErr := e.Store.Delete(cleanupCtx, objectKey); deleteErr == nil {
				_, _ = e.DB.ExecContext(cleanupCtx, `UPDATE usage_export_tasks SET cleaned=true,reserved_bytes=0 WHERE id=$1 AND generation=$2 AND status IN ('failed','canceled')`, t.ID, t.Generation)
			}
		}
	}
}
func (e *Engine) phase(ctx context.Context, t *Task, phase string) error {
	res, err := e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET phase=$3 WHERE id=$1 AND generation=$2 AND status='running'`, t.ID, t.Generation, phase)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return context.Canceled
	}
	slog.Info("usage export phase", "task_id", t.ID, "phase", phase)
	return nil
}
func (e *Engine) execute(ctx context.Context, lock *sql.Conn, t *Task, dir string, count *atomic.Int64) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	spool := filepath.Join(dir, "rows.jsonl")
	file, err := os.OpenFile(spool, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	limited := &boundedWriter{Writer: file, Remaining: e.Config.TaskDiskBytes / 2}
	buf := bufio.NewWriterSize(limited, 64<<10)
	encoder := json.NewEncoder(buf)
	readStarted := time.Now()
	readCtx, stop := context.WithTimeout(ctx, time.Duration(e.Config.ReadSeconds)*time.Second)
	tx, err := e.DB.BeginTx(readCtx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		stop()
		_ = file.Close()
		return err
	}
	var snapshot time.Time
	err = tx.QueryRowContext(readCtx, `SELECT clock_timestamp(), pg_current_snapshot()::text`).Scan(&snapshot, new(string))
	if err == nil {
		_, err = e.DB.ExecContext(readCtx, `UPDATE usage_export_tasks SET snapshot_at=$3 WHERE id=$1 AND generation=$2 AND status='running'`, t.ID, t.Generation, snapshot)
	}
	if err == nil {
		err = e.Snapshot(readCtx, tx, t.Options, func(row []string) error {
			if er := readCtx.Err(); er != nil {
				return er
			}
			if count.Load() >= e.Config.MaxRows {
				return fail(422, "EXPORT_ROW_LIMIT")
			}
			if er := encoder.Encode(row); er != nil {
				return er
			}
			count.Add(1)
			return nil
		})
	}
	if err == nil {
		err = buf.Flush()
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if readCtx.Err() != nil && err != nil {
		err = readCtx.Err()
	}
	stop()
	if err != nil {
		return err
	}
	slog.Info("usage export read complete", "task_id", t.ID, "rows", count.Load(), "duration_ms", time.Since(readStarted).Milliseconds())
	total := count.Load()
	t.Total = &total
	if _, err = e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET total_rows=$3,processed_rows=$3 WHERE id=$1 AND generation=$2 AND status='running'`, t.ID, t.Generation, total); err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%d.%s", t.ID, t.Generation, t.Options.Format)
	if _, err = e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET storage_key=$3 WHERE id=$1 AND generation=$2 AND status='running'`, t.ID, t.Generation, key); err != nil {
		return err
	}
	output := filepath.Join(dir, "result."+t.Options.Format)
	for attempt := 0; attempt < 3; attempt++ {
		if err = e.phase(ctx, t, "formatting"); err != nil {
			return err
		}
		err = Render(ctx, spool, output, t.Options, e.Config.MaxFileBytes, dir)
		if err == nil {
			err = validateFile(ctx, output, t.Options.Format)
			if err == nil {
				err = e.phase(ctx, t, "uploading")
			}
		}
		if err == nil {
			err = e.Store.Put(ctx, key, output)
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var f *Fault
		if errors.As(err, &f) {
			return err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err != nil {
		return err
	}
	f, err := os.Open(output)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	_ = f.Close()
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	res, err := lock.ExecContext(ctx, `UPDATE usage_export_tasks SET status='succeeded',phase='completed',size_bytes=$3,sha256=$4,finished_at=now(),expires_at=now()+($5*interval '1 second') WHERE id=$1 AND generation=$2 AND status='running'`, t.ID, t.Generation, size, digest, e.Config.RetentionSeconds)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return context.Canceled
	}
	t.Size = size
	return nil
}

type boundedWriter struct {
	Writer    io.Writer
	Remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.Remaining {
		return 0, fail(422, "EXPORT_SIZE_LIMIT")
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= int64(n)
	return n, err
}
func safeCSV(v string) string {
	trim := strings.TrimLeft(v, " \t\r\n")
	if len(trim) > 0 && strings.ContainsRune("=+-@", rune(trim[0])) {
		return "'" + v
	}
	if strings.HasPrefix(v, "\t") || strings.HasPrefix(v, "\r") || strings.HasPrefix(v, "\n") {
		return "'" + v
	}
	return v
}
func Render(ctx context.Context, spool, output string, o Options, max int64, tmp string) error {
	in, err := os.Open(spool)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	decoder := json.NewDecoder(bufio.NewReaderSize(in, 64<<10))
	out, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	w := &boundedWriter{Writer: out, Remaining: max}
	headers := Headers(o)
	if o.Format == "csv" {
		if _, err = w.Write([]byte("\xef\xbb\xbf")); err != nil {
			return err
		}
		csvw := csv.NewWriter(w)
		csvw.UseCRLF = true
		if err = csvw.Write(headers); err != nil {
			return err
		}
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			var row []string
			err = decoder.Decode(&row)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			for i := range row {
				if !numericColumn(o, i) {
					row[i] = safeCSV(row[i])
				}
			}
			if err = csvw.Write(row); err != nil {
				return err
			}
		}
		csvw.Flush()
		if err = csvw.Error(); err != nil {
			return err
		}
	} else {
		book := excelize.NewFile(excelize.Options{TmpDir: tmp})
		defer func() { _ = book.Close() }()
		if err = book.SetSheetName("Sheet1", "Usage"); err != nil {
			return err
		}
		stream, er := book.NewStreamWriter("Usage")
		if er != nil {
			return er
		}
		values := make([]any, len(headers))
		for i, v := range headers {
			values[i] = v
		}
		if err = stream.SetRow("A1", values); err != nil {
			return err
		}
		idx := 2
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			var row []string
			err = decoder.Decode(&row)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			values = make([]any, len(row))
			for i, v := range row {
				values[i] = v
				if (i >= 14 && i <= 17) || (i >= 27 && i <= 28) {
					if n, er := strconv.ParseInt(v, 10, 64); er == nil && n >= -9007199254740991 && n <= 9007199254740991 {
						values[i] = n
					}
				}
			}
			cell, _ := excelize.CoordinatesToCellName(1, idx)
			if err = stream.SetRow(cell, values); err != nil {
				return err
			}
			idx++
		}
		if err = stream.Flush(); err != nil {
			return err
		}
		// Excelize otherwise buffers the complete ZIP even with a stream writer.
		// Direct its supported ZIP writer hook to the bounded output file.
		book.SetZipWriter(func(io.Writer) excelize.ZipWriter { return zip.NewWriter(&contextWriter{ctx: ctx, Writer: w}) })
		if err = book.Write(io.Discard); err != nil {
			return err
		}
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}
func (e *Engine) Cleanup(ctx context.Context) error {
	if err := e.cleanOrphanSpools(ctx); err != nil {
		return err
	}
	_, err := e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET status=CASE WHEN status='succeeded' THEN 'expired' ELSE 'failed' END,error_code=CASE WHEN status='running' THEN 'EXPORT_INTERRUPTED' WHEN status='queued' THEN 'EXPORT_QUEUE_TIMEOUT' ELSE error_code END,finished_at=COALESCE(finished_at,now()) WHERE (status='succeeded' AND expires_at<=now()) OR (status='queued' AND created_at<now()-($1*interval '1 second')) OR (status='running' AND heartbeat_at<now()-interval '60 seconds')`, e.Config.QueueSeconds)
	if err != nil {
		return err
	}
	rows, err := e.DB.QueryContext(ctx, `SELECT `+taskJSON+` FROM usage_export_tasks WHERE status IN ('failed','canceled','expired','deleted') AND NOT cleaned AND (heartbeat_at IS NULL OR heartbeat_at<now()-interval '60 seconds') LIMIT 100`)
	if err != nil {
		return err
	}
	var tasks []Task
	for rows.Next() {
		t, er := scanTask(rows)
		if er != nil {
			_ = rows.Close()
			return er
		}
		tasks = append(tasks, *t)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.StorageKey != "" {
			if er := e.Store.Delete(ctx, t.StorageKey); er != nil {
				continue
			}
		}
		if er := os.RemoveAll(filepath.Join(e.Config.Directory, "tmp", fmt.Sprintf("%s-%d", t.ID, t.Generation))); er != nil {
			continue
		}
		if _, er := e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET cleaned=true,reserved_bytes=0,size_bytes=0 WHERE id=$1 AND status IN ('failed','canceled','expired','deleted')`, t.ID); er != nil {
			return er
		}
	}
	_, err = e.DB.ExecContext(ctx, `DELETE FROM usage_export_tickets WHERE expires_at<=now()`)
	if err != nil {
		return err
	}
	_, err = e.DB.ExecContext(ctx, `DELETE FROM usage_export_rate WHERE window_at<now()-($1*interval '1 second')`, e.Config.MetadataSeconds)
	if err != nil {
		return err
	}
	_, err = e.DB.ExecContext(ctx, `DELETE FROM usage_export_tasks WHERE cleaned AND created_at<now()-($1*interval '1 second')`, e.Config.MetadataSeconds)
	return err
}

// contextWriter also observes cancellation while compressing a large worksheet.
type contextWriter struct {
	ctx context.Context
	io.Writer
}

func (w *contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.Writer.Write(p)
}

// Validate ZIP members with bounded reads; CRC failures never publish as success.
func validateFile(ctx context.Context, file, format string) error {
	if format != "xlsx" {
		return ctx.Err()
	}
	z, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = z.Close() }()
	worksheet := false
	for _, entry := range z.File {
		if entry.Name == "xl/worksheets/sheet1.xml" {
			worksheet = true
		}
		r, er := entry.Open()
		if er != nil {
			return er
		}
		_, er = io.Copy(&contextWriter{ctx: ctx, Writer: io.Discard}, r)
		_ = r.Close()
		if er != nil {
			return er
		}
	}
	if !worksheet {
		return fmt.Errorf("missing usage worksheet")
	}
	return nil
}
func (e *Engine) cleanOrphanSpools(ctx context.Context) error {
	root := filepath.Join(e.Config.Directory, "tmp")
	dirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		info, er := d.Info()
		if er != nil || time.Since(info.ModTime()) < time.Minute {
			continue
		}
		name := d.Name()
		if len(name) < 38 || name[36] != '-' {
			continue
		}
		id := name[:36]
		var active bool
		er = e.DB.QueryRowContext(ctx, `SELECT status='running' AND heartbeat_at>now()-interval '60 seconds' FROM usage_export_tasks WHERE id=$1`, id).Scan(&active)
		if er != nil && !errors.Is(er, sql.ErrNoRows) {
			return er
		}
		if active {
			continue
		}
		if er = os.RemoveAll(filepath.Join(root, name)); er != nil {
			continue
		}
		_, er = e.DB.ExecContext(ctx, `UPDATE usage_export_tasks SET reserved_bytes=0 WHERE id=$1 AND status='succeeded'`, id)
		if er != nil {
			return er
		}
	}
	return nil
}

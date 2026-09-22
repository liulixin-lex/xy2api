package usageexport

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExportCapacity(t *testing.T) {
	if os.Getenv("USAGE_EXPORT_CAPACITY") != "1" {
		t.Skip("opt-in capacity gate")
	}
	rows := int64(1000000)
	if v := os.Getenv("USAGE_EXPORT_CAPACITY_ROWS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		require.NoError(t, err)
		rows = n
	}
	if os.Getenv("USAGE_EXPORT_CAPACITY_OVERFLOW") == "1" {
		rows++
	}
	format := os.Getenv("USAGE_EXPORT_CAPACITY_FORMAT")
	if format == "" {
		format = "csv"
	}
	e := testEngine(t, func(ctx context.Context, tx *sql.Tx, o Options, emit func([]string) error) error {
		_, err := tx.ExecContext(ctx, `DECLARE capacity_cursor NO SCROLL CURSOR FOR SELECT id FROM generate_series(1,$1::bigint) AS id`, rows)
		if err != nil {
			return err
		}
		for {
			r, err := tx.QueryContext(ctx, `FETCH FORWARD 2000 FROM capacity_cursor`)
			if err != nil {
				return err
			}
			n := 0
			for r.Next() {
				var id int64
				if err = r.Scan(&id); err != nil {
					_ = r.Close()
					return err
				}
				line := make([]string, len(Headers(o)))
				for i := range line {
					line[i] = "1"
				}
				line[0] = "2026-09-22T00:00:00Z"
				line[1] = fmt.Sprintf("key-%d", id)
				line[2] = "gpt-test"
				if err = emit(line); err != nil {
					_ = r.Close()
					return err
				}
				n++
			}
			err = r.Err()
			_ = r.Close()
			if err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
		}
	})
	e.Config.TaskDiskBytes = 2 << 30
	e.Config.MaxFileBytes = 512 << 20
	e.Config.StorageBytes = 4 << 30
	e.Config.FreeBytes = 0
	scope := "user"
	o := options()
	if format == "xlsx" {
		scope = "admin"
		o.Format = "xlsx"
	}
	task, err := e.Create(context.Background(), 1, scope, "capacity-1", o)
	require.NoError(t, err)
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	baseRSS := residentBytes()
	var peakRSS atomic.Uint64
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				rss := residentBytes()
				if rss > peakRSS.Load() {
					peakRSS.Store(rss)
				}
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				used := m.Sys - m.HeapReleased
				if used > peak.Load() {
					peak.Store(used)
				}
			}
		}
	}()
	start := time.Now()
	e.runSlot(context.Background(), 0)
	elapsed := time.Since(start)
	close(stop)
	<-done
	task, err = e.Get(context.Background(), 1, scope, task.ID)
	require.NoError(t, err)
	if rows > 1000000 {
		require.Equal(t, "EXPORT_ROW_LIMIT", task.ErrorCode)
		require.Equal(t, "failed", task.Status)
	} else {
		require.Equal(t, "succeeded", task.Status)
		require.Equal(t, rows, *task.Total)
	}
	baseline := base.Sys - base.HeapReleased
	increment := uint64(0)
	if peak.Load() > baseline {
		increment = peak.Load() - baseline
	}
	t.Logf("CAPACITY format=%s input_rows=%d status=%s output_bytes=%d elapsed_ms=%d go_resident_increment_bytes=%d", format, rows, task.Status, task.Size, elapsed.Milliseconds(), increment)
	rssIncrement := uint64(0)
	if peakRSS.Load() > baseRSS {
		rssIncrement = peakRSS.Load() - baseRSS
	}
	t.Logf("RSS baseline_bytes=%d peak_bytes=%d increment_bytes=%d", baseRSS, peakRSS.Load(), rssIncrement)
	require.Less(t, rssIncrement, uint64(256<<20))
	require.Less(t, elapsed, 15*time.Minute)
	require.Less(t, increment, uint64(256<<20))
}

func residentBytes() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			n, _ := strconv.ParseUint(fields[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}

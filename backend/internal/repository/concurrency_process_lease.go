package repository

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
)

// Process leases of the concurrency slots (xy2api; multi-process review).
//
// Startup cleanup used to drop every slot whose request id did not carry
// the new process's prefix. With two processes on one Redis (blue-green
// cutover, a second replica) that cleared the live slots of the other one,
// so accounts and users could exceed their concurrency until those requests
// ended. Each process now keeps a short lease; startup cleanup keeps the
// slots of every process holding one, and the periodic slot worker sweeps a
// skipped process once its lease was missing on two sweeps in a row (a
// crashed process within about processLeaseTTL plus two worker intervals;
// one missed refresh during a Redis hiccup does not clear a live process).
// The sweep leaves the shared wait counts alone: this process's waiters are
// in them, and a stale count expires with its TTL.
//
// Processes without a lease (0.4.1 and earlier) cannot be told from dead
// ones: the first blue-green cutover from such a version, and a rollback to
// one, still clear the other process's slots (runbook §3.3).

const (
	processLeasePrefix   = "concurrency:process_lease:"
	processLeaseTTL      = 15 * time.Second
	processLeaseInterval = 5 * time.Second
	processLeaseOpTO     = 2 * time.Second
	// processLeaseMissesBeforeSweep is how many sweeps in a row must miss a
	// process's lease before its kept slots are cleared.
	processLeaseMissesBeforeSweep = 2
)

type processLease struct {
	once   sync.Once
	prefix string

	mu      sync.Mutex
	pending map[string]int // other processes whose slots were kept -> sweeps that missed their lease
}

func processLeaseKey(prefix string) string { return processLeasePrefix + prefix }

// startProcessLease writes the process's lease and keeps it fresh.
func (c *concurrencyCache) startProcessLease(ctx context.Context, prefix string) {
	c.lease.once.Do(func() {
		c.lease.prefix = prefix
		c.refreshProcessLease(ctx)
		go func() {
			ticker := time.NewTicker(processLeaseInterval)
			defer ticker.Stop()
			for range ticker.C {
				c.refreshProcessLease(context.Background())
			}
		}()
	})
}

func (c *concurrencyCache) refreshProcessLease(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), processLeaseOpTO)
	defer cancel()
	if err := c.rdb.Set(opCtx, processLeaseKey(c.lease.prefix), time.Now().Unix(), processLeaseTTL).Err(); err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: refresh process lease failed: %v", err)
	}
}

// liveProcessPrefixes returns the request-id prefixes of processes holding a
// lease, the caller's own included.
func (c *concurrencyCache) liveProcessPrefixes(ctx context.Context, own string) ([]string, error) {
	live := []string{own}
	var cursor uint64
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, processLeasePrefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			// A client-side key namespace may precede the lease prefix.
			i := strings.LastIndex(k, processLeasePrefix)
			if i < 0 {
				continue
			}
			if p := k[i+len(processLeasePrefix):]; p != "" && p != own {
				live = append(live, p)
			}
		}
		cursor = next
		if cursor == 0 {
			return live, nil
		}
	}
}

// keepPendingProcesses remembers processes whose slots startup kept.
func (c *concurrencyCache) keepPendingProcesses(live []string, own string) {
	c.lease.mu.Lock()
	defer c.lease.mu.Unlock()
	for _, p := range live {
		if p == own {
			continue
		}
		if c.lease.pending == nil {
			c.lease.pending = map[string]int{}
		}
		c.lease.pending[p] = 0
	}
}

// sweepEndedProcesses repeats the stale-slot cleanup once a process whose
// slots startup kept has missed its lease on two sweeps in a row.
func (c *concurrencyCache) sweepEndedProcesses(ctx context.Context) error {
	c.lease.mu.Lock()
	if len(c.lease.pending) == 0 || c.lease.prefix == "" {
		c.lease.mu.Unlock()
		return nil
	}
	pending := make([]string, 0, len(c.lease.pending))
	for p := range c.lease.pending {
		pending = append(pending, p)
	}
	own := c.lease.prefix
	c.lease.mu.Unlock()

	live, err := c.liveProcessPrefixes(ctx, own)
	if err != nil {
		return err
	}
	alive := make(map[string]struct{}, len(live))
	for _, p := range live {
		alive[p] = struct{}{}
	}
	var ended []string
	// keep: processes with a lease, and pending ones that missed it fewer
	// than processLeaseMissesBeforeSweep times so far.
	keep := append([]string(nil), live...)
	c.lease.mu.Lock()
	for _, p := range pending {
		if _, ok := alive[p]; ok {
			c.lease.pending[p] = 0
			continue
		}
		c.lease.pending[p]++
		if c.lease.pending[p] >= processLeaseMissesBeforeSweep {
			ended = append(ended, p)
		} else {
			keep = append(keep, p)
		}
	}
	c.lease.mu.Unlock()
	if len(ended) == 0 {
		return nil
	}
	if err := c.cleanupDeadProcessSlots(ctx, keep, false); err != nil {
		return err
	}
	c.lease.mu.Lock()
	for _, p := range ended {
		delete(c.lease.pending, p)
	}
	c.lease.mu.Unlock()
	return nil
}

// leaseMemberPrefixes passes the process prefixes to the cleanup script;
// slot members start with their process's prefix.
func leaseMemberPrefixes(live []string) []any {
	out := make([]any, 0, len(live))
	for _, p := range live {
		out = append(out, p)
	}
	return out
}

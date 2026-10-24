package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const cacheTraceDefaultByteBudget int64 = 64 << 20
const cacheTraceDefaultWorkers = 1
const cacheTraceDefaultBytesPerSecond int64 = 32 << 20
const cacheTraceDefaultQueueWait = 10 * time.Second
const cacheTraceAsyncLifetime = 10 * time.Minute

// No request context, Gin object, headers, account, or raw identifiers cross
// the worker boundary. The ingress slice and GetBody reader own immutable
// backing arrays: HTTP builders use bytes.NewReader; sjson patches allocate,
// and mapped Lite replaces GetBody with a closure over its own saved copy.
// Retrying builds another request and never writes these arrays. Holding the
// reference keeps it alive independently of the request's lifetime.
type cacheTraceJob struct {
	policy                       *CachePolicySnapshot
	pool                         *cacheTraceWorkers
	cost                         int64
	next                         *cacheTraceJob
	state                        *cacheTraceAsync
	service                      *OpenAIGatewayService
	ingress                      []byte
	apiKeyID                     int64
	headerSource                 bool
	session, protocol, transport string
	getBody                      func() (io.ReadCloser, error)
	size                         int64
	created                      time.Time
	sentAt                       time.Time
	queuedAt                     time.Time
	history                      *cacheTraceSessionState
}

type cacheTraceComputed struct {
	prefix                     cachePrefixObservation
	chain                      cachePrefixChain
	anchor, pckIn, idSource    string
	pckExists, pckUUID, thread bool
}

type cacheTraceSettlement struct {
	owner  *cacheTraceSessionState
	prompt int
	at     time.Time
}

// Atomic publication lets response/settlement take a snapshot without waiting
// for JSON, hashing, queue locks, or an active worker. History publication needs
// both a delivered settlement and a computed chain, in either order.
type cacheTraceAsync struct {
	result        atomic.Pointer[cacheTraceComputed]
	settled       atomic.Pointer[cacheTraceSettlement]
	published     atomic.Bool
	reason        atomic.Uint32
	chainExcluded bool
}

func (a *cacheTraceAsync) publish(s *OpenAIGatewayService, session string) {
	r, settlement := a.result.Load(), a.settled.Load()
	if r == nil || settlement == nil || len(r.chain.hashes) == 0 || time.Since(settlement.at) > cacheTraceAsyncLifetime {
		return
	}
	if !a.published.CompareAndSwap(false, true) {
		return
	}
	chain := r.chain
	chain.settledAt = settlement.at
	s.rememberCachePrefix(session, chain, settlement.prompt, settlement.owner)
}

// Workers exist only while there is queued work; idle test services and closed
// requests cannot leak a goroutine. The budget includes both retained bodies,
// the worker's output-body read copy, and a conservative metadata allowance.
// A tiny body therefore cannot create an unbounded number of queue nodes.
type cacheTraceWorkers struct {
	maxQueueWait   time.Duration
	mu             sync.Mutex
	head, tail     *cacheTraceJob
	reserved       atomic.Int64 // pending + queued + active ownership, reserved before job allocation
	bytes          int64        // queued + active; guarded by mu for existing queue diagnostics
	active         int
	budget         int64 // accessed atomically after pool publication
	workers        int
	bytesPerSecond int64
	limiter        *rate.Limiter
}

func (s *OpenAIGatewayService) cacheTraceWorkerPool() *cacheTraceWorkers {
	s.cacheTraceWorkersOnce.Do(func() {
		budget, n := cacheTraceDefaultByteBudget, s.cfg.Gateway.CacheTrace.Workers
		if budget <= 0 {
			budget = cacheTraceDefaultByteBudget
		}
		if n <= 0 {
			n = cacheTraceDefaultWorkers
		}
		rate := cacheTraceDefaultBytesPerSecond
		if rate <= 0 {
			rate = cacheTraceDefaultBytesPerSecond
		}
		wait := s.cfg.Gateway.CacheTrace.MaxQueueWait
		if wait <= 0 {
			wait = cacheTraceDefaultQueueWait
		}
		s.cacheTraceWorkers = &cacheTraceWorkers{budget: budget, workers: n, bytesPerSecond: rate, maxQueueWait: wait}
		if s.cachePolicies != nil {
			s.cachePolicies.subscribe(s.cacheTraceWorkers.applyPolicy)
		}
	})
	return s.cacheTraceWorkers
}

// reserve is the first admission operation. No job, body snapshot, history
// lookup, hashing, or parsing may precede it. Include the fallback read copy
// even when the worker can borrow the source without a scratch allocation.
func (p *cacheTraceWorkers) reserve(ingress, output int64) (int64, bool) {
	budget := atomic.LoadInt64(&p.budget)
	if ingress < 0 || output < 0 || ingress > budget-1024 || output > (budget-ingress-1024)/2 {
		return 0, false
	}
	cost := ingress + 2*output + 1024
	for used := p.reserved.Load(); used <= budget-cost; used = p.reserved.Load() {
		if p.reserved.CompareAndSwap(used, used+cost) {
			// A shrink may have published after the first budget load.
			// Undo this admission if it exceeds the now-current limit.
			if used+cost > atomic.LoadInt64(&p.budget) {
				p.reserved.Add(-cost)
				return 0, false
			}
			return cost, true
		}
	}
	return 0, false
}

// A pending attempt owns its reservation until enqueue or cancellation. Once
// queued, only the worker releases it. Retrying never cancels a running job.
func (j *cacheTraceJob) cancelPending() {
	if j != nil && j.pool != nil {
		j.ingress, j.getBody = nil, nil
		j.pool.reserved.Add(-j.cost)
		j.pool, j.cost = nil, 0
	}
}

func (p *cacheTraceWorkers) submit(j *cacheTraceJob) bool {
	if j.pool == nil {
		cost, ok := p.reserve(int64(len(j.ingress)), j.size)
		if !ok {
			if j.state != nil {
				j.state.reason.Store(cacheTraceBudgetFull)
			}
			return false
		}
		j.pool, j.cost = p, cost
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if j.queuedAt.IsZero() {
		j.queuedAt = time.Now()
	}
	cost := j.cost
	p.bytes += cost
	if p.tail == nil {
		p.head = j
	} else {
		p.tail.next = j
	}
	p.tail = j
	if p.active < p.workers {
		p.active++
		go p.run()
	}
	return true
}

func (p *cacheTraceWorkers) run() {
	for {
		p.mu.Lock()
		j := p.head
		if j == nil {
			p.active--
			p.mu.Unlock()
			return
		}
		p.head = j.next
		if p.head == nil {
			p.tail = nil
		}
		j.next = nil
		if p.limiter == nil {
			bytesPerSecond := p.bytesPerSecond
			if bytesPerSecond <= 0 {
				bytesPerSecond = cacheTraceDefaultBytesPerSecond
			}
			// A policy may shrink the budget after admission but before this
			// first worker starts. Preserve those reservations in the burst.
			burst := atomic.LoadInt64(&p.budget)
			if reserved := p.reserved.Load(); reserved > burst {
				burst = reserved
			}
			p.limiter = rate.NewLimiter(rate.Limit(bytesPerSecond), int(burst))
		}
		limiter := p.limiter
		p.mu.Unlock()
		cost := j.cost
		wait := p.maxQueueWait
		if wait <= 0 {
			wait = cacheTraceDefaultQueueWait
		}
		deadline := j.queuedAt.Add(wait)
		expire := func() {
			if j.state != nil {
				j.state.reason.Store(cacheTraceExpired)
			}
		}
		func() {
			// Body sources are in-memory readers. Recover an unexpected parser/body
			// panic as a coverage gap; never leak budget or terminate the process.
			defer func() { _ = recover() }()
			if time.Now().After(deadline) || j.historyEvicted() {
				expire()
				return
			}
			// Waiting work remains charged to p.bytes. Admission absorbs the
			// burst; only expiration or a full byte budget discards work.
			// A private deadline also bounds waiting if the request ends.
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			err := limiter.WaitN(ctx, int(int64(len(j.ingress))+j.size))
			// WaitN may reject a reservation before the deadline when it predicts
			// insufficient tokens. Keep the admitted task and its budget until
			// actual expiration; a rate shortage must not cause an early discard.
			if err != nil && ctx.Err() == nil {
				<-ctx.Done()
			}
			cancel()
			if err != nil || time.Now().After(deadline) || j.historyEvicted() {
				expire()
				return
			}
			j.compute()
		}()
		// Release body closures before returning their budget to the pool.
		j.ingress, j.getBody = nil, nil
		p.mu.Lock()
		p.bytes -= cost
		p.reserved.Add(-cost)
		p.mu.Unlock()
	}
}

// A complete WriteTo grant is borrowed only during Write. Neither this adapter
// nor the computed result may retain the body. Sources with multiple writes or
// no WriterTo use the existing budgeted copy fallback. Publication waits for a
// successful reader return, even when a reader writes a full body then errors.
var errCacheTracePartialBody = errors.New("cache trace incomplete body grant")

type cacheTraceBodyObservation struct {
	size     int64
	observe  func([]byte) *cacheTraceComputed
	result   *cacheTraceComputed
	fallback bool
}

func (w *cacheTraceBodyObservation) Write(body []byte) (int, error) {
	if w.result != nil || int64(len(body)) != w.size {
		w.fallback = true
		return 0, errCacheTracePartialBody
	}
	w.result = w.observe(body)
	return len(body), nil
}

func (j *cacheTraceJob) compute() {
	reader, err := j.getBody()
	if err != nil {
		return
	}
	// This is the only production call site. Results contain only owned HMAC
	// strings, enum constants, counts and process-local xxhash checkpoints.
	cutoff := j.sentAt
	if cutoff.IsZero() {
		cutoff = j.created // adapters that do not dispatch retain their old cutoff
	}
	compute := func(body []byte) *cacheTraceComputed {
		return j.service.computeCacheTrace(j.ingress, body, j.apiKeyID, j.headerSource, j.session, j.protocol, j.transport, j.policy, cutoff)
	}
	var result *cacheTraceComputed
	if direct, ok := reader.(io.WriterTo); ok {
		view := &cacheTraceBodyObservation{size: j.size, observe: compute}
		_, err = direct.WriteTo(view)
		_ = reader.Close()
		if view.fallback {
			reader, err = j.getBody()
			if err != nil {
				return
			}
		} else {
			if err != nil {
				return
			}
			result = view.result
			if result == nil {
				if j.size != 0 {
					return
				}
				result = compute(nil)
			}
		}
	}
	if result == nil {
		body := make([]byte, int(j.size))
		_, err = io.ReadFull(reader, body)
		_ = reader.Close()
		if err != nil {
			return
		}
		result = compute(body)
	}
	if j.historyEvicted() {
		j.state.reason.Store(cacheTraceExpired)
		return
	}
	// A cold request cannot become a missing-history request because its own
	// legacy usage entry was installed while the worker was scanning.
	if j.history == nil && result.prefix.ForkRef == "none" {
		result.prefix.HistoryUnsampled = false
	}
	j.state.result.Store(result)
	j.state.publish(j.service, j.session)
}

// Coverage reasons are internal enums; no body, hash, or upstream text can
// enter them. The settlement layer emits this only for n/a_sampled_out.
const (
	cacheTraceBudgetFull uint32 = iota + 1
	cacheTraceExpired
)

func (a *cacheTraceAsync) naReason() string {
	if a == nil {
		return "not_ready"
	}
	return a.coverageReason(a.result.Load())
}
func (a *cacheTraceAsync) coverageReason(r *cacheTraceComputed) string {
	if a == nil {
		return "not_ready"
	}
	if a.chainExcluded {
		return "chain_percent"
	}
	switch a.reason.Load() {
	case cacheTraceBudgetFull:
		return "budget_full"
	case cacheTraceExpired:
		return "expired"
	}
	if r == nil {
		return "not_ready"
	}
	if !r.prefix.Sampled {
		return "chain_percent"
	}
	if r.prefix.HistoryUnsampled {
		return "hist_unavailable"
	}
	return ""
}

// Only an already established trace history is pinned for this comparison.
// A missing first-request history is valid cold traffic, not an eviction.
func (j *cacheTraceJob) historyEvicted() bool {
	if j.history == nil || j.service == nil {
		return false
	}
	telemetry := j.service.cacheTelemetry()
	telemetry.mu.Lock()
	current, exists := telemetry.sessions[j.session]
	telemetry.mu.Unlock()
	return !exists || current.traceChains != j.history
}

// applyPolicy is called under the policy publisher lock. It never allocates a
// new pool or cancels admitted work. Lower budgets reject new admissions until
// existing reservations drain; running requests retain their sampling snapshot.
func (p *cacheTraceWorkers) applyPolicy(policy *CachePolicySnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	atomic.StoreInt64(&p.budget, policy.ByteBudget())
	p.bytesPerSecond = policy.RateBytesPerSecond()
	if p.limiter != nil {
		now := time.Now()
		p.limiter.SetLimitAt(now, rate.Limit(p.bytesPerSecond))
		burst := atomic.LoadInt64(&p.budget)
		if reserved := p.reserved.Load(); reserved > burst {
			burst = reserved
		}
		p.limiter.SetBurstAt(now, int(burst))
	}
}

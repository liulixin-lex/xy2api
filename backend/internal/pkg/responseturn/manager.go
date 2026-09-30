package responseturn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

type idempotencyID struct {
	Scope Scope
	Key   string
}
type responseID struct {
	Scope Scope
	ID    string
}

// Manager is process-local by design. Close cancels execution, and no persistent
// or cross-instance recovery guarantee is made by this implementation.
type Manager struct {
	mu           sync.Mutex
	cfg          Config
	turns        map[string]*Turn
	responses    map[responseID]*Turn
	keys         map[idempotencyID]*Turn
	tenantBytes  map[int64]int64
	processBytes int64
	closed       bool
	done         chan struct{}
}

type Turn struct {
	cancelStarted      bool
	backgroundAccepted bool
	onCancel           func(Reason)
	model              string
	background         bool
	firstEventAt       time.Time
	firstContentAt     time.Time
	replayEvents       uint64
	cancelRequested    bool
	cancelConfirmed    bool
	manager            *Manager
	// Publication and downstream writes are serialized independently. The manager
	// mutex is never held over a network write or waiting on a consumer.
	publishMu      sync.Mutex
	writeMu        sync.Mutex
	id             string
	attemptID      string
	scope          Scope
	key            string
	hash           [32]byte
	created        time.Time
	finished       time.Time
	ctx            context.Context
	cancel         context.CancelCauseFunc
	deadlineCancel context.CancelFunc
	stopControl    func() bool
	policyVersion  string
	stream         bool
	httpStatus     int
	store          bool
	recoverable    bool
	unavailable    string
	owner          int64
	responseID     string
	state          State
	reason         Reason
	events         []*Event
	identities     map[string][32]byte
	journalBytes   int64
	sequence       uint64
	lastNative     int64
	hasNative      bool
	result         []byte
	resultBytes    int64
	expired        bool
	offlineUsed    time.Duration
	detachedAt     time.Time
	epoch          uint64
	attachment     *Attachment
	terminal       chan struct{}
	publishing     bool
	terminalClosed bool
	settle         sync.Once
}

func NewManager(cfg Config) *Manager {
	cfg = cfg.defaults()
	m := &Manager{cfg: cfg, turns: map[string]*Turn{}, responses: map[responseID]*Turn{},
		keys: map[idempotencyID]*Turn{}, tenantBytes: map[int64]int64{}, done: make(chan struct{})}
	if !cfg.DisableBackground {
		go m.run()
	}
	return m
}
func (m *Manager) run() {
	ticker := time.NewTicker(m.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.Sweep()
		}
	}
}
func newID(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(bytes[:]), nil
}

// Create reserves identity atomically. created=false NEVER authorizes a second
// upstream call or writer takeover. A matching expired tombstone stays expired
// for the entire idempotency retention period.
func (m *Manager) Create(o CreateOptions) (*Turn, bool, error) {
	body, canonical, err := decodeBody(o.Body)
	if err != nil {
		return nil, false, err
	}
	if o.Scope.UserID <= 0 || o.Scope.APIKeyID <= 0 || o.Scope.Interface == "" {
		return nil, false, ErrNotFound
	}
	if len(o.IdempotencyKey) > 1024 {
		return nil, false, &Error{"invalid_idempotency_key", 400, "Idempotency key is too long"}
	}
	hash := sha256.Sum256(canonical)
	now := m.cfg.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false, ErrClosed
	}
	if o.IdempotencyKey != "" {
		key := idempotencyID{o.Scope, o.IdempotencyKey}
		if t := m.keys[key]; t != nil {
			m.sweepTurnLocked(t, now)
		}
		if t := m.keys[key]; t != nil {
			if t.hash != hash {
				return nil, false, ErrConflict
			}
			if t.expired {
				return nil, false, ErrExpired
			}
			return t, false, nil
		}
	}
	if len(m.turns) >= m.cfg.MaxTurns {
		m.sweepLocked(now)
		if len(m.turns) >= m.cfg.MaxTurns {
			return nil, false, ErrCapacity
		}
	}
	id, err := newID("turn_")
	if err != nil {
		return nil, false, err
	}
	attempt, err := newID("attempt_")
	if err != nil {
		return nil, false, err
	}
	control := o.ControlContext
	if control == nil {
		control = context.Background()
	}
	deadlineCancel := func() {}
	if !o.Deadline.IsZero() {
		control, deadlineCancel = context.WithDeadline(control, o.Deadline)
	}
	execution, cancel := context.WithCancelCause(control)
	stream, _ := body["stream"].(bool)
	model, _ := body["model"].(string)
	background, _ := body["background"].(bool)
	store := body["store"] != false
	capable := o.Capabilities.Verified && o.Capabilities.Protocol != "" && o.Capabilities.Retrieve && o.Capabilities.NativeCursor
	eligible := stream && background && store && capable
	unavailable := ""
	if !background {
		unavailable = "native_background_intent_required"
	} else if !store {
		unavailable = "store_disabled"
	} else if !capable {
		unavailable = "native_capability_unverified"
	}
	t := &Turn{onCancel: o.OnCancel, manager: m, id: id, attemptID: attempt, scope: o.Scope, key: o.IdempotencyKey, hash: hash, created: now,
		ctx: execution, cancel: cancel, deadlineCancel: deadlineCancel, policyVersion: o.PolicyVersion,
		stream: stream, store: store, recoverable: eligible, background: background, model: model, unavailable: unavailable, state: StateRunning,
		identities: map[string][32]byte{}, terminal: make(chan struct{})}
	m.turns[id] = t
	if o.IdempotencyKey != "" {
		m.keys[idempotencyID{o.Scope, o.IdempotencyKey}] = t
	}
	// Only the caller's explicit host-control context is monitored. The package
	// never calls WithoutCancel or erases a host deadline/cancellation.
	t.stopControl = context.AfterFunc(execution, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !t.isTerminalLocked() {
			t.finishLocked(StateCancelled, CancellationReason(context.Cause(execution)), m.cfg.Now())
		}
	})
	return t, true, nil
}

func (t *Turn) Context() context.Context { return t.ctx }
func (t *Turn) ID() string               { return t.id }
func (t *Turn) AttemptID() string        { return t.attemptID }

// BindOwner permits account-first then response-ID binding, never rebinding a
// nonzero/nonempty owner. Response IDs are scoped, so they confer no authority.
func (t *Turn) BindOwner(accountID int64, upstreamResponseID string) error {
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	return t.bindOwnerLocked(accountID, upstreamResponseID)
}
func (t *Turn) bindOwnerLocked(accountID int64, upstreamResponseID string) error {
	m := t.manager
	if accountID < 0 || (t.owner != 0 && accountID != 0 && t.owner != accountID) {
		return ErrOwner
	}
	if t.responseID != "" && upstreamResponseID != "" && t.responseID != upstreamResponseID {
		return ErrOwner
	}
	if upstreamResponseID != "" {
		key := responseID{t.scope, upstreamResponseID}
		if old := m.responses[key]; old != nil && old != t {
			return ErrOwner
		}
		m.responses[key] = t
		t.responseID = upstreamResponseID
	}
	if accountID != 0 {
		t.owner = accountID
	}
	return nil
}

// Lookup requires freshly authenticated scope. The caller must additionally
// validate current group/access permissions, since this package has no DB.
func (m *Manager) Lookup(scope Scope, id string) (*Turn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	t := m.responses[responseID{scope, id}]
	if t == nil {
		t = m.turns[id]
	}
	if t == nil || t.scope != scope {
		return nil, ErrNotFound
	}
	m.sweepTurnLocked(t, m.cfg.Now())
	if m.turns[t.id] != t {
		return nil, ErrNotFound
	}
	if t.expired {
		return nil, ErrExpired
	}
	return t, nil
}

func (t *Turn) isTerminalLocked() bool {
	return t.state == StateCompleted || t.state == StatePartial || t.state == StateFailed || t.state == StateCancelled
}
func (t *Turn) offlineUsedLocked(now time.Time) time.Duration {
	used := t.offlineUsed
	if !t.detachedAt.IsZero() && now.After(t.detachedAt) {
		used += now.Sub(t.detachedAt)
	}
	return used
}
func (t *Turn) finishLocked(state State, reason Reason, now time.Time) {
	if t.isTerminalLocked() {
		return
	}
	if !t.detachedAt.IsZero() {
		t.offlineUsed = t.offlineUsedLocked(now)
		t.detachedAt = time.Time{}
	}

	t.state = state
	t.reason = reason
	t.finished = now
	// A verified upstream terminal commits before delivery. Delay only the EOF
	// signal until the in-flight event has reached its bounded delivery queue.
	if !t.publishing {
		t.closeTerminalLocked()
	}
	if !t.store {
		t.clearBodyLocked()
		t.expired = true
	}
	if (state == StateCancelled || state == StateFailed) && (!t.publishing || reason != "") {
		// Cancellation is a control action, not a notification callback outcome.
		// The cause remains available even if the host callback blocks.
		t.cancel(&Cancellation{reason})
		if t.onCancel != nil {
			callback := t.onCancel
			go func() {
				// Callbacks may safely inspect Snapshot without reentering the manager.
				callback(reason)
			}()
		}
	}
}
func (t *Turn) closeTerminalLocked() {
	if !t.terminalClosed {
		t.terminalClosed = true
		close(t.terminal)
	}
}
func (t *Turn) cancelLocked(reason Reason, now time.Time) {
	if t.isTerminalLocked() {
		return
	}
	t.finishLocked(StateCancelled, reason, now)
}
func (t *Turn) Cancel(reason Reason) bool {
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.isTerminalLocked() {
		return false
	}
	if reason == "" {
		reason = ReasonUserStop
	}
	t.cancelLocked(reason, m.cfg.Now())
	return true
}

// Finish records a non-event terminal (e.g. verified retrieval or transport
// failure). A disconnected upstream must not be re-created by this package.
func (t *Turn) Finish(state State, reason Reason, result []byte) error {
	if state != StateCompleted && state != StatePartial && state != StateFailed && state != StateCancelled {
		return ErrTerminal
	}
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.isTerminalLocked() {
		return ErrTerminal
	}
	if len(result) > 0 && t.store {
		if !t.reserveResultLocked(result) {
			t.unavailable = "recovery_quota_exceeded"
			t.recoverable = false
		}
	}
	t.finishLocked(state, reason, m.cfg.Now())
	return nil
}

func (t *Turn) Snapshot() Snapshot {
	m := t.manager
	m.mu.Lock()
	m.sweepTurnLocked(t, m.cfg.Now())
	used := t.offlineUsedLocked(m.cfg.Now())
	remaining := m.cfg.OfflineBudget - used
	if remaining < 0 {
		remaining = 0
	}
	result := t.result // Stored slices are immutable and replaced, never reused.
	status := t.httpStatus
	if status == 0 {
		status = 200
	}
	snapshot := Snapshot{HTTPStatus: status, BackgroundAccepted: t.backgroundAccepted, Model: t.model, FirstEventAt: t.firstEventAt, FirstContentAt: t.firstContentAt, ReplayEvents: t.replayEvents, Attachments: t.epoch, CancelRequested: t.cancelRequested, CancelConfirmed: t.cancelConfirmed, TurnID: t.id, AttemptID: t.attemptID, UpstreamResponseID: t.responseID, OwnerAccountID: t.owner,
		Scope: t.scope, State: t.state, Reason: t.reason, Recoverable: t.recoverable && !t.expired,
		RecoveryUnavailableReason: t.unavailable, Stream: t.stream, Store: t.store, PolicyVersion: t.policyVersion,
		CreatedAt: t.created, FinishedAt: t.finished, OfflineUsed: used, OfflineRemaining: remaining,
		JournalBytes: t.journalBytes + t.resultBytes, JournalEvents: len(t.events), AttachmentEpoch: t.epoch,
		Attached: t.attachment != nil}
	m.mu.Unlock()
	snapshot.Result = append([]byte(nil), result...)
	return snapshot
}

// SettleOnce is a per-execution fence, not a substitute for the existing durable
// billing idempotency transaction. Query/replay/attach must not call settlement.
func (t *Turn) SettleOnce(f func()) { t.settle.Do(f) }

func (t *Turn) addBytesLocked(n int64) {
	t.manager.processBytes += n
	t.manager.tenantBytes[t.scope.UserID] += n
	if t.manager.tenantBytes[t.scope.UserID] == 0 {
		delete(t.manager.tenantBytes, t.scope.UserID)
	}
}
func (t *Turn) fitsLocked(n int64) bool {
	m := t.manager
	return t.journalBytes+t.resultBytes+n <= m.cfg.MaxTurnBytes && m.tenantBytes[t.scope.UserID]+n <= m.cfg.MaxTenantBytes && m.processBytes+n <= m.cfg.MaxProcessBytes
}
func (t *Turn) clearBodyLocked() {
	t.addBytesLocked(-t.journalBytes - t.resultBytes)
	t.journalBytes = 0
	t.resultBytes = 0
	t.events = nil
	t.result = nil
	t.identities = map[string][32]byte{}
}

// An attached replay still owns its unread historical events after recovery is
// disabled. Keep charging those bytes until they are consumed or detached;
// otherwise another turn could reuse quota while old replay retained its body.
func (t *Turn) degradeJournalLocked(a *Attachment) {
	retained := int64(0)
	if a != nil {
		for _, event := range a.replay[a.replayAt:] {
			if event != nil {
				retained += event.cost()
			}
		}
	}
	t.clearBodyLocked()
	if retained > 0 {
		a.retainedBytes = retained
		t.journalBytes = retained
		t.addBytesLocked(retained)
	}
}
func (t *Turn) reserveResultLocked(result []byte) bool {
	cost := int64(len(result))
	if !t.fitsLocked(cost - t.resultBytes) {
		return false
	}
	t.addBytesLocked(cost - t.resultBytes)
	t.resultBytes = cost
	t.result = append([]byte(nil), result...)
	return true
}

func (m *Manager) Sweep() { m.mu.Lock(); defer m.mu.Unlock(); m.sweepLocked(m.cfg.Now()) }
func (m *Manager) sweepLocked(now time.Time) {
	for _, t := range m.turns {
		m.sweepTurnLocked(t, now)
	}
}

// Hot paths inspect their own execution; only the periodic/capacity sweep
// scans retained tombstones process-wide.
func (m *Manager) sweepTurnLocked(t *Turn, now time.Time) {
	if m.turns[t.id] != t {
		return
	}
	if !t.isTerminalLocked() {
		if err := t.ctx.Err(); err != nil {
			t.cancelLocked(CancellationReason(context.Cause(t.ctx)), now)
		}
		if t.stream && !t.detachedAt.IsZero() && t.offlineUsedLocked(now) >= m.cfg.OfflineBudget {
			t.cancelLocked(ReasonOfflineBudget, now)
		}
	}
	if t.isTerminalLocked() && !t.expired && now.Sub(t.finished) >= m.cfg.TerminalTTL {
		if t.attachment != nil {
			t.attachment.closeLocked()
			t.attachment = nil
		}
		t.clearBodyLocked()
		t.expired = true
		t.recoverable = false
		t.unavailable = "result_expired"
	}
	if t.isTerminalLocked() && t.expired && now.Sub(t.created) >= m.cfg.IdempotencyTTL {
		if t.stopControl != nil {
			t.stopControl()
		}
		t.deadlineCancel()
		t.cancel(&Cancellation{t.reason})
		if t.attachment != nil {
			t.attachment.closeLocked()
			t.attachment = nil
		}
		t.clearBodyLocked()
		delete(m.turns, t.id)
		if t.key != "" {
			delete(m.keys, idempotencyID{t.scope, t.key})
		}
		if t.responseID != "" {
			delete(m.responses, responseID{t.scope, t.responseID})
		}
	}
}
func (m *Manager) ResourceUsage() ResourceUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	tenants := make(map[int64]int64, len(m.tenantBytes))
	for k, v := range m.tenantBytes {
		tenants[k] = v
	}
	return ResourceUsage{ProcessBytes: m.processBytes, TenantBytes: tenants, Turns: len(m.turns)}
}
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	close(m.done)
	for _, t := range m.turns {
		t.cancelLocked(ReasonAdminCancel, m.cfg.Now())
		t.cancel(&Cancellation{ReasonAdminCancel})
		t.deadlineCancel()
		if t.stopControl != nil {
			t.stopControl()
		}
		if t.attachment != nil {
			t.attachment.closeLocked()
			t.attachment = nil
		}
		t.clearBodyLocked()
	}
	return nil
}

func eventFingerprint(e Event) [32]byte {
	h := sha256.New()
	_, _ = h.Write(e.Data)
	_, _ = fmt.Fprintf(h, "\x00%s\x00%s\x00%s\x00%d\x00%t", e.Identity, e.ResponseID, e.Terminal, e.HTTPStatus, e.JSONResponse)
	if e.NativeSequence != nil {
		_, _ = fmt.Fprintf(h, "\x00%d", *e.NativeSequence)
	}
	_, _ = h.Write(e.Result)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ConfirmRecovery upgrades a pending admission only after the transport observed
// verified native background identity and sequence capabilities. No previous
// non-journaled protocol event may have been sent.
func (t *Turn) ConfirmRecovery(c Capabilities) error {
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.isTerminalLocked() || t.expired {
		return ErrTerminal
	}
	if t.recoverable {
		return nil
	}
	if !t.background || !t.store || t.owner <= 0 || !c.Verified || c.Protocol == "" || !c.Retrieve || !c.NativeCursor || t.sequence != 0 {
		return ErrUnavailable
	}
	t.recoverable = true
	t.backgroundAccepted = true
	t.unavailable = ""
	return nil
}

// BeginUpstreamCancel fences a dedicated cancel request for the original
// accepted background response. It is not generation or a scheduling attempt.
func (t *Turn) BeginUpstreamCancel() bool {
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.cancelStarted || !t.backgroundAccepted || t.owner <= 0 || t.responseID == "" {
		return false
	}
	t.cancelStarted = true
	return true
}
func (t *Turn) ConfirmUpstreamCancel() {
	m := t.manager
	m.mu.Lock()
	t.cancelConfirmed = true
	m.mu.Unlock()
}

// MarkUpstreamCancelRequested is called at the terminal HTTP transport call,
// after owner lookup, URL validation and authentication succeeded.
func (t *Turn) MarkUpstreamCancelRequested() {
	m := t.manager
	m.mu.Lock()
	t.cancelRequested = true
	m.mu.Unlock()
}

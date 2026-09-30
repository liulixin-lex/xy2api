package service

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/tidwall/gjson"
)

// Cancellation is control evidence, not an inferred provider failure.
type ControlledCancelReason string

const (
	ControlledClientDetached  ControlledCancelReason = "client_detached"
	ControlledUserStop        ControlledCancelReason = "user_stop"
	ControlledStartupTimeout  ControlledCancelReason = "startup_timeout"
	ControlledContentTimeout  ControlledCancelReason = "content_timeout"
	ControlledAdminCancel     ControlledCancelReason = "admin_cancel"
	ControlledLeaseLost       ControlledCancelReason = "lease_lost"
	ControlledUpstreamFailure ControlledCancelReason = "upstream_failure"
	ControlledSlowConsumer    ControlledCancelReason = "slow_consumer"
)

func (r ControlledCancelReason) excludesProviderHealth() bool {
	switch r {
	case ControlledClientDetached, ControlledUserStop, ControlledAdminCancel, ControlledLeaseLost, ControlledSlowConsumer:
		return true
	default:
		return false
	}
}

type ControlledSendCertainty string

const (
	ControlledNotSent             ControlledSendCertainty = "not_sent"
	ControlledSendUnknown         ControlledSendCertainty = "sent_execution_unknown"
	ControlledResponseReceived    ControlledSendCertainty = "response_received"
	ControlledExternallyCommitted ControlledSendCertainty = "externally_committed"
)

// ControlledStreamState keeps HTTP, attempt identity and content independent.
// Local heartbeats can commit HTTP without committing an upstream attempt.
type ControlledStreamState struct {
	HTTPCommitted    bool
	AttemptCommitted bool
	SemanticSeen     bool
	SendCertainty    ControlledSendCertainty
	CancelReason     ControlledCancelReason
}

func ControlledStreamSnapshot(ctx context.Context) ControlledStreamState {
	var state ControlledStreamState
	if ctx == nil {
		return state
	}
	r := controlledRequest(ctx)
	if r == nil {
		return state
	}
	r.mu.Lock()
	state.HTTPCommitted, state.AttemptCommitted, state.SemanticSeen = r.httpCommitted, r.attemptCommitted, r.semanticSeen || !r.semanticAt.IsZero()
	state.CancelReason = r.cancelReason
	d := r.currentDispatch
	r.mu.Unlock()
	if d != nil {
		d.mu.Lock()
		state.SendCertainty, state.CancelReason = d.sendCertainty, d.cancellationReason
		d.mu.Unlock()
	}
	return state
}

// CommitControlledOutput must precede the first Write of an upstream event.
// It does no datastore I/O and never uses semantic classification as a gate.
// Receiving an event alone is deliberately insufficient: errors can be classified
// for a coordinator retry until the adapter elects to deliver them.
func CommitControlledOutput(ctx context.Context, frame []byte) error {
	if ctx == nil || !NativeStreamDeliveryEnabled(ctx) {
		return nil
	}
	if !gjson.ValidBytes(frame) && !bytes.Equal(bytes.TrimSpace(frame), []byte("[DONE]")) {
		return nil
	}
	return CommitControlledIdentity(ctx)
}

// CommitControlledIdentity also covers an upstream response/tool identity sent
// in headers before a protocol body event is available.
func CommitControlledIdentity(ctx context.Context) error {
	if ctx == nil || !NativeStreamDeliveryEnabled(ctx) {
		return nil
	}
	r := controlledRequest(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	d := r.currentDispatch
	r.mu.Unlock()
	if d != nil {
		return d.tryCommitAttempt()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelReason.excludesProviderHealth() {
		return context.Canceled
	}
	r.commitAttemptLocked()
	return nil
}

// TransferControlledExecutionContext gives the turn worker the same request
// policy, deadline and ledger before its first dispatch. It does not bind an
// account: a safe pre-commit retry must remain eligible for normal selection.
// The caller remains responsible for canceling an unproven detached request.
func TransferControlledExecutionContext(requestCtx, executionCtx context.Context) (context.Context, error) {
	if requestCtx == nil || executionCtx == nil {
		return nil, scheduling.ErrInvalidControl
	}
	r := controlledRequest(requestCtx)
	if r == nil {
		return executionCtx, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentDispatch != nil || r.currentAttemptID != "" {
		return nil, scheduling.ErrAttemptIdentity
	}
	if r.cancelReason != "" || (r.clientContext != nil && r.clientContext.Err() != nil) || executionCtx.Err() != nil {
		return nil, context.Canceled
	}
	if !r.ClientDeadline.IsZero() {
		deadline, ok := executionCtx.Deadline()
		if !ok || deadline.After(r.ClientDeadline) {
			executionCtx, r.executionCancel = context.WithDeadline(executionCtx, r.ClientDeadline)
		}
	}
	r.clientContext = executionCtx
	executionCtx = WithNativeStreamPolicy(executionCtx, NativeStreamPolicyFromContext(requestCtx))
	return context.WithValue(executionCtx, controlledSchedulingContextKey{}, r), nil
}

// BindControlledExecutionContext transfers lifetime only after a caller has
// established native recovery eligibility. It reuses the same policy and ledger,
// retains the original deadline, and pins the already selected account.
func BindControlledExecutionContext(requestCtx, executionCtx context.Context, accountID int64) (context.Context, error) {
	if requestCtx == nil || executionCtx == nil || accountID <= 0 {
		return nil, scheduling.ErrInvalidControl
	}
	r := controlledRequest(requestCtx)
	if r == nil {
		return nil, scheduling.ErrInvalidControl
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentDispatch != nil || r.currentAttemptID != "" {
		return nil, scheduling.ErrAttemptIdentity
	}
	if r.cancelReason != "" || (r.clientContext != nil && r.clientContext.Err() != nil) {
		return nil, context.Canceled
	}
	if (r.Decision.AccountID != 0 && r.Decision.AccountID != accountID) || (r.ownerAccountID != 0 && r.ownerAccountID != accountID) {
		return nil, scheduling.ErrControlBlocked
	}
	if !r.ClientDeadline.IsZero() {
		deadline, ok := executionCtx.Deadline()
		if !ok || deadline.After(r.ClientDeadline) {
			executionCtx, r.executionCancel = context.WithDeadline(executionCtx, r.ClientDeadline)
		}
	}
	r.clientContext = executionCtx
	r.owner, r.ownerAccountID = true, accountID
	executionCtx = WithNativeStreamPolicy(executionCtx, NativeStreamPolicyFromContext(requestCtx))
	return context.WithValue(executionCtx, controlledSchedulingContextKey{}, r), nil
}

// RecordNativeStreamFlush receives measured complete-event and successful flush
// timestamps from the relay. It only updates bounded metadata on the hot path.
type nativeStreamFlushObserverKey struct{}

func RecordNativeStreamFlush(ctx context.Context, readAt, flushedAt time.Time) {
	if ctx == nil || !NativeStreamDeliveryEnabled(ctx) || NativeStreamFlushDeferred(ctx) || readAt.IsZero() || flushedAt.Before(readAt) {
		return
	}
	// Optional in-process instrumentation records raw samples in acceptance
	// fixtures. Production contexts never install this callback.
	if observer, ok := ctx.Value(nativeStreamFlushObserverKey{}).(func(time.Time, time.Time)); ok {
		observer(readAt, flushedAt)
	}
	r := controlledRequest(ctx)
	if r == nil {
		return
	}
	ms := float64(flushedAt.Sub(readAt)) / float64(time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstReadToFlushMS == nil {
		r.firstReadToFlushMS = &ms
		r.firstFlushAt = flushedAt
	}
	if ms > r.maxReadToFlushMS {
		r.maxReadToFlushMS = ms
	}
	r.flushCount++
}

func (r *ControlledRequest) commitAttemptLocked() {
	r.attemptCommitted = true
	if r.Ledger != nil {
		r.Ledger.MarkAttemptCommit()
	}
}

// The dispatch mutex orders timeout/control cancellation and delivery. A late
// callback cannot write the old attempt after the coordinator starts a retry.
func (d *controlledDispatch) tryCommitAttempt() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timeout {
		return context.DeadlineExceeded
	}
	if d.adminCancelled.Load() || d.cancellationReason != "" {
		return context.Canceled
	}
	r := d.request
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelReason.excludesProviderHealth() {
		return context.Canceled
	}
	if r.clientContext != nil && r.clientContext.Err() != nil {
		return r.clientContext.Err()
	}
	if r.currentDispatch != nil && r.currentDispatch != d {
		return scheduling.ErrCommitted
	}
	r.commitAttemptLocked()
	if d.attemptCommittedAt.IsZero() {
		d.attemptCommittedAt = time.Now()
	}
	d.sendCertainty = ControlledExternallyCommitted
	return nil
}

func (d *controlledDispatch) TryCommitOutput(frame []byte) error {
	if d == nil {
		return nil
	}
	if !NativeStreamDeliveryEnabled(d.ctx) {
		d.CommitOutput(frame)
		return nil
	}
	if !gjson.ValidBytes(frame) && !bytes.Equal(bytes.TrimSpace(frame), []byte("[DONE]")) {
		return nil
	}
	if err := d.tryCommitAttempt(); err != nil {
		return err
	}
	semantic, answer, _, _ := classifySemanticEvent(frame)
	if semantic && d.request != nil {
		d.request.markSemantic(time.Now(), answer)
	}
	return nil
}

func (d *controlledDispatch) cancelWithReason(reason ControlledCancelReason) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if NativeStreamDeliveryEnabled(d.ctx) && (d.cancellationReason == "" || (reason.excludesProviderHealth() && !d.cancellationReason.excludesProviderHealth())) {
		d.cancellationReason = reason
	}
	if NativeStreamDeliveryEnabled(d.ctx) && reason.excludesProviderHealth() && d.timer != nil {
		d.timer.Stop()
	}
	if NativeStreamDeliveryEnabled(d.ctx) && reason.excludesProviderHealth() && d.request != nil {
		d.request.mu.Lock()
		if d.request.cancelReason == "" {
			d.request.cancelReason = reason
		}
		d.request.mu.Unlock()
	}
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// CancelControlledRequest is an explicit control-plane cancellation. It never
// detaches cancellation propagation or grants recovery eligibility.
func CancelControlledRequest(ctx context.Context, reason ControlledCancelReason) error {
	switch reason {
	case ControlledClientDetached, ControlledUserStop, ControlledAdminCancel, ControlledLeaseLost, ControlledSlowConsumer:
	default:
		return errors.New("invalid controlled cancellation reason")
	}
	if ctx == nil {
		return nil
	}
	r := controlledRequest(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.cancelReason == "" {
		r.cancelReason = reason
	}
	d := r.currentDispatch
	r.mu.Unlock()
	if d != nil {
		d.cancelWithReason(reason)
	}
	return nil
}

// The writer fallback protects protocols whose adapter writes an SSE frame in
// several chunks. Comments and blank lines are local transport activity only.
func controlledSSEHasProtocolBytes(p []byte) bool {
	for _, line := range bytes.Split(p, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		return true
	}
	return false
}

package service

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func nativeCommitFixture(t *testing.T) (context.Context, *ControlledRequest, *controlledDispatch) {
	t.Helper()
	ctx := NewControlledRequestContext(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 1, Delivery: true}), "responses")
	r := controlledRequest(ctx)
	r.Policy = scheduling.Policy{Enabled: true, AccountPool: true, Retry: scheduling.RetryPolicy{MaxAttempts: 3, MaxPerAccount: 1, MaxPerTier: 3, CrossTier: true}}
	r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, scheduling.LatencyProfile{}, time.Now(), time.Time{})
	require.NoError(t, r.Ledger.BeginAttempt(1, 0, time.Now(), true))
	live, cancel := context.WithCancel(ctx)
	d := &controlledDispatch{request: r, ctx: live, cancel: cancel, done: make(chan struct{}), semanticReady: make(chan struct{}), semanticObservable: true, sent: true, started: time.Now(), sendCertainty: ControlledSendUnknown, ticket: scheduling.DispatchTicket{AccountID: 1, TicketID: "fixture", Failure: &scheduling.FailureAdmission{AccountID: 1, Model: "fixture", HealthIdentity: "fixture"}}}
	r.currentDispatch = d
	t.Cleanup(cancel)
	return ctx, r, d
}

func TestNativeStreamObserveDoesNotCommitOrFabricateContent(t *testing.T) {
	ctx, r, d := nativeCommitFixture(t)
	frames := [][]byte{[]byte("{\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}"), []byte("{\"type\":\"future.unknown\",\"sequence_number\":1}")}
	for _, frame := range frames {
		d.ObserveFrame(frame)
	}
	require.False(t, d.firstEvent.IsZero())
	require.True(t, d.semantic.IsZero())
	require.False(t, r.Ledger.Snapshot().Committed)
	require.NoError(t, CommitControlledOutput(ctx, frames[1]))
	state := ControlledStreamSnapshot(ctx)
	require.True(t, state.AttemptCommitted)
	require.False(t, state.HTTPCommitted)
	require.False(t, state.SemanticSeen)
	require.Equal(t, ControlledExternallyCommitted, state.SendCertainty)
	require.ErrorIs(t, r.Ledger.CanAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
}

func TestNativeStreamAnswerTimingExcludesSummaryPart(t *testing.T) {
	_, _, d := nativeCommitFixture(t)
	d.ObserveFrame([]byte(`{"type":"response.content_part.done","part":{"type":"summary_text","text":"thinking"}}`))
	require.True(t, d.answer.IsZero())
	d.ObserveFrame([]byte(`{"type":"response.content_part.done","part":{"type":"output_text","text":"answer"}}`))
	require.False(t, d.answer.IsZero())
	require.False(t, d.semantic.IsZero())
}

func TestNativeStreamRecoveryCommitsCreatedBeforeAnswer(t *testing.T) {
	ctx, r, d := nativeCommitFixture(t)

	created := []byte(`{"type":"response.created","response":{"id":"resp_first"}}`)
	reasoning := []byte(`{"type":"response.reasoning_text.delta","delta":"thinking"}`)
	d.ObserveFrame(created)
	require.False(t, r.Ledger.Snapshot().Committed)
	require.NoError(t, CommitControlledOutput(ctx, created))
	require.True(t, r.Ledger.Snapshot().Committed)
	require.ErrorIs(t, r.Ledger.CanAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
	d.ObserveFrame(reasoning)

	answer := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	d.ObserveFrame(answer)
	require.False(t, d.answer.IsZero())
}

func TestNativeStreamHeartbeatCommitsOnlyHTTP(t *testing.T) {
	ctx, r, _ := nativeCommitFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
	w.Header().Set("Content-Type", "text/event-stream")
	_, err := w.Write([]byte(": keepalive\n\n"))
	require.NoError(t, err)
	w.Flush()
	state := ControlledStreamSnapshot(ctx)
	require.True(t, state.HTTPCommitted)
	require.False(t, state.AttemptCommitted)
	require.False(t, state.SemanticSeen)
	require.NoError(t, CommitControlledOutput(ctx, []byte("{\"type\":\"response.created\"}")))
	require.True(t, ControlledStreamSnapshot(ctx).AttemptCommitted)
	require.False(t, ControlledStreamSnapshot(ctx).SemanticSeen)
}

func TestNativeStreamTimeoutCommitRaceCannotAdmitMixedAttempt(t *testing.T) {
	for i := 0; i < 250; i++ {
		_, r, d := nativeCommitFixture(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var commitErr error
		go func() {
			defer wg.Done()
			<-start
			commitErr = d.TryCommitOutput([]byte("{\"type\":\"response.created\"}"))
		}()
		go func() {
			defer wg.Done()
			<-start
			d.mu.Lock()
			d.timeout = true
			d.cancellationReason = ControlledStartupTimeout
			d.cancel()
			d.mu.Unlock()
		}()
		close(start)
		wg.Wait()
		retryErr := r.Ledger.BeginAttempt(2, 0, time.Now(), true)
		if commitErr == nil && retryErr == nil {
			t.Fatal("old first event and replacement attempt both admitted")
		}
		if commitErr != nil {
			require.ErrorIs(t, commitErr, context.DeadlineExceeded)
		} else {
			require.ErrorIs(t, retryErr, scheduling.ErrCommitted)
		}
	}
}

func TestNativeStreamCancellationReasonsExcludeProviderPenalty(t *testing.T) {
	for _, reason := range []ControlledCancelReason{ControlledClientDetached, ControlledUserStop, ControlledAdminCancel, ControlledLeaseLost, ControlledSlowConsumer} {
		t.Run(string(reason), func(t *testing.T) {
			ctx, r, d := nativeCommitFixture(t)
			require.NoError(t, CancelControlledRequest(ctx, reason))
			require.ErrorIs(t, d.ctx.Err(), context.Canceled)
			require.ErrorIs(t, d.TryCommitOutput([]byte("{\"type\":\"response.created\"}")), context.Canceled)
			decision := d.classifyFailureDomains(string(reason), io.ErrClosedPipe)
			require.Equal(t, "none", decision.Effect)
			require.Equal(t, "stop", decision.Retry)
			require.Equal(t, reason, ControlledStreamSnapshot(ctx).CancelReason)
			_, err := (&ControlledSchedulingService{}).beginDispatch(ctx, 1, 1)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 1, r.Ledger.Snapshot().Attempts)
		})
	}
}

func TestNativeStreamCommittedCreatedStopsStartupDeadline(t *testing.T) {
	ctx, r, d := nativeCommitFixture(t)
	r.Profile = scheduling.LatencyProfile{AttemptTimeoutMS: 30, TotalBudgetMS: 1000}
	r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, time.Now(), time.Time{})
	require.NoError(t, r.Ledger.BeginAttempt(1, 0, time.Now(), true))
	d.mu.Lock()
	d.startFirstOutputTimerLocked()
	d.mu.Unlock()
	created := []byte("{\"type\":\"response.created\"}")
	d.ObserveFrame(created)
	require.NoError(t, CommitControlledOutput(ctx, created))
	select {
	case <-d.ctx.Done():
		t.Fatal("committed first event was canceled by the startup deadline")
	case <-time.After(100 * time.Millisecond):
	}
	require.Empty(t, ControlledStreamSnapshot(ctx).CancelReason)
	require.False(t, ControlledStreamSnapshot(ctx).SemanticSeen)
	require.ErrorIs(t, r.Ledger.CanAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
}

type nativeFinalRead struct{ data []byte }

func (r *nativeFinalRead) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = nil
	return n, io.EOF
}
func (r *nativeFinalRead) Close() error { return nil }

func TestNativeStreamFinalBytesReturnBeforeSettlement(t *testing.T) {
	_, _, d := nativeCommitFixture(t)
	payload := []byte("data: {\"type\":\"response.completed\"}\n\n")
	b := &controlledResponseBody{ReadCloser: &nativeFinalRead{data: payload}, dispatch: d, sse: true, success: true}
	d.parser.onFrame = d.ObserveFrame
	out := make([]byte, 1024)
	n, err := b.Read(out)
	require.NoError(t, err)
	require.Equal(t, payload, out[:n])
	require.ErrorIs(t, b.pendingReadErr, io.EOF)
	select {
	case <-d.done:
		t.Fatal("settlement ran before final event delivery")
	default:
	}
}

func TestNativeStreamCommittedTransportFailureCannotFailover(t *testing.T) {
	ctx, _, _ := nativeCommitFixture(t)
	require.NoError(t, CommitControlledOutput(ctx, []byte("{\"type\":\"response.created\"}")))
	err := controlledSchedulingTransportFailure(ctx, io.ErrUnexpectedEOF)
	require.ErrorIs(t, err, scheduling.ErrCommitted)
	var retry *UpstreamFailoverError
	require.False(t, errors.As(err, &retry))
}

func TestNativeStreamIdentityHeaderCommitsBeforeHeartbeatFlush(t *testing.T) {
	ctx, r, _ := nativeCommitFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Openai-Response-Id", "resp_1")
	w.Flush()
	state := ControlledStreamSnapshot(ctx)
	require.True(t, state.AttemptCommitted)
	require.True(t, state.HTTPCommitted)
	require.False(t, state.SemanticSeen)
	require.ErrorIs(t, r.Ledger.BeginAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
}

func TestNativeStreamExplicitCancelOutranksContentTimer(t *testing.T) {
	ctx, r, d := nativeCommitFixture(t)
	r.Profile = scheduling.LatencyProfile{AttemptTimeoutMS: 10, TotalBudgetMS: 1000}
	r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, time.Now(), time.Time{})
	d.mu.Lock()
	d.startFirstOutputTimerLocked()
	d.mu.Unlock()
	require.NoError(t, CancelControlledRequest(ctx, ControlledUserStop))
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, ControlledUserStop, ControlledStreamSnapshot(ctx).CancelReason)
	d.mu.Lock()
	timeout := d.timeout
	d.mu.Unlock()
	require.False(t, timeout)
}

func TestNativeStreamExecutionBindingPreservesLedgerDeadlineAndOwner(t *testing.T) {
	original, cancelOriginal := context.WithTimeout(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 1, Delivery: true}), time.Minute)
	defer cancelOriginal()
	ctx := NewControlledRequestContext(original, "responses")
	r := controlledRequest(ctx)
	r.Ledger = scheduling.NewAttemptLedger(scheduling.RetryPolicy{}, scheduling.LatencyProfile{}, time.Now(), r.ClientDeadline)
	r.Decision.AccountID = 17
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	bound, err := BindControlledExecutionContext(ctx, live, 17)
	require.NoError(t, err)
	require.Same(t, r, controlledRequest(bound))
	require.True(t, r.owner)
	require.EqualValues(t, 17, r.ownerAccountID)
	deadline, ok := bound.Deadline()
	require.True(t, ok)
	require.Equal(t, r.ClientDeadline, deadline)
	cancelOriginal()
	require.NoError(t, bound.Err(), "eligible detach uses the original execution context")
	cancelLive()
	require.ErrorIs(t, bound.Err(), context.Canceled, "execution control cancellation must remain live")
	r.Close()
}

func TestNativeStreamExecutionTransferKeepsRetrySelectionAndControl(t *testing.T) {
	original, cancelOriginal := context.WithTimeout(WithNativeStreamPolicy(context.Background(), NativeStreamPolicy{Version: 9, Delivery: true, Recovery: true}), time.Minute)
	defer cancelOriginal()
	ctx := NewControlledRequestContext(original, "responses")
	r := controlledRequest(ctx)
	r.Decision.AccountID = 17
	r.Ledger = scheduling.NewAttemptLedger(scheduling.RetryPolicy{MaxAttempts: 3, MaxPerAccount: 1, MaxPerTier: 3}, scheduling.LatencyProfile{}, time.Now(), r.ClientDeadline)
	ledger := r.Ledger
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	bound, err := TransferControlledExecutionContext(ctx, live)
	require.NoError(t, err)
	require.Same(t, r, controlledRequest(bound))
	require.Same(t, ledger, controlledRequest(bound).Ledger)
	require.False(t, r.owner)
	require.Zero(t, r.ownerAccountID)
	require.Equal(t, NativeStreamPolicyFromContext(ctx), NativeStreamPolicyFromContext(bound))
	deadline, ok := bound.Deadline()
	require.True(t, ok)
	require.Equal(t, r.ClientDeadline, deadline)
	require.NoError(t, r.Ledger.BeginAttempt(17, 0, time.Now(), true))
	require.NoError(t, r.Ledger.BeginAttempt(18, 0, time.Now(), true), "an uncommitted first candidate must not become a protocol owner")
	cancelOriginal()
	require.NoError(t, bound.Err())
	cancelLive()
	require.ErrorIs(t, bound.Err(), context.Canceled)
	r.Close()
}

func TestNativeStreamExecutionTransferRejectsLateOrCanceled(t *testing.T) {
	ctx, _, _ := nativeCommitFixture(t)
	_, err := TransferControlledExecutionContext(ctx, context.Background())
	require.ErrorIs(t, err, scheduling.ErrAttemptIdentity)
	ctx = NewControlledRequestContext(context.Background(), "responses")
	require.NoError(t, CancelControlledRequest(ctx, ControlledUserStop))
	_, err = TransferControlledExecutionContext(ctx, context.Background())
	require.ErrorIs(t, err, context.Canceled)
	live, cancel := context.WithCancel(context.Background())
	defer cancel()
	bound, err := TransferControlledExecutionContext(context.Background(), live)
	require.NoError(t, err)
	require.Equal(t, live, bound)
}

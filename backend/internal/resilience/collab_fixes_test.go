package resilience

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type collabPanicConsumer struct{ NoopCollabDeliveryConsumer }

func (collabPanicConsumer) Received(context.Context, CollabDeliveryEvent)     { panic("synthetic") }
func (collabPanicConsumer) AboutToWrite(context.Context, CollabDeliveryEvent) { panic("synthetic") }
func (collabPanicConsumer) Written(context.Context, CollabDeliveryEvent)      { panic("synthetic") }
func (collabPanicConsumer) Completed(context.Context, CollabDeliveryEvent)    { panic("synthetic") }

type collabPanicObserver struct{}

func (collabPanicObserver) SealSend(CollabSendObservation) bool       { panic("synthetic") }
func (collabPanicObserver) ObserveDelivery(CollabDeliveryObservation) { panic("synthetic") }
func (collabPanicObserver) Freeze() *CollabObservation                { panic("synthetic") }

func TestCollabSafetyContainsConsumerAndObserverPanics(t *testing.T) {
	before := CollabSafetySnapshot()
	hooks := &CollabDeliveryHooks{}
	next := &collabTestConsumer{}
	hooks.Register(collabPanicConsumer{})
	hooks.Register(next)
	ctx := context.Background()
	event := CollabDeliveryEvent{Generation: 1}
	require.NotPanics(t, func() {
		hooks.Received(ctx, event, nil)
		hooks.AboutToWrite(ctx, event, nil)
		hooks.Written(ctx, event, nil)
		hooks.Completed(ctx, event, nil)
	})
	require.Len(t, next.events, 4)
	observer := SafeCollabObserver{Observer: collabPanicObserver{}}
	require.False(t, observer.SealSend(CollabSendObservation{}))
	require.NotPanics(t, func() { observer.ObserveDelivery(CollabDeliveryObservation{}) })
	require.Nil(t, observer.Freeze())
	state := NewRequestState()
	request := &CollabRequestState{Observer: collabPanicObserver{}}
	require.True(t, state.SetCollabState(request))
	require.Nil(t, state.CollabState().Observer.Freeze())
	after := CollabSafetySnapshot()
	require.Equal(t, before.ConsumerPanics+4, after.ConsumerPanics)
	require.Equal(t, before.ObserverPanics+4, after.ObserverPanics)
}

func TestCollabOrdersAndOriginalFailure(t *testing.T) {
	s := Defaults()
	s.Collab.Ladder = true
	require.Equal(t, CollabOrderLegacyFirst, s.CollabOrder())
	s.Collab.LosslessFirst = true
	require.Equal(t, CollabOrderLosslessFirst, s.CollabOrder())
	s.Collab.Ladder = false
	require.Equal(t, CollabOrderLegacyFirst, s.CollabOrder())
	failure := errors.New("synthetic saved failure")
	endpoint := CollabLegacyEndpoint{Saved: NewCollabSavedTerminal(CollabTerminalSnapshot{Failure: failure})}
	state := CollabRequestState{Order: CollabOrderLegacyFirst, LegacyEndpoint: endpoint}
	require.Same(t, failure, state.LegacyEndpoint.Saved.Snapshot.Failure)
	controller := NoopCollabController{}
	require.False(t, controller.TakeOver(endpoint))
	require.Equal(t, CollabNextKindLegacy, controller.NextStep().Kind)
	require.Same(t, failure, endpoint.Saved.Snapshot.Failure)
}

func TestCollabOptionalDeadlineAndRevocableHold(t *testing.T) {
	require.Equal(t, time.Minute, CollabOptionalPolicy(CollabOptionalBudget{}).TotalOptionalWait, "no configured deadline still permits bounded optional work")
	for _, remaining := range []time.Duration{0, -time.Second} {
		require.False(t, CollabOptionalPolicy(CollabOptionalBudget{HasDeadline: true, Remaining: remaining}).Allowed)
	}
	require.Equal(t, time.Minute, CollabOptionalPolicy(CollabOptionalBudget{HasDeadline: true, Remaining: 20 * time.Minute}).TotalOptionalWait)
	base := CollabOptionalBudget{Order: CollabOrderLosslessFirst, CompensationAvailable: true, FirstAccountSpareSlots: true, HasClientIdleDeadline: true, ClientIdleRemaining: 2 * time.Minute, LegacyTimeToRestore: time.Minute}
	policy := CollabOptionalPolicy(base)
	require.True(t, policy.Allowed)
	require.True(t, policy.HoldFirstAccount)
	require.True(t, policy.RevocableHold)
	require.Equal(t, 12*time.Second, policy.LegacyCompensation(12*time.Second))
	require.Equal(t, time.Minute, policy.LegacyCompensation(2*time.Minute))
	for _, change := range []func(*CollabOptionalBudget){
		func(b *CollabOptionalBudget) { b.CompensationAvailable = false },
		func(b *CollabOptionalBudget) { b.FirstAccountSpareSlots = false },
		func(b *CollabOptionalBudget) { b.FirstAccountHasWaiters = true },
		func(b *CollabOptionalBudget) { b.HoldRevoked = true },
		func(b *CollabOptionalBudget) { b.ClientIdleRemaining = time.Minute },
	} {
		modified := base
		change(&modified)
		p := CollabOptionalPolicy(modified)
		require.False(t, p.Allowed)
		require.False(t, p.HoldFirstAccount)
	}
	legacy := base
	legacy.Order = CollabOrderLegacyFirst
	legacy.FirstAccountHasWaiters = true
	require.True(t, CollabOptionalPolicy(legacy).Allowed)
	require.False(t, CollabOptionalPolicy(legacy).HoldFirstAccount)
	require.Zero(t, CollabOptionalPolicy(legacy).LegacyCompensation(time.Second))
}

var collabBenchmarkState *RequestState
var collabBenchmarkSettings *Settings

func BenchmarkCollabIngressSnapshot(b *testing.B) {
	b.Run("baseline", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			collabBenchmarkState = &RequestState{requestID: uuid.NewString()}
		}
	})
	b.Run("snapshot", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			collabBenchmarkState = NewRequestState()
			collabBenchmarkSettings = collabBenchmarkState.CollabSettingsSnapshot()
		}
	})
}
func TestCollabSnapshotAddsNoAllocations(t *testing.T) {
	baseline := testing.AllocsPerRun(100, func() { collabBenchmarkState = &RequestState{requestID: uuid.NewString()} })
	snapshot := testing.AllocsPerRun(100, func() {
		collabBenchmarkState = NewRequestState()
		collabBenchmarkSettings = collabBenchmarkState.CollabSettingsSnapshot()
	})
	require.Equal(t, baseline, snapshot)
	require.Same(t, Current(), collabBenchmarkSettings)
}

type collabPayloadConsumer struct {
	NoopCollabDeliveryConsumer
	payload string
}

func (c *collabPayloadConsumer) Received(_ context.Context, event CollabDeliveryEvent) {
	c.payload = event.Payload
}
func TestCollabHooksBoundPayloadAllocations(t *testing.T) {
	ctx := context.Background()
	payload := make([]byte, 1<<20)
	empty := &CollabDeliveryHooks{}
	event := CollabDeliveryEvent{EventType: "response.output_item.done", Executable: true}
	require.Zero(t, testing.AllocsPerRun(100, func() { empty.Received(ctx, event, payload) }))
	h := &CollabDeliveryHooks{}
	first, second := &collabPayloadConsumer{}, &collabPayloadConsumer{}
	h.Register(first)
	h.Register(second)
	event.EventType = "response.output_text.delta"
	require.Zero(t, testing.AllocsPerRun(100, func() { h.Received(ctx, event, payload) }))
	require.Empty(t, first.payload)
	event.EventType = "response.output_item.done"
	event.Executable = false
	require.Zero(t, testing.AllocsPerRun(100, func() { h.Received(ctx, event, payload) }), "image frame")
	event.Executable = true
	require.LessOrEqual(t, testing.AllocsPerRun(10, func() { h.Received(ctx, event, payload) }), float64(1))
	require.Len(t, first.payload, len(payload))
	require.Equal(t, first.payload, second.payload)
	payload[0] = 1
	require.Zero(t, first.payload[0])
}

func TestCollabRequestRetainsOnlyOriginalAndCurrentBody(t *testing.T) {
	stateType := reflect.TypeFor[CollabRequestState]()
	byteSlice := reflect.TypeFor[[]byte]()
	var bodies []string
	for i := 0; i < stateType.NumField(); i++ {
		field := stateType.Field(i)
		if field.Type == byteSlice {
			bodies = append(bodies, field.Name)
		}
		require.NotEqual(t, reflect.TypeFor[map[int64][]byte](), field.Type)
	}
	require.ElementsMatch(t, []string{"Original", "CurrentBody"}, bodies)
	sendType := reflect.TypeFor[CollabSendState]()
	for i := 0; i < sendType.NumField(); i++ {
		require.NotEqual(t, byteSlice, sendType.Field(i).Type)
	}
}

func TestCollabSelectionPrecedesAccountPlanning(t *testing.T) {
	next := CollabNextStep{Kind: CollabNextKindSelectAccount, Selection: &CollabAccountSelection{Preferences: []CollabAccountPreference{{AccountID: 2}}, ExcludedAccountIDs: []int64{1}, ImmediateOnly: true}}
	require.Nil(t, next.Candidate)
	require.True(t, next.Selection.ImmediateOnly)
	original := []byte("original")
	candidate := &collabPlanningCandidate{}
	candidate.Plan(CollabCandidateInput{Original: original, AccountID: 2, Records: CollabRecordBatchResult{TreeLocalEndings: 1}})
	require.Equal(t, int64(2), candidate.input.AccountID)
	require.Equal(t, original, candidate.input.Original)
	require.Equal(t, 1, candidate.input.Records.TreeLocalEndings)
}

type collabPlanningCandidate struct {
	NoopCollabCandidate
	input CollabCandidateInput
}

func (c *collabPlanningCandidate) Plan(in CollabCandidateInput) (CollabCandidatePlan, bool) {
	c.input = in
	return CollabCandidatePlan{AccountID: in.AccountID}, true
}

func TestCollabThreadSchedulingAndOptionalEffects(t *testing.T) {
	failure := CollabThreadPolicy(CollabRoleChild, CollabAttempt{}, true, false)
	require.True(t, failure.ThreadScoped)
	require.True(t, failure.AvoidNow, "avoid at rejection before a final outcome exists")
	require.False(t, failure.BindRecovered)
	recovery := CollabThreadPolicy(CollabRoleChild, CollabAttempt{}, true, true)
	require.True(t, recovery.ThreadScoped)
	require.True(t, recovery.BindRecovered)
	require.False(t, recovery.AvoidNow)
	require.Empty(t, CollabThreadPolicy(CollabRoleChild, CollabAttempt{Optional: true}, true, false))
	require.Empty(t, CollabThreadPolicy(CollabRoleRoot, CollabAttempt{}, true, false))
	hint := CollabTreeHint{AccountID: 2}
	require.False(t, CollabTreeHintUsable(hint, false))
	require.True(t, CollabTreeHintUsable(hint, true))
	scheduler := NoopCollabScheduler{}
	require.False(t, scheduler.BindRecoveredThread(context.Background(), CollabThreadRoute{}))
	_, ok := scheduler.LookupThreadRoute(context.Background(), CollabRecordScope{}, "")
	require.False(t, ok)
	for _, abandoned := range []bool{false, true} {
		ctx := WithCollabAttempt(context.Background(), CollabAttempt{Seq: 1, Optional: true, Abandoned: abandoned})
		attempt := CollabAttemptFrom(ctx)
		require.False(t, attempt.AccountEffectsAllowed())
		require.False(t, attempt.CustomerSettlementAllowed())
	}
	normal := CollabAttemptFrom(context.Background())
	require.True(t, normal.AccountEffectsAllowed())
	require.True(t, normal.CustomerSettlementAllowed())
}

func TestCollabLearningAndNamedResolution(t *testing.T) {
	legacy := CollabLearning{Timing: CollabLearningConfirmedRejection}
	replacement := CollabLearning{Timing: CollabLearningReplacementAccepted}
	require.True(t, legacy.CommitReady(false, false), "existing learning remains before resend")
	require.False(t, legacy.CommitReady(false, true), "optional probes cannot commit legacy learning")
	require.False(t, replacement.CommitReady(false, false), "replacement must wait for acceptance")
	require.True(t, replacement.CommitReady(true, false))
	require.False(t, (CollabLearning{}).CommitReady(true, false))
	named := CollabNamedItem{Identity: "synthetic-item", Located: true, Item: "stale"}
	require.False(t, ResolveCollabNamedItem(named, nil).Located)
	original := []CollabOriginalItem{{Identity: "synthetic-item", Item: "digest", Family: CollabFamilyReasoning}}
	resolved := ResolveCollabNamedItem(named, original)
	require.True(t, resolved.Located)
	require.Equal(t, 1, resolved.MatchCount)
	require.Equal(t, CollabDigest("digest"), resolved.Item)
	require.Equal(t, CollabFamilyReasoning, resolved.Family)
	original = append(original, original[0])
	resolved = ResolveCollabNamedItem(named, original)
	require.False(t, resolved.Located)
	require.True(t, resolved.Ambiguous)
	require.Equal(t, 2, resolved.MatchCount)
	require.Empty(t, resolved.Item)
}

type collabSharedTestStore struct {
	reads  atomic.Int64
	lookup func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error)
	apply  func(context.Context, CollabRecordMutation) error
}

func (s *collabSharedTestStore) LookupBatch(ctx context.Context, b CollabRecordBatch) (CollabRecordBatchResult, error) {
	s.reads.Add(1)
	if s.lookup != nil {
		return s.lookup(ctx, b)
	}
	return CollabRecordBatchResult{}, nil
}
func (s *collabSharedTestStore) Apply(ctx context.Context, m CollabRecordMutation) error {
	if s.apply != nil {
		return s.apply(ctx, m)
	}
	return nil
}

type collabLocalTestStore struct {
	NoopCollabRecordsStore
	writes atomic.Int64
}

func (s *collabLocalTestStore) PendingIssued(context.Context, CollabIssuedRecord) (CollabIssuedToken, bool) {
	s.writes.Add(1)
	return CollabIssuedToken{Token: "synthetic"}, true
}

func TestCollabRecordsBatchDeadlineAndNonblockingWrites(t *testing.T) {
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	local := &collabLocalTestStore{}
	shared := &collabSharedTestStore{apply: func(ctx context.Context, _ CollabRecordMutation) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-unblock:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	store := NewSafeCollabRecordsStore(local, shared, 1)
	t.Cleanup(store.Close)
	ctx := context.Background()
	_, ok := store.PendingIssued(ctx, CollabIssuedRecord{})
	require.True(t, ok)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	before := CollabSafetySnapshot().RecordDropped
	for i := 0; i < 10; i++ {
		_, ok = store.PendingIssued(ctx, CollabIssuedRecord{})
		require.True(t, ok)
	}
	require.Equal(t, int64(11), local.writes.Load())
	require.Greater(t, CollabSafetySnapshot().RecordDropped, before)
	close(unblock)

	shared.lookup = func(ctx context.Context, b CollabRecordBatch) (CollabRecordBatchResult, error) {
		require.Len(t, b.Queries, 64)
		return CollabRecordBatchResult{TreeLocalEndings: 2}, nil
	}
	readCtx, cancel := WithCollabRecordReadBudget(ctx)
	defer cancel()
	batch := CollabRecordBatch{Queries: make([]CollabRecordQuery, 64), Records: true, Dispositions: true}
	require.Equal(t, 2, store.LookupBatch(readCtx, batch).TreeLocalEndings)
	again, againCancel := WithCollabRecordReadBudget(readCtx)
	defer againCancel()
	store.LookupBatch(again, batch)
	require.Equal(t, int64(1), shared.reads.Load(), "one shared batch per request")

	blocked := &collabSharedTestStore{lookup: func(ctx context.Context, _ CollabRecordBatch) (CollabRecordBatchResult, error) {
		<-ctx.Done()
		return CollabRecordBatchResult{}, ctx.Err()
	}}
	slow := NewSafeCollabRecordsStore(nil, blocked, 1)
	defer slow.Close()
	readCtx, cancel = WithCollabRecordReadBudget(ctx)
	defer cancel()
	start := time.Now()
	require.Empty(t, slow.LookupBatch(readCtx, batch))
	require.Less(t, time.Since(start), time.Second)
	require.Empty(t, slow.LookupBatch(readCtx, batch))
	require.Equal(t, int64(1), blocked.reads.Load())
}

type collabPanicLocal struct{ NoopCollabRecordsStore }

func (collabPanicLocal) TerminalRejected(context.Context, CollabTerminalRejectionRecord) (int, bool) {
	panic("synthetic")
}
func (collabPanicLocal) LookupBatch(context.Context, CollabRecordBatch) CollabRecordBatchResult {
	panic("synthetic")
}
func (collabPanicLocal) PendingIssued(context.Context, CollabIssuedRecord) (CollabIssuedToken, bool) {
	panic("synthetic")
}
func (collabPanicLocal) ConfirmIssued(context.Context, CollabIssuedToken) bool { panic("synthetic") }
func (collabPanicLocal) VoidIssued(context.Context, CollabIssuedToken)         { panic("synthetic") }
func (collabPanicLocal) PlaintextDelivered(context.Context, CollabPlaintextRecord) bool {
	panic("synthetic")
}
func (collabPanicLocal) Consumed(context.Context, CollabConsumedRecord) bool     { panic("synthetic") }
func (collabPanicLocal) Replaced(context.Context, CollabReplacedRecord) bool     { panic("synthetic") }
func (collabPanicLocal) Terminated(context.Context, CollabTerminatedRecord) bool { panic("synthetic") }
func (collabPanicLocal) IncrementTreeLocalEnding(context.Context, CollabRecordScope, CollabDigest) (int, bool) {
	panic("synthetic")
}
func TestCollabRecordsPanicsAndErrorsAreAbsence(t *testing.T) {
	before := CollabSafetySnapshot()
	store := NewSafeCollabRecordsStore(collabPanicLocal{}, nil, 1)
	defer store.Close()
	ctx := context.Background()
	require.Empty(t, store.LookupBatch(ctx, CollabRecordBatch{}))
	_, ok := store.PendingIssued(ctx, CollabIssuedRecord{})
	require.False(t, ok)
	require.False(t, store.ConfirmIssued(ctx, CollabIssuedToken{}))
	require.NotPanics(t, func() { store.VoidIssued(ctx, CollabIssuedToken{}) })
	require.False(t, store.PlaintextDelivered(ctx, CollabPlaintextRecord{}))
	require.False(t, store.Consumed(ctx, CollabConsumedRecord{}))
	require.False(t, store.Replaced(ctx, CollabReplacedRecord{}))
	require.False(t, store.Terminated(ctx, CollabTerminatedRecord{}))
	_, ok = store.IncrementTreeLocalEnding(ctx, CollabRecordScope{}, "")
	require.False(t, ok)
	_, ok = store.TerminalRejected(ctx, CollabTerminalRejectionRecord{})
	require.False(t, ok)
	require.Equal(t, before.RecordPanics+10, CollabSafetySnapshot().RecordPanics)
	for _, lookup := range []func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error){
		func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error) { panic("synthetic") },
		func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error) {
			return CollabRecordBatchResult{TreeLocalEndings: 9}, errors.New("synthetic")
		},
	} {
		shared := NewSafeCollabRecordsStore(nil, &collabSharedTestStore{lookup: lookup}, 1)
		readCtx, cancel := WithCollabRecordReadBudget(ctx)
		result := shared.LookupBatch(readCtx, CollabRecordBatch{})
		require.Empty(t, result)
		cancel()
		shared.Close()
	}
}

package resilience

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabNoopContracts(t *testing.T) {
	ctx := context.Background()
	var restorer CollabResponseRestorer = NoopCollabResponseRestorer{}
	body := []byte("synthetic")
	stream, err := restorer.SSEData("response.completed", body)
	require.NoError(t, err)
	require.Equal(t, body, stream)
	response, err := restorer.ResponseJSON(body)
	require.NoError(t, err)
	require.Equal(t, body, response)
	require.Equal(t, body, restorer.ErrorBody(body))
	var classifier CollabEvidenceClassifier = NoopCollabEvidenceClassifier{}
	evidence := classifier.Classify(CollabEvidenceInput{Payload: []byte("synthetic"), HTTPStatus: 400, WrappedStatus: 400, Source: CollabEvidenceSourceHttpError, Shape: CollabRequestShape{AgentCiphertext: true}})
	require.Equal(t, CollabEvidenceGradeUnclaimed, evidence.Grade)
	require.Nil(t, evidence.Inner)
	var records CollabRecordsStore = NoopCollabRecordsStore{}
	_, recorded := records.TerminalRejected(ctx, CollabTerminalRejectionRecord{})
	require.False(t, recorded)
	require.Equal(t, CollabRecordBatchResult{}, records.LookupBatch(ctx, CollabRecordBatch{}))
	token, ok := records.PendingIssued(ctx, CollabIssuedRecord{})
	require.False(t, ok)
	require.False(t, records.ConfirmIssued(ctx, token))
	records.VoidIssued(ctx, token)
	require.False(t, records.PlaintextDelivered(ctx, CollabPlaintextRecord{}))
	require.False(t, records.Consumed(ctx, CollabConsumedRecord{}))
	require.False(t, records.Replaced(ctx, CollabReplacedRecord{}))
	require.False(t, records.Terminated(ctx, CollabTerminatedRecord{}))
	require.Zero(t, records.LookupBatch(ctx, CollabRecordBatch{}).TreeLocalEndings)
	count, ok := records.IncrementTreeLocalEnding(ctx, CollabRecordScope{}, "")
	require.Equal(t, CollabTreeLocalEndingLimit, count)
	require.False(t, ok)
	var scheduler CollabScheduler = NoopCollabScheduler{}
	candidates := []int64{3, 1, 2}
	require.Equal(t, candidates, scheduler.OrderAccounts(ctx, CollabSchedulingRequest{Preferences: []CollabAccountPreference{{AccountID: 2}}}, candidates))
	require.Nil(t, scheduler.OrderAccounts(ctx, CollabSchedulingRequest{}, nil))
	require.Equal(t, CollabBindingPolicy{}, scheduler.BindingPolicy(ctx, CollabSchedulingRequest{}))
	require.False(t, scheduler.ProvisionalTreeHint(ctx, CollabTreeHint{}))
	require.False(t, scheduler.ConfirmTreeHint(ctx, CollabTreeHint{}))
	_, ok = scheduler.LookupTreeHint(ctx, CollabRecordScope{})
	require.False(t, ok)
	require.False(t, scheduler.RefreshTreeMembership(ctx, CollabTreeHint{}))
	_, ok = scheduler.LookupThread(ctx, CollabRecordScope{}, "")
	require.False(t, ok)
	require.False(t, scheduler.AcquireDeliveryLease(ctx, CollabDeliveryLease{}))
	scheduler.ReleaseDeliveryLease(ctx, CollabDeliveryKey{}, CollabLeaseReleaseConsumed)
	require.False(t, scheduler.HasDeliveryLeases(ctx, CollabRecordScope{}))
	require.False(t, scheduler.AvoidThread(ctx, CollabThreadAvoidance{}))
	require.False(t, scheduler.ThreadAvoided(ctx, CollabThreadAvoidance{}))
	var observer CollabObserver = NoopCollabObserver{}
	require.False(t, observer.SealSend(CollabSendObservation{}))
	observer.ObserveDelivery(CollabDeliveryObservation{})
	require.Nil(t, observer.Freeze())
}

func TestCollabOptionalPolicyProtectsFloor(t *testing.T) {
	require.Equal(t, 3, CollabOptionalSendLimit)
	for _, remaining := range []time.Duration{-time.Second, 0, 10 * time.Second, 300 * time.Second} {
		policy := CollabOptionalPolicy(CollabOptionalBudget{HasDeadline: true, Remaining: remaining, ClientIdleRemaining: time.Minute, FirstAccountSpareSlots: true})
		require.Equal(t, remaining > 0, policy.HoldFirstAccount)
		require.True(t, policy.RevocableHold)
		require.True(t, policy.ImmediateTargetOnly)
		require.True(t, policy.FallbackOnAnyFailure)
		require.Equal(t, min(time.Minute, max(time.Duration(0), remaining)), policy.TotalOptionalWait)
		require.LessOrEqual(t, policy.FirstEventTimeout, 60*time.Second)
		require.LessOrEqual(t, policy.FirstEventTimeout, policy.TotalOptionalWait)
	}
}

type collabTestConsumer struct {
	NoopCollabDeliveryConsumer
	events []CollabDeliveryEvent
	mutate bool
}

func (c *collabTestConsumer) observe(event CollabDeliveryEvent) {
	c.events = append(c.events, event)
	if c.mutate && len(event.Payload) > 0 {
		copyForMutation := []byte(event.Payload)
		copyForMutation[0] = 'x'
		event.Payload = string(copyForMutation)
	}
}
func (c *collabTestConsumer) Received(_ context.Context, event CollabDeliveryEvent) { c.observe(event) }
func (c *collabTestConsumer) AboutToWrite(_ context.Context, event CollabDeliveryEvent) {
	c.observe(event)
}
func (c *collabTestConsumer) Written(_ context.Context, event CollabDeliveryEvent) { c.observe(event) }
func (c *collabTestConsumer) Completed(_ context.Context, event CollabDeliveryEvent) {
	c.observe(event)
}

func TestCollabDeliveryHooksMultipleConsumers(t *testing.T) {
	ctx := context.Background()
	var missing *CollabDeliveryHooks
	missing.Register(nil)
	missing.Written(ctx, CollabDeliveryEvent{}, nil)
	h := &CollabDeliveryHooks{}
	first := &collabTestConsumer{mutate: true}
	second := &collabTestConsumer{}
	h.Register(first)
	h.Register(second)
	h.Register(nil)
	payload := []byte("synthetic")
	for _, generation := range []CollabGeneration{1, 2} {
		event := CollabDeliveryEvent{Generation: generation, SendSeq: CollabSendSequence(generation), EventType: "response.output_item.done", Executable: true}
		h.Received(ctx, event, payload)
		h.AboutToWrite(ctx, event, payload)
		h.Written(ctx, event, payload)
		h.Completed(ctx, event, payload)
	}
	require.Len(t, first.events, 8)
	require.Len(t, second.events, 8)
	require.Equal(t, "synthetic", string(payload))
	for i, event := range second.events {
		require.Equal(t, CollabGeneration(i/4+1), event.Generation)
		require.Equal(t, "synthetic", string(event.Payload))
		require.Equal(t, []CollabDeliveryPhase{CollabDeliveryPhaseReceived, CollabDeliveryPhaseAboutToWrite, CollabDeliveryPhaseWritten, CollabDeliveryPhaseCompleted}[i%4], event.Phase)
	}
}

func TestCollabObservationJSONShape(t *testing.T) {
	zero := 0
	document := CollabObservation{V: CollabObservationVersion, Role: CollabRoleChild, RequestKind: CollabRequestKindNormal, Route: CollabRouteResponsesHttp, Client: CollabClientCodexCli, VersionState: CollabVersionStateParsed, FinalMode: CollabFinalModeLocal, InitialReason: CollabReasonAgentDecrypt, StopReason: CollabReasonNone,
		Sends:      []CollabSendObservation{{Seq: 1, Generation: 1, Action: CollabActionOriginal, Result: CollabSendResultCipherRejected, RawPlan: CollabPlanOutcomeApplied, RawReason: CollabPlanReasonNone, Plan: CollabPlanOutcomeApplied, PlanReason: CollabPlanReasonNone, Eligibility: CollabEligibilityEligible, RestoreError: CollabRestoreErrorNone, CallCount: &zero, Marker: CollabMarkerCounts{UpstreamEmpty: &zero}}},
		Deliveries: []CollabDeliveryObservation{{Position: CollabPositionMessage, Form: CollabFormCiphertext, Kind: CollabMessageKindTask, Class: CollabItemClassProtected, Outcome: CollabDeliveryOutcomeNoticePending, Evidence: CollabEvidenceGradeVerified, Stage: CollabStageLocal, Scope: CollabScopeWholeItem}},
		Execution:  CollabExecutionUnknown, Events: []CollabEventObservation{{Source: CollabEventSourceIssued}}, Hints: []CollabHintObservation{{Kind: CollabHintKindConsumer, Cooldown: CollabCooldownNone, Attempt: CollabHintAttemptSelected}}}
	encoded, err := json.Marshal(document)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Len(t, fields, 34)
	for _, key := range []string{"untracked", "counts", "deliveries_total", "events_total", "events_overflow", "hints_total", "hints_overflow", "v", "thread", "tree", "parent", "role", "request_kind", "route", "client", "version_state", "client_version", "covered", "initial_reason", "stop_reason", "final_mode", "sends", "send_total", "send_overflow", "mixed", "deliveries", "deliveries_overflow", "replacements", "local_count", "execution", "events", "input_transformations", "hints", "truncated"} {
		require.Contains(t, fields, key)
	}
	require.Equal(t, "null", string(fields["input_transformations"]))
	require.Contains(t, string(encoded), `"call_count":0`)
	require.Contains(t, string(encoded), `"upstream_empty":0`)
	require.Contains(t, string(encoded), `"delivered_empty":null`)
	var decoded CollabObservation
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, document, decoded)
}

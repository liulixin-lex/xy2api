package service

import (
	"context"
	"crypto/rand"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// OpenAICollabLocalEnding is an exact, already reserved notice. It belongs to
// the inbound owner, never an upstream attempt or its billing result.
type OpenAICollabLocalEnding struct {
	Notice   string
	Response CollabLocalEndingResponseState
	request  *openAICollabRequest
	records  []resilience.CollabTerminatedRecord
}

func (r *openAICollabRequest) localRejection(e resilience.CollabEvidence) resilience.CollabDigest {
	e = ParseCollabBodyWithContext(r.state.Original, CollabBodyContext{Thread: r.thread, ClientVersion: r.version}).ResolveEvidence(e)
	identity := ""
	if e.Named != nil {
		identity = e.Named.Identity
	}
	// Fixed classifier enums and the located item identify the rejection;
	// transient upstream prose, request IDs and account changes do not.
	return r.gateway.CollabDigest(resilience.CollabDigestDomainRejection, string(e.Grade), string(e.Family), string(e.Ownership), identity)
}

func (r *openAICollabRequest) localItems(ctx context.Context, e resilience.CollabEvidence) []resilience.CollabCipherItem {
	parsed := ParseCollabBodyWithContext(r.state.Original, CollabBodyFactsForRequest(ctx))
	e = parsed.ResolveEvidence(e)
	if parsed.Shape.Ambiguous || parsed.Shape.ContinuationPointer || e.Grade != resilience.CollabEvidenceGradeVerified || e.Named != nil && e.Named.Ambiguous {
		return nil
	}
	namedIndex := -1
	if e.Named != nil {
		for _, item := range parsed.Items {
			if item.Identity == e.Named.Identity {
				namedIndex = item.Index
			}
		}
		if namedIndex < 0 {
			return nil
		}
	}
	var selected []resilience.CollabCipherItem
	for _, item := range r.state.Items {
		if r.state.Role == resilience.CollabRoleChild && (item.Class != resilience.CollabItemClassProtected || item.Family != resilience.CollabFamilyAgentMessage || namedIndex >= 0 && item.Position.InputIndex != namedIndex) {
			continue
		}
		selected = append(selected, item)
	}
	// Do not authorize a partial set when an encrypted message has no stable
	// original identity. New deliveries must never inherit an older ending.
	for _, item := range parsed.Items {
		if item.Family == resilience.CollabFamilyCallArgs {
			continue
		}
		if !collabRecordItemIdentity(item.Identity) {
			return nil
		}
	}
	if r.state.Role == resilience.CollabRoleChild && len(selected) > 0 {
		for _, item := range selected[1:] {
			if item.Position.InputIndex != selected[0].Position.InputIndex {
				return nil // Several possible tasks: keep the original error.
			}
		}
	}
	return selected
}

func (r *openAICollabRequest) initCollabAuxiliaryItems() {
	if !r.settings.CollabLadderOn() || r.gateway == nil || r.scope.Tree == "" || r.state.Thread == "" {
		return
	}
	parsed := ParseCollabBodyWithContext(r.state.Original, CollabBodyContext{Thread: r.thread, ClientVersion: r.version})
	for _, item := range parsed.Items {
		if item.Family != resilience.CollabFamilyReasoning && item.Family != resilience.CollabFamilyCompaction || !collabRecordItemIdentity(item.Identity) {
			continue
		}
		for index, path := range item.Paths {
			value := gjson.GetBytes(r.state.Original, path)
			if value.IsObject() {
				value = value.Get("encrypted_content")
			}
			if value.Type != gjson.String {
				continue
			}
			key := resilience.CollabDeliveryKey{Scope: r.scope, Thread: r.state.Thread,
				Item:     r.gateway.CollabDigest(resilience.CollabDigestDomainItem, item.Identity, strconv.Itoa(index)),
				Digest:   r.gateway.CollabDigest(resilience.CollabDigestDomainCiphertext, value.String()),
				Delivery: r.gateway.CollabDigest(resilience.CollabDigestDomainDelivery, string(r.state.Thread), item.Identity, strconv.Itoa(index))}
			r.state.Items = append(r.state.Items, resilience.CollabCipherItem{Key: key, Class: item.Class, Kind: resilience.CollabMessageKindUnknown, Family: item.Family, Position: resilience.CollabInputPosition{InputIndex: item.Index, FragmentIndex: index, Position: resilience.CollabPositionMessage}})
			r.itemPaths = append(r.itemPaths, path)
		}
	}
}

// ReserveOpenAICollabLocalEnding runs only after the seam has stopped writers
// and all upstream readers have returned. Unknown admission never writes bytes.
func (s *OpenAIGatewayService) ReserveOpenAICollabLocalEnding(c *gin.Context, e resilience.CollabEvidence, retryable bool) (plan *OpenAICollabLocalEnding) {
	defer func() {
		if recover() != nil {
			plan = nil
			resilience.Count("collab_wiring_panic", 0, 0, "local_reservation")
		}
	}()
	ctx := c.Request.Context()
	r := openAICollabRequestFrom(ctx)
	owner := resilience.StateFrom(ctx)
	if r == nil || !r.settings.CollabLocalEndingOn(r.state.GroupID) || owner.GatewayRequestFinalized() || r.state.LocalOutcome != 0 {
		return nil
	}
	r.sealSend()
	r.lookupRecords(ctx, false)
	items := r.localItems(ctx, e)
	if len(items) == 0 || !r.lookup.DispositionsComplete {
		return nil
	}
	parsed := ParseCollabBodyWithContext(r.state.Original, CollabBodyFactsForRequest(ctx))
	route, kind := openAICollabIngressRoute(c, r.state.Original)
	w := r.delivery
	w.mu.Lock()
	writeFailed := w.failed != nil
	w.mu.Unlock()
	now := time.Now()
	for _, item := range items {
		if !resilience.CollabLocalEndingEligible(resilience.CollabLocalEndingFacts{
			Settings: r.settings, GroupID: r.state.GroupID, Route: route, RequestKind: kind,
			CompleteHistory: !parsed.Shape.Ambiguous, ContinuationPointer: parsed.Shape.ContinuationPointer,
			EvidenceGrade: e.Grade, LatestRejectionCiphertext: e.Grade == resilience.CollabEvidenceGradeVerified,
			SemanticOutputDelivered: !owner.SemanticOutputAt().IsZero(), TerminalClaimed: ProtocolTerminalClaimed(c),
			WriteFailed: writeFailed || GatewayDeliveryFailed(c), ClientCanceled: ctx.Err() != nil,
			UnreplaceableCallArgs: e.Family == resilience.CollabFamilyCallArgs,
			SavedErrorPending:     true, SavedErrorRetryable: retryable, TreeLocalEndings: r.lookup.LocalEndingCount(),
			Delivery: item.Key, Rejection: r.localRejection(e), PriorRejections: item.Dispositions.TerminalRejections, Now: now,
		}) {
			return nil
		}
	}
	event, err := NewCollabLocalEndingEvent()
	if err != nil {
		return nil
	}
	plan = &OpenAICollabLocalEnding{request: r}
	plan.Response.ResponseID, err = newCollabLocalID("resp_", rand.Reader)
	if err != nil {
		return nil
	}
	plan.Response.CreatedAt = now.Unix()
	eventDigest := s.CollabDigest(resilience.CollabDigestDomainEvent, event)
	for _, item := range items {
		record := resilience.CollabTerminatedRecord{Key: item.Key, Event: eventDigest, Parent: r.state.Parent, ExpiresAt: now.Add(resilience.CollabRecordTTL)}
		// Call arguments are not decrypted by the current protocol. Remember
		// exact copies for provenance while leaving their wire bytes alone.
		for _, call := range gjson.GetBytes(r.state.Original, "input").Array() {
			if call.Get("type").String() != "function_call" || call.Get("namespace").String() != "collaboration" {
				continue
			}
			message := gjson.Get(call.Get("arguments").String(), "message")
			if message.Type == gjson.String && s.CollabDigest(resilience.CollabDigestDomainCiphertext, message.String()) == item.Key.Digest {
				if !collabRecordItemIdentity(call.Get("id").String()) || len(record.Copies) >= resilience.CollabRecordMaxCopies {
					return nil
				}
				record.Copies = append(record.Copies, s.CollabDigest(resilience.CollabDigestDomainItem, call.Get("id").String(), "message"))
			}
		}
		plan.records = append(plan.records, record)
	}
	store := s.CollabRecords()
	if store == nil {
		return nil
	}
	noPlaintextRoute := s.collabLocalNoPlaintextRoute(c, r)
	if _, ok := store.ReserveLocalEnding(ctx, plan.records); !ok {
		return nil
	}
	plan.Notice = resilience.CollabLocalEndingText(event, r.state.Role, r.state.Execution, r.borrowAttempted, noPlaintextRoute)
	return plan
}

func (s *OpenAIGatewayService) collabLocalNoPlaintextRoute(c *gin.Context, r *openAICollabRequest) bool {
	if !r.settings.CollaborationPlaintext.Enabled {
		return true
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), resilience.CollabRecordTimeout)
	defer cancel()
	accounts, err := s.listSelectionMembers(ctx, &r.state.GroupID, PlatformOpenAI)
	if err != nil {
		return false
	} // An unavailable directory proves nothing.
	model := gjson.GetBytes(r.state.Original, "model").String()
	for i := range accounts {
		a := &accounts[i]
		if openAICollaborationEligible(c, a, OpenAIAttemptModel(a, model), OpenAIUpstreamTransportHTTPSSE, a.IsOpenAIPassthroughEnabled(), false, false) {
			return false
		}
	}
	return true
}

// Finish records a local outcome before the request record can be frozen. A
// handoff is provisional; every later request still needs its own receipt.
func (p *OpenAICollabLocalEnding) Finish(ctx context.Context, complete bool) {
	r := p.request
	resilience.StateFrom(ctx).NoteCollabLocalOutcome(complete)
	defer func() {
		if recover() != nil {
			resilience.Count("collab_wiring_panic", 0, 0, "local_completion")
		}
	}()
	outcome, stop := resilience.CollabDeliveryOutcomeUndeliverable, resilience.CollabReasonWriteFailed
	if complete {
		outcome, stop = resilience.CollabDeliveryOutcomeNoticeWrittenUser, resilience.CollabReasonNone
		if r.state.Role == resilience.CollabRoleChild {
			outcome = resilience.CollabDeliveryOutcomeNoticePending
		}
	}
	for _, record := range p.records {
		r.observation.ObserveDelivery(resilience.CollabDeliveryObservation{Delivery: record.Key.Delivery, Item: record.Key.Item, Digest: record.Key.Digest, Position: resilience.CollabPositionMessage, Form: resilience.CollabFormCiphertext, Outcome: outcome, Evidence: resilience.CollabEvidenceGradeVerified, Stage: resilience.CollabStageLocal})
		if complete {
			r.observation.ObserveEvent(resilience.CollabEventObservation{Event: record.Event, Delivery: record.Key.Delivery, Source: resilience.CollabEventSourceIssued})
			if store := r.gateway.CollabRecords(); store != nil {
				store.Terminated(ctx, record)
			}
			r.releaseAcceptedLeases(ctx, []resilience.CollabDeliveryKey{record.Key}, resilience.CollabLeaseReleaseTerminated)
		}
	}
	r.observation.Finish(r.state.InitialReason, stop, resilience.CollabFinalModeLocal, resilience.CollabReplacementCounts{}, 1, r.state.Execution)
}

// RecordOpenAICollabTerminalRejected is called once, only when the seam restores
// today's terminal. Optional attempts and local notices never increment it.
func (s *OpenAIGatewayService) RecordOpenAICollabTerminalRejected(c *gin.Context, e resilience.CollabEvidence) {
	defer func() {
		if recover() != nil {
			resilience.Count("collab_wiring_panic", 0, 0, "terminal_rejection")
		}
	}()
	ctx := c.Request.Context()
	r := openAICollabRequestFrom(ctx)
	if r == nil || !r.settings.CollabLadderOn() || r.state.LocalOutcome != 0 {
		return
	}
	items := r.localItems(ctx, e)
	store := s.CollabRecords()
	if len(items) == 0 || store == nil {
		return
	}
	event, err := NewCollabLocalEndingEvent()
	if err != nil {
		return
	}
	for _, item := range items {
		store.TerminalRejected(ctx, resilience.CollabTerminalRejectionRecord{Key: item.Key, Rejection: r.localRejection(e), Event: s.CollabDigest(resilience.CollabDigestDomainEvent, event), ExpiresAt: time.Now().Add(resilience.CollabRecordTTL)})
	}
}

// LocalEndingBeforeDisposal is limited to a child's active task with no later
// user input or readable new task. Other requests exhaust disposal first.
func LocalEndingBeforeDisposal(ctx context.Context) bool {
	r := openAICollabRequestFrom(ctx)
	if r == nil || r.state.Role != resilience.CollabRoleChild {
		return false
	}
	input := gjson.GetBytes(r.state.Original, "input").Array()
	lastTask := -1
	for _, item := range r.state.Items {
		if item.Class == resilience.CollabItemClassProtected && item.Family == resilience.CollabFamilyAgentMessage {
			lastTask = max(lastTask, item.Position.InputIndex)
		}
	}
	if lastTask < 0 {
		return false
	}
	for i := lastTask + 1; i < len(input); i++ {
		if input[i].Get("role").String() == "user" || collabEnvelopeKind(input[i]) == "NEW_TASK" {
			return false
		}
	}
	return true
}

// Kept with wiring so failures hidden by a void Flush can be inspected by the
// notice owner without unwrapping or bypassing the seam writer.
func OpenAICollabLocalWriteError(ctx context.Context) error {
	if r := openAICollabRequestFrom(ctx); r != nil {
		r.delivery.mu.Lock()
		defer r.delivery.mu.Unlock()
		return r.delivery.failed
	}
	return nil
}

func (w *openAICollabDeliveryWriter) CollabDeliveryError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failed
}

func SealOpenAICollabAttemptForLocal(ctx context.Context) {
	if attempt := currentSchedAttempt(ctx); attempt != nil {
		attempt.finishUnfinished()
	}
}

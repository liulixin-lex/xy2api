package service

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

var collabObservationSample atomic.Uint64

func (r *openAICollabRequest) initObservation(c *gin.Context) {
	r.client, r.versionState, r.version = resilience.ParseCollabClient(c.GetHeader("User-Agent"))
	route, kind := openAICollabIngressRoute(c, r.state.Original)
	r.state.Kind = kind
	r.state.InitialReason, r.state.StopReason = resilience.CollabReasonNone, resilience.CollabReasonNone
	r.observation = resilience.NewCollabObservationAccumulator(resilience.CollabObservation{
		V: resilience.CollabObservationVersion, Role: r.state.Role, RequestKind: kind, Route: route,
		Thread: r.state.Thread, Tree: r.state.Tree, Parent: r.state.Parent,
		Client: r.client, VersionState: r.versionState, ClientVersion: r.version, Covered: route == resilience.CollabRouteResponsesHttp,
		InitialReason: resilience.CollabReasonNone, StopReason: resilience.CollabReasonNone, FinalMode: resilience.CollabFinalModeNone,
		Execution: resilience.CollabExecutionUnknown,
	}, true)
	r.state.Observer = resilience.SafeCollabObserver{Observer: r.observation}
	r.state.DeliveryHooks.Register(&openAICollabCallObservation{request: r})
}

func openAICollabIngressRoute(c *gin.Context, body []byte) (resilience.CollabRoute, resilience.CollabRequestKind) {
	route, kind := resilience.CollabRouteResponsesHttp, resilience.CollabRequestKindNormal
	if IsOpenAIResponsesCompactPath(c) || IsOpenAINativeCompactionV2(c) {
		route, kind = resilience.CollabRouteCompact, resilience.CollabRequestKindCompaction
	} else if strings.Contains(gjson.GetBytes(body, "model").String(), "review") {
		kind = resilience.CollabRequestKindReview
	} else if format := gjson.GetBytes(body, "text.format.type").String(); format != "" && format != "text" {
		kind = resilience.CollabRequestKindStructuredOutput
	}
	return route, kind
}

func countOpenAICollabIngress(c *gin.Context, body []byte, ingress resilience.CollabIngressObservation) {
	client, _, _ := resilience.ParseCollabClient(c.GetHeader("User-Agent"))
	if client == resilience.CollabClientOther || client == resilience.CollabClientUnknown {
		return
	}
	route, kind := openAICollabIngressRoute(c, body)
	// The parser and route helper already return fixed values. Avoid allocating
	// an observation document for ordinary requests merely to sanitize labels.
	if !ingress.Observed {
		resilience.Count("collab_ingress_unobserved", 0, 0, "")
		return
	}
	resilience.Count("collab_ingress_carrier", 0, 0, ingress.Carrier)
	resilience.Count("collab_ingress_route", 0, 0, string(route))
	resilience.Count("collab_ingress_kind", 0, 0, string(kind))
	if ingress.SuspectedCustomNamespace {
		resilience.Count("collab_ingress_custom_namespace", 0, 0, "")
	}
	if ingress.UnrecognizedCatalog {
		resilience.Count("collab_ingress_unrecognized_catalog", 0, 0, "")
	}
}

type openAICollabSendObservation struct {
	mu              sync.Mutex
	request         *openAICollabRequest
	value           resilience.CollabSendObservation
	shape           resilience.CollabRequestShape
	status          int
	source          resilience.CollabEvidenceSource
	stream          bool
	buffer          []byte
	oversized       bool
	sealed          bool
	evidence        *resilience.CollabEvidence
	transformations *resilience.CollabInputTransformations
	readErr         error
	ctx             context.Context
	unchanged       []resilience.CollabDeliveryKey
	replaced        []resilience.CollabDeliveryObservation
	replacements    resilience.CollabReplacementCounts
	upstreamCalls   map[string]bool
	deliveredCalls  map[string]bool
	calls           int
	upstreamEmpty   int
	deliveredEmpty  int

	auxiliary          []openAICollabAuxiliaryObservation
	auxiliaryUnchanged bool
}

type openAICollabCallObservation struct {
	resilience.NoopCollabDeliveryConsumer
	request *openAICollabRequest
}

func (o *openAICollabCallObservation) Written(_ context.Context, event resilience.CollabDeliveryEvent) {
	s := o.request.send
	if s == nil || event.Generation != s.value.Generation || !event.Successful || !event.Executable {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sealed {
		s.observeCalls(event.EventType, []byte(event.Payload), true)
	}
}

func (s *openAICollabSendObservation) observeCalls(event string, payload []byte, delivered bool) {
	root := gjson.ParseBytes(payload)
	check := func(item gjson.Result) {
		namespace := item.Get("namespace").String()
		if item.Get("type").String() != "function_call" || namespace != "collaboration" && namespace != "collaboration-xy" {
			return
		}
		id := item.Get("call_id").String()
		if id == "" {
			id = item.Get("id").String()
		}
		if id == "" {
			return
		}
		seen := &s.upstreamCalls
		if delivered {
			seen = &s.deliveredCalls
		}
		if (*seen)[id] || len(*seen) >= 256 {
			return
		}
		if *seen == nil {
			*seen = map[string]bool{}
		}
		(*seen)[id] = true
		if !delivered {
			s.calls++
		}
		marker := item.Get("encrypted_function_args")
		if marker.IsArray() && len(marker.Array()) == 0 {
			if delivered {
				s.deliveredEmpty++
			} else {
				s.upstreamEmpty++
			}
		}
	}
	if event == "response.output_item.done" {
		check(root.Get("item"))
		return
	}
	if root.Get("response").IsObject() {
		root = root.Get("response")
	}
	for _, item := range root.Get("output").Array() {
		check(item)
	}
}

func (r *openAICollabRequest) beginSendObservation(ctx context.Context) {
	r.delivery.mu.Lock()
	event := r.delivery.current
	r.delivery.mu.Unlock()
	action := resilience.CollabAttemptFrom(ctx).Action
	if action == "" {
		action = resilience.CollabActionOriginal
	}
	value := resilience.CollabSendObservation{Seq: event.SendSeq, Generation: event.Generation, Action: action, Result: resilience.CollabSendResultTransientError,
		RawPlan: resilience.CollabPlanOutcomeAbsent, RawReason: resilience.CollabPlanReasonNoNamespace, Plan: resilience.CollabPlanOutcomeAbsent, PlanReason: resilience.CollabPlanReasonNoNamespace,
		Eligibility: resilience.CollabEligibilityFeatureOff, RestoreError: resilience.CollabRestoreErrorNone}
	if r.gateway != nil {
		if target := resilience.StateFrom(ctx).UpstreamAttemptTarget(); target != nil {
			value.Account, _ = r.gateway.CollabRecordAccount(target.AccountID, target.Origin)
			r.state.CurrentAccountID = target.AccountID
		}
	}
	if a := r.attempt; a != nil {
		value.RawPlan, value.Plan = resilience.CollabPlanOutcome(a.outcome), resilience.CollabPlanOutcome(a.outcome)
		value.RawReason, value.PlanReason = codexCollabLegacyReason(a.outcome), codexCollabLegacyReason(a.outcome)
		if a.applied {
			value.Eligibility = resilience.CollabEligibilityEligible
		}
	}
	if r.state.RawPlan != "" {
		value.RawPlan, value.RawReason = r.state.RawPlan, r.state.RawReason
	}
	r.send = &openAICollabSendObservation{ctx: ctx, request: r, value: value, shape: resilience.CollabRequestShape{Kind: r.state.Kind, Route: resilience.CollabRouteResponsesHttp, Codex: r.client != resilience.CollabClientOther && r.client != resilience.CollabClientUnknown, AgentCiphertext: r.ingress.EncryptedAgent, ToolOutputCiphertext: r.ingress.EncryptedTool}}
}

func (s *openAICollabSendObservation) receive(resp *http.Response, err error) {
	s.readErr = err
	if resp == nil || resp.Body == nil {
		return
	}
	s.status = resp.StatusCode
	s.stream = isEventStreamResponse(resp.Header)
	s.source = resilience.CollabEvidenceSourceFailedJson
	if resp.StatusCode >= 400 {
		s.source = resilience.CollabEvidenceSourceHttpError
	} else if s.stream {
		s.source = resilience.CollabEvidenceSourceStreamError
		if !gjson.GetBytes(s.request.state.Original, "stream").Bool() {
			s.source = resilience.CollabEvidenceSourceAggregatedStream
		}
	}
	resp.Body = &openAICollabObservationBody{ReadCloser: resp.Body, send: s}
}

type openAICollabObservationBody struct {
	io.ReadCloser
	send *openAICollabSendObservation
}

func (b *openAICollabObservationBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.send.observe(p[:n], err)
	return n, err
}

func (s *openAICollabSendObservation) observe(p []byte, err error) {
	// Observation cannot change read boundaries or the upstream's error.
	defer func() { _ = recover() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed || s.oversized {
		return
	}
	if err != nil && err != io.EOF {
		s.readErr = err
	}
	if len(s.buffer)+len(p) > collabDeliveryBufferLimit {
		s.buffer = nil
		s.oversized = true
		return
	}
	s.buffer = append(s.buffer, p...)
	if !s.stream {
		if err == io.EOF && gjson.ValidBytes(s.buffer) {
			s.payload("", s.buffer)
			s.buffer = nil
		}
		return
	}
	for {
		end, sep := sseBlockEnd(s.buffer)
		if end < 0 {
			break
		}
		event, payload := sseBlockFields(s.buffer[:end])
		s.payload(event, payload)
		s.buffer = s.buffer[end+sep:]
	}
}

func (s *openAICollabSendObservation) payload(event string, payload []byte) {
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	if event == "" {
		event = root.Get("type").String()
	}
	s.observeCalls(event, payload, false)
	response := root
	if root.Get("response").IsObject() {
		response = root.Get("response")
	}
	status := response.Get("status").String()
	if s.status >= 400 || event == "response.failed" || event == "error" || status == "failed" || root.Get("error").IsObject() || response.Get("error").IsObject() {
		evidence := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: payload, HTTPStatus: s.status, Event: event, Source: s.source, Shape: s.shape})
		s.evidence = &evidence
		s.value.Result = resilience.CollabSendResultUnrelatedRejected
		switch evidence.Grade {
		case resilience.CollabEvidenceGradeVerified:
			s.value.Result = resilience.CollabSendResultCipherRejected
		case resilience.CollabEvidenceGradeClaimed:
			s.value.Result = resilience.CollabSendResultUncertainRejected
		}
	} else if event == "response.completed" || event == "response.done" || status == "completed" || !s.stream && response.Get("output").IsArray() {
		s.value.Result = resilience.CollabSendResultAccepted
	}
	if report := response.Get("input_transformations"); report.IsArray() && s.transformations == nil {
		s.transformations = &resilience.CollabInputTransformations{Counts: map[resilience.CollabTransformationReason]int{}}
		for _, entry := range report.Array() {
			s.transformations.Counts[resilience.ClassifyCollabTransformationReason(entry.Get("reason").String())]++
		}
	}
}

func (r *openAICollabRequest) sealSend() {
	defer func() { _ = recover() }()
	s := r.send
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return
	}
	if !s.stream && len(s.buffer) > 0 {
		s.payload("", s.buffer)
	}
	s.buffer = nil
	s.sealed = true
	s.value.CallCount = &s.calls
	s.value.Marker.UpstreamEmpty = &s.upstreamEmpty
	s.value.Marker.DeliveredEmpty = &s.deliveredEmpty
	if s.readErr == context.Canceled || s.readErr == context.DeadlineExceeded {
		s.value.Result = resilience.CollabSendResultCanceled
	}
	if a := r.attempt; a != nil && a.generation == s.value.Generation {
		stats, _ := a.restorer.SealStats()
		s.value.Marker.Add = &stats.MarkerAdded
		s.value.Marker.Veto = &stats.Vetoed
		if a.restoreFailed {
			s.value.RestoreError = resilience.CollabRestoreErrorInvalidItem
		}
	}
	if s.evidence != nil {
		r.state.LatestEvidence = s.evidence
		reason := collabWiringEvidenceReason(*s.evidence)
		if r.state.FirstEvidence == nil {
			r.state.FirstEvidence = s.evidence
			r.state.InitialReason = reason
		}
		r.state.StopReason = reason
	}
	r.observation.ObserveInputTransformations(s.value.Generation, s.transformations)
	r.observation.SealSend(s.value)
	r.failed = s.value.Result != resilience.CollabSendResultAccepted
	if s.value.Result == resilience.CollabSendResultCipherRejected && s.value.Account != "" {
		if r.rejectedAccounts == nil {
			r.rejectedAccounts = map[resilience.CollabDigest]bool{}
		}
		r.rejectedAccounts[s.value.Account] = true
	}
	s.consumed()
	mode := resilience.CollabFinalModeNone
	if s.value.Result == resilience.CollabSendResultAccepted {
		mode = resilience.CollabFinalModeCiphertext
		if r.attempt != nil && r.attempt.applied {
			mode = resilience.CollabFinalModePlaintext
		}
		if s.replacements != (resilience.CollabReplacementCounts{}) {
			if len(s.unchanged) > 0 || s.auxiliaryUnchanged || mode == resilience.CollabFinalModePlaintext {
				mode = resilience.CollabFinalModeMixed
			} else {
				mode = resilience.CollabFinalModePlaceholder
			}
		}
		r.state.StopReason = resilience.CollabReasonNone
	}
	r.observation.Finish(r.state.InitialReason, r.state.StopReason, mode, s.replacements, 0, r.state.Execution)
}

func collabWiringEvidenceReason(e resilience.CollabEvidence) resilience.CollabReason {
	if e.Grade != resilience.CollabEvidenceGradeVerified {
		return resilience.CollabReasonUnrelated
	}
	switch e.Family {
	case resilience.CollabFamilyAgentMessage:
		return resilience.CollabReasonAgentDecrypt
	case resilience.CollabFamilyToolOutput:
		return resilience.CollabReasonToolDecrypt
	case resilience.CollabFamilyReasoning:
		return resilience.CollabReasonReasoningDecrypt
	case resilience.CollabFamilyCompaction:
		return resilience.CollabReasonCompactionDecrypt
	case resilience.CollabFamilyCallArgs:
		return resilience.CollabReasonCallArgsDecrypt
	case resilience.CollabFamilyOwnership:
		return resilience.CollabReasonRelayOwnership
	default:
		return resilience.CollabReasonOther
	}
}

func SealOpenAICollabObservation(c *gin.Context) {
	if c != nil && c.Request != nil {
		if r := openAICollabRequestFrom(c.Request.Context()); r != nil {
			r.sealSend()
		}
	}
}

func LogOpenAICollabObservation(c *gin.Context, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	r := openAICollabRequestFrom(c.Request.Context())
	if r == nil {
		client, version, _ := resilience.ParseCollabClient(c.GetHeader("User-Agent"))
		if client == resilience.CollabClientOther || client == resilience.CollabClientUnknown {
			return
		}
		if resilience.ShouldLogCollabObservation(c.Writer.Status() < 400, 0, resilience.CollabIngressObservation{}, collabObservationSample.Add(1)) {
			route, kind := openAICollabIngressRoute(c, body)
			slog.InfoContext(c.Request.Context(), "collab.request", "client", client, "version_state", version, "route", route, "request_kind", kind)
		}
		return
	}
	r.sealSend()
	value := r.observation.Freeze()
	if value == nil {
		return
	}
	calls := 0
	for _, send := range value.Sends {
		if send.CallCount != nil {
			calls += *send.CallCount
		}
	}
	if resilience.ShouldLogCollabObservation(c.Writer.Status() < 400, calls, r.ingress, collabObservationSample.Add(1)) {
		slog.InfoContext(c.Request.Context(), "collab.request", "client", value.Client, "version_state", value.VersionState, "route", value.Route, "request_kind", value.RequestKind, "send_total", value.SendTotal, "initial_reason", value.InitialReason, "stop_reason", value.StopReason, "final_mode", value.FinalMode)
	}
}

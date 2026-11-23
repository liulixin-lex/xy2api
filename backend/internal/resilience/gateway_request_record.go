package resilience

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	RequestSuccess  int16 = 1
	RequestRescued  int16 = 2
	RequestFailed   int16 = 3
	RequestCanceled int16 = 4
)

// GatewayRequestMetadata is frozen before arranging billing. It must survive a
// later websocket turn and client cancellation without consulting mutable state.
type GatewayRequestMetadata struct {
	CollabObservation  json.RawMessage
	ProfitBasis        *ProfitBasis
	RequestScope       GatewayRequestScope
	ConversionNotices  []apicompat.ConversionNotice `json:"degradations,omitempty"`
	UsageObserved      *bool
	UsageSource        *string
	TerminalEventSeen  *bool
	TerminalEventValid *bool
	ResponseBodyType   *string

	RequestID                                         string
	Result                                            int16
	Switches                                          int16
	Fallback, Takeover                                bool
	StartedAt                                         time.Time
	AccountID                                         int64
	FirstOutputMs                                     *int64
	DurationMs                                        int
	ErrorClass                                        string
	InputTokens, CacheReadTokens, CacheCreationTokens *int
}

type gatewayRequestTrace struct {
	profit             *profitTrace
	RequestScope       GatewayRequestScope
	ConversionNotices  []apicompat.ConversionNotice `json:"degradations,omitempty"`
	UsageObserved      *bool
	UsageSource        *string
	TerminalEventSeen  *bool
	TerminalEventValid *bool
	ResponseBodyType   *string
	excluded           bool

	started                    time.Time
	sends                      int
	failedBeforeSend           bool
	currentFailed              bool
	lastAccount                int64
	switches                   int16
	fallback, takeover         bool
	failed, complete, canceled bool
	errorClass                 string
	input, read, creation      *int
	final                      *GatewayRequestMetadata
	billing, claimed           bool
}

// EnableGatewayRequest is called only after ingress validation, before account
// selection. The legacy single-argument call enables text statistics.
func (s *RequestState) EnableGatewayRequest(at time.Time, scopes ...GatewayRequestScope) {
	if s == nil {
		return
	}
	scope := GatewayRequestScopeText
	if len(scopes) > 1 {
		return
	}
	if len(scopes) == 1 {
		scope = scopes[0]
	}
	switch scope {
	case GatewayRequestScopeText, GatewayRequestScopeImage, GatewayRequestScopeEmbedding, GatewayRequestScopeSearch:
	default:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gatewayRecord == nil {
		s.d.mu.Lock()
		if !s.d.started.IsZero() {
			at = s.d.started
		}
		s.d.mu.Unlock()
		s.gatewayAdmissionScope = scope
		s.gatewayRecord = &gatewayRequestTrace{started: at, RequestScope: scope}
	}
}

// ExcludeGatewayRequest disables text statistics while preserving correlation
// and facts for a trace that has already been admitted.
func (s *RequestState) ExcludeGatewayRequest() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.gatewayRecord; r != nil && r.final == nil {
		r.excluded = true
		if r.RequestScope == GatewayRequestScopeText {
			r.RequestScope = ""
		}
	}
}

func (s *RequestState) GatewayRequestEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.gatewayRecord
	return r != nil && !r.excluded && (r.RequestScope == "" || r.RequestScope == GatewayRequestScopeText)
}

// BeginGatewayTurn starts an admitted logical turn, not a new connection. All
// asynchronous consumers of the preceding turn already own a frozen snapshot.
func (s *RequestState) BeginGatewayTurn(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.requestID = uuid.NewString()
	s.stickyChange = nil
	s.voluntarySwitchVetoLogged = false
	// A WebSocket turn remains pinned to the account selected for its
	// connection. Its fallback standing is recalculated by the handler for each
	// admitted turn; takeover is a one-time binding change and must not leak.
	s.selectionFlags.takeover = false
	s.currentAttempt = nil
	s.turnStartSendSeq = s.upstreamSendSeq
	s.ownerCutoff = OwnerFirstOutputCutoff{}
	s.ownerAvoidConfirmed = false
	s.schedSelection.HTTPStreamText = false
	s.gatewayRecord = &gatewayRequestTrace{started: at, RequestScope: s.gatewayAdmissionScope}
	s.firstOutput = time.Time{}
	s.firstOutputNoted.Store(false)
	s.mu.Unlock()
	s.d.mu.Lock()
	s.d.started, s.d.semanticOutput = at, time.Time{}
	s.d.mu.Unlock()
}

func (s *RequestState) noteGatewaySendLocked(accountID int64) {
	// A later send (including a WebSocket turn) cannot inherit HTTP headers
	// from the preceding attempt, even when request statistics are disabled.
	if accountID > 0 {
		s.upstreamHTTPAccount, s.upstreamHTTPStatus = accountID, 0
		s.sessionDispatch.requestID = s.requestID
		s.sessionDispatch.accountID = accountID
		s.sessionDispatch.at = time.Now()
	}
	r := s.gatewayRecord
	if r == nil || r.final != nil || accountID <= 0 {
		return
	}
	if r.lastAccount != 0 && r.lastAccount != accountID && r.switches < 32767 {
		r.switches++
	}
	r.lastAccount = accountID
	r.profit.noteSend(accountID)
	r.fallback = s.selectionFlags.accountID == accountID && s.selectionFlags.fallback
	r.takeover = s.selectionFlags.accountID == accountID && s.selectionFlags.takeover
	if r.sends > 0 && r.currentFailed {
		r.failedBeforeSend = true
	}
	r.currentFailed = false
	r.sends++
	r.complete = false
	r.UsageObserved, r.UsageSource = nil, nil
	r.TerminalEventSeen, r.TerminalEventValid, r.ResponseBodyType = nil, nil, nil
}

// NoteGatewaySend also serves native WS sends, which have no HTTP boundary.
func (s *RequestState) NoteGatewaySend(accountID int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteGatewaySendLocked(accountID)
}

func (s *RequestState) NoteGatewayAttempt(success bool, detail string, input, read, creation *int) {
	s.NoteGatewayAttemptForRequest("", success, detail, input, read, creation)
}

// NoteGatewayAttemptForRequest prevents a late previous-turn capture from
// adding failure or usage facts to a new WebSocket request. An empty ID denotes
// the synchronous current-request callers of NoteGatewayAttempt.
func (s *RequestState) NoteGatewayAttemptForRequest(requestID string, success bool, detail string, input, read, creation *int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.gatewayRecord
	if r == nil || r.final != nil || requestID != "" && requestID != s.requestID {
		return
	}
	if !success {
		r.failed = true
		r.currentFailed = true
		r.errorClass = detail
	}
	addKnown := func(dst **int, value *int) {
		if value == nil {
			return
		}
		if *dst == nil {
			*dst = new(int)
		}
		**dst += max(*value, 0)
	}
	addKnown(&r.input, input)
	addKnown(&r.read, read)
	addKnown(&r.creation, creation)
}

// CompleteGatewayForward consumes actual forwarding/delivery evidence. Wire
// status and presence of a bill are deliberately absent from this decision.
func (s *RequestState) CompleteGatewayForward(complete, canceled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.gatewayRecord
	if r == nil || r.final != nil {
		return
	}
	r.complete, r.canceled = complete && !canceled, canceled
	if !complete && r.sends > 0 {
		r.failed = true
		r.currentFailed = true
	}
}

// ClaimGatewayRequest picks the sole row owner. Billing writes keep ownership
// even if persistence later fails; the deferred tail must not write a second row.
// GatewayRequestFinalized reports whether billing or the unbilled record has
// already claimed and frozen this request.
func (s *RequestState) GatewayRequestFinalized() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gatewayRecord != nil && s.gatewayRecord.final != nil
}

func (s *RequestState) ClaimGatewayRequest(billing bool) (*GatewayRequestMetadata, bool) {
	if s == nil {
		return nil, false
	}
	first := s.EndToEndFirstOutputMs()
	canceled := s.ClientCanceled()
	waived := false
	if !billing {
		s.d.mu.Lock()
		// Only billing's explicit undelivered waiver records usage with billed=false.
		// Neither an unattempted/failed bill nor a zero-cost usage row is a waiver.
		waived = s.d.disconnected && !s.d.delivered && !s.d.abuse && len(s.d.usageJSON) > 0 && !s.d.billed
		s.d.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.gatewayRecord
	if r == nil {
		return nil, false
	}
	if r.final == nil {
		result := RequestFailed
		switch {
		case r.canceled || !r.complete && canceled:
			result = RequestCanceled
		case r.complete && r.sends >= 2 && r.failedBeforeSend:
			result = RequestRescued
		case r.complete:
			result = RequestSuccess
			if r.failed {
				logger.L().Warn("sched_quality.delivery_capture_mismatch", zap.String("component", "sched_quality"),
					zap.String("request_id", s.requestID), zap.Int("sends", r.sends), zap.String("sample_error_class", r.errorClass))
			}
		}
		r.final = cloneGatewayRequest(&GatewayRequestMetadata{CollabObservation: freezeCollabObservationForBilling(s.collab), RequestID: s.requestID, Result: result, Switches: r.switches, Fallback: r.fallback, Takeover: r.takeover,
			StartedAt: r.started, AccountID: r.lastAccount, FirstOutputMs: first, DurationMs: int(time.Since(r.started).Milliseconds()),
			ErrorClass: r.errorClass, InputTokens: r.input, CacheReadTokens: r.read, CacheCreationTokens: r.creation,
			RequestScope: r.RequestScope, ConversionNotices: s.conversionNotices,
			ProfitBasis:   r.profit.snapshot(),
			UsageObserved: r.UsageObserved, UsageSource: r.UsageSource, TerminalEventSeen: r.TerminalEventSeen,
			TerminalEventValid: r.TerminalEventValid, ResponseBodyType: r.ResponseBodyType})
		r.billing = billing
	}
	if !billing && r.billing && waived && gatewayRequestWaiverTailEnabled(r.RequestScope) {
		// Inline billing has explicitly declined both settlement and a usage row.
		// Cancellation may arrive after the billing claim froze a failed write.
		// Normalize only the waived tail's result; retain all other frozen facts.
		r.final.Result = RequestCanceled
		r.billing, r.claimed = false, false
	}
	writeUnbilled := !r.billing && !r.claimed && (!r.excluded || r.RequestScope != "")
	r.claimed = true
	return cloneGatewayRequest(r.final), writeUnbilled
}

// Text and search transfer ownership only after an explicit undelivered waiver.
func gatewayRequestWaiverTailEnabled(scope GatewayRequestScope) bool {
	return scope == GatewayRequestScopeSearch || scope == GatewayRequestScopeText
}

func cloneGatewayRequest(in *GatewayRequestMetadata) *GatewayRequestMetadata {
	if in == nil {
		return nil
	}
	v := *in
	v.CollabObservation = NormalizeCollabObservationJSON(in.CollabObservation)
	v.ProfitBasis = CloneProfitBasis(in.ProfitBasis)
	copyInt := func(p *int) *int {
		if p == nil {
			return nil
		}
		n := *p
		return &n
	}
	v.InputTokens, v.CacheReadTokens, v.CacheCreationTokens = copyInt(in.InputTokens), copyInt(in.CacheReadTokens), copyInt(in.CacheCreationTokens)
	copyBool := func(p *bool) *bool {
		if p == nil {
			return nil
		}
		n := *p
		return &n
	}
	copyString := func(p *string) *string {
		if p == nil {
			return nil
		}
		n := *p
		return &n
	}
	v.UsageObserved = copyBool(in.UsageObserved)
	v.UsageSource = copyString(in.UsageSource)
	v.TerminalEventSeen, v.TerminalEventValid = copyBool(in.TerminalEventSeen), copyBool(in.TerminalEventValid)
	v.ResponseBodyType = copyString(in.ResponseBodyType)
	v.ConversionNotices = append([]apicompat.ConversionNotice(nil), in.ConversionNotices...)
	if in.FirstOutputMs != nil {
		n := *in.FirstOutputMs
		v.FirstOutputMs = &n
	}
	return &v
}

type gatewayRequestMetadataKey struct{}

func WithGatewayRequestMetadata(ctx context.Context, meta *GatewayRequestMetadata) context.Context {
	return context.WithValue(ctx, gatewayRequestMetadataKey{}, cloneGatewayRequest(meta))
}

func GatewayRequestMetadataFromContext(ctx context.Context) *GatewayRequestMetadata {
	if ctx == nil {
		return nil
	}
	meta, _ := ctx.Value(gatewayRequestMetadataKey{}).(*GatewayRequestMetadata)
	return cloneGatewayRequest(meta)
}

// GatewayRequestScope separates request correlation from text statistics.
type GatewayRequestScope string

const (
	GatewayRequestScopeText      GatewayRequestScope = "text"
	GatewayRequestScopeImage     GatewayRequestScope = "image"
	GatewayRequestScopeEmbedding GatewayRequestScope = "embedding"
	GatewayRequestScopeSearch    GatewayRequestScope = "search"
)

func (s *RequestState) GatewayRequestScope() (GatewayRequestScope, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gatewayRecord == nil {
		return "", false
	}
	return s.gatewayRecord.RequestScope, true
}

// SetGatewayResponseFacts records only authenticated evidence for the current account.
func (s *RequestState) SetGatewayResponseFacts(accountID int64, known, usagePresent bool, usageSource string, terminalSeen, terminalValid bool, bodyKind string) bool {
	if s == nil || !known || accountID <= 0 {
		return false
	}
	switch usageSource {
	case "missing", "upstream_json", "upstream_sse_terminal", "upstream_sse_progressive":
	default:
		return false
	}
	if usagePresent == (usageSource == "missing") {
		return false
	}
	switch bodyKind {
	case "", "json", "sse":
	default:
		return false
	}
	if bodyKind != "sse" && (terminalSeen || terminalValid) || terminalValid && !terminalSeen {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.gatewayRecord
	if r == nil || r.claimed || r.final != nil || r.lastAccount != accountID {
		return false
	}
	r.UsageObserved, r.UsageSource = &usagePresent, &usageSource
	r.TerminalEventSeen, r.TerminalEventValid, r.ResponseBodyType = &terminalSeen, &terminalValid, &bodyKind
	return true
}

// NoteUpstreamHTTPStatus stores only the actual response head for the active
// send. A zero status resets it before transport; semantic error projections
// must never be passed here.
func (s *RequestState) NoteUpstreamHTTPStatus(accountID int64, status int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreamHTTPAccount, s.upstreamHTTPStatus = accountID, status
}

func (s *RequestState) UpstreamHTTPStatus(accountID int64) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if accountID != 0 && accountID != s.upstreamHTTPAccount {
		return 0
	}
	return s.upstreamHTTPStatus
}

// NoteClientOutputBytes records confirmed client payload writes, independently
// of semantic-output eligibility. HTTP supplies Write's n; WebSocket supplies
// the payload size only after a successful frame write.
func (s *RequestState) NoteClientOutputBytes(n int) {
	if s != nil && n > 0 {
		s.clientOutputBytes.Add(int64(n))
	}
}

func (s *RequestState) ClientOutputBytes() int64 {
	if s != nil && s.collabParent != nil {
		return s.collabParent.ClientOutputBytes()
	}
	if s == nil {
		return 0
	}
	return s.clientOutputBytes.Load()
}

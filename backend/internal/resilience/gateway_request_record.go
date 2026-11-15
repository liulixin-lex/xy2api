package resilience

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
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
	case GatewayRequestScopeText, GatewayRequestScopeImage, GatewayRequestScopeEmbedding:
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
	// A WebSocket turn remains pinned to the account selected for its
	// connection. Its fallback standing is recalculated by the handler for each
	// admitted turn; takeover is a one-time binding change and must not leak.
	s.selectionFlags.takeover = false
	s.currentAttempt = nil
	s.gatewayRecord = &gatewayRequestTrace{started: at}
	s.firstOutput = time.Time{}
	s.firstOutputNoted.Store(false)
	s.mu.Unlock()
	s.d.mu.Lock()
	s.d.started, s.d.semanticOutput = at, time.Time{}
	s.d.mu.Unlock()
}

func (s *RequestState) noteGatewaySendLocked(accountID int64) {
	r := s.gatewayRecord
	if r == nil || r.final != nil || accountID <= 0 {
		return
	}
	if r.lastAccount != 0 && r.lastAccount != accountID && r.switches < 32767 {
		r.switches++
	}
	r.lastAccount = accountID
	r.fallback = s.selectionFlags.accountID == accountID && s.selectionFlags.fallback
	r.takeover = s.selectionFlags.accountID == accountID && s.selectionFlags.takeover
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
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.gatewayRecord
	if r == nil || r.final != nil {
		return
	}
	if !success {
		r.failed = true
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
	}
}

// ClaimGatewayRequest picks the sole row owner. Billing writes keep ownership
// even if persistence later fails; the deferred tail must not write a second row.
func (s *RequestState) ClaimGatewayRequest(billing bool) (*GatewayRequestMetadata, bool) {
	if s == nil {
		return nil, false
	}
	first := s.EndToEndFirstOutputMs()
	canceled := s.ClientCanceled()
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
		case r.complete && r.failed:
			result = RequestRescued
		case r.complete:
			result = RequestSuccess
		}
		r.final = cloneGatewayRequest(&GatewayRequestMetadata{RequestID: s.requestID, Result: result, Switches: r.switches, Fallback: r.fallback, Takeover: r.takeover,
			StartedAt: r.started, AccountID: r.lastAccount, FirstOutputMs: first, DurationMs: int(time.Since(r.started).Milliseconds()),
			ErrorClass: r.errorClass, InputTokens: r.input, CacheReadTokens: r.read, CacheCreationTokens: r.creation,
			RequestScope: r.RequestScope, ConversionNotices: s.conversionNotices,
			UsageObserved: r.UsageObserved, UsageSource: r.UsageSource, TerminalEventSeen: r.TerminalEventSeen,
			TerminalEventValid: r.TerminalEventValid, ResponseBodyType: r.ResponseBodyType})
		r.billing = billing
	}
	writeUnbilled := !r.billing && !r.claimed && (!r.excluded || r.RequestScope != "")
	r.claimed = true
	return cloneGatewayRequest(r.final), writeUnbilled
}

func cloneGatewayRequest(in *GatewayRequestMetadata) *GatewayRequestMetadata {
	if in == nil {
		return nil
	}
	v := *in
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

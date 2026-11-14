package resilience

import (
	"context"
	"time"

	"github.com/google/uuid"
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
// selection. Excluded endpoints never enable this trace.
func (s *RequestState) EnableGatewayRequest(at time.Time) {
	if s == nil {
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
		s.gatewayRecord = &gatewayRequestTrace{started: at}
	}
}

// ExcludeGatewayRequest keeps opaque/binary forwarding on its existing billing path.
func (s *RequestState) ExcludeGatewayRequest() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.gatewayRecord = nil
	s.mu.Unlock()
}

func (s *RequestState) GatewayRequestEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gatewayRecord != nil
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
		r.final = &GatewayRequestMetadata{RequestID: s.requestID, Result: result, Switches: r.switches, Fallback: r.fallback, Takeover: r.takeover,
			StartedAt: r.started, AccountID: r.lastAccount, FirstOutputMs: first, DurationMs: int(time.Since(r.started).Milliseconds()),
			ErrorClass: r.errorClass, InputTokens: r.input, CacheReadTokens: r.read, CacheCreationTokens: r.creation}
		r.billing = billing
	}
	writeUnbilled := !r.billing && !r.claimed
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

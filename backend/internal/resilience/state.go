package resilience

import (
	"context"
	"sync"
)

// RequestState is the mutable per-request resilience state. Gateway routes
// attach it once (middleware); scheduling, forwarding and billing read and
// update it through the request context. All methods are nil-safe so code
// paths without a state (internal calls, tests) keep the 0.3.0 behaviour.
type RequestState struct {
	mu sync.Mutex

	budget *Budget

	// groupID is the API key's group (0 when ungrouped or unknown).
	groupID int64

	// d tracks client delivery and disconnects (design §8).
	d delivery

	// stickyOwner is the account the session was bound to when selection
	// started; keptSticky is set when that binding must survive this request
	// because its account is only soft-penalised (design §4.2).
	stickyOwner int64
	keptSticky  int64

	// lastResort is the account chosen by last-resort soft selection.
	lastResort int64

	// edge is the CDN edge marker of the request ("" when direct).
	edge string

	// originGroupID is set when the request was moved to an availability
	// fallback group (design §5).
	originGroupID int64
	fallbackUsed  bool

	// configError is set when selection failed for a configuration reason
	// (channel pricing restriction, Claude-Code-only group) so the response
	// carries x-should-retry: false (design §9.1).
	configError bool

	// session identifies the request's sticky session for failure streaks.
	sessionGroupID int64
	sessionHash    string
	sessionRec     SessionRecorder

	// turn identifies the conversation for the failed-turn cap (design
	// §9.7); turnFailed is set once the request counted as a failure.
	turnGroupID int64
	turnModel   string
	turnSession string
	turnNoted   bool
	turnFailed  bool
}

// SessionRecorder records attempt outcomes of a sticky session. The service
// layer provides a scheduler-specific implementation after selection.
type SessionRecorder interface {
	Failure(ctx context.Context, groupID int64, sessionHash string, accountID int64, c Category)
	Success(ctx context.Context, groupID int64, sessionHash string, accountID int64)
}

// SetSession records the sticky session of the request and its recorder.
func (s *RequestState) SetSession(groupID int64, sessionHash string, rec SessionRecorder) {
	if s == nil || sessionHash == "" || rec == nil {
		return
	}
	s.mu.Lock()
	s.sessionGroupID, s.sessionHash, s.sessionRec = groupID, sessionHash, rec
	s.mu.Unlock()
}

// NoteTurn records the conversation the request belongs to. Selection calls
// it on every attempt, including ones that find no account.
func (s *RequestState) NoteTurn(groupID int64, model, sessionHash string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.turnGroupID, s.turnModel, s.turnSession, s.turnNoted = groupID, model, sessionHash, true
	s.mu.Unlock()
}

// TurnKey returns the conversation key, or "" before selection or without
// an API key.
func (s *RequestState) TurnKey() string {
	if s == nil {
		return ""
	}
	s.d.mu.Lock()
	apiKeyID := s.d.apiKeyID
	s.d.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.turnNoted || apiKeyID <= 0 {
		return ""
	}
	return TurnKey(apiKeyID, s.turnGroupID, s.turnModel, s.turnSession)
}

// MarkTurnFailed records that the request counted as a failed turn attempt,
// so a 200 that later carries an in-stream failure does not reset the streak.
func (s *RequestState) MarkTurnFailed() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.turnFailed = true
	s.mu.Unlock()
}

// TurnFailed reports whether MarkTurnFailed was called.
func (s *RequestState) TurnFailed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnFailed
}

func (s *RequestState) session() (int64, string, SessionRecorder) {
	if s == nil {
		return 0, "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionGroupID, s.sessionHash, s.sessionRec
}

// SessionFailure reports a failed attempt of the session on an account.
func (s *RequestState) SessionFailure(ctx context.Context, accountID int64, c Category) {
	g, h, rec := s.session()
	if rec == nil || accountID <= 0 {
		return
	}
	rec.Failure(ctx, g, h, accountID, c)
}

// SessionSuccess reports a successful request of the session on an account.
func (s *RequestState) SessionSuccess(ctx context.Context, accountID int64) {
	g, h, rec := s.session()
	if rec == nil || accountID <= 0 {
		return
	}
	rec.Success(ctx, g, h, accountID)
}

type stateKey struct{}

// NewRequestState creates an empty state.
func NewRequestState() *RequestState { return &RequestState{} }

// WithRequestState attaches st to ctx.
func WithRequestState(ctx context.Context, st *RequestState) context.Context {
	if st == nil {
		return ctx
	}
	return context.WithValue(ctx, stateKey{}, st)
}

// StateFrom returns the state of ctx, or nil.
func StateFrom(ctx context.Context) *RequestState {
	if ctx == nil {
		return nil
	}
	st, _ := ctx.Value(stateKey{}).(*RequestState)
	return st
}

// SetGroup records the API key's group.
func (s *RequestState) SetGroup(groupID int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.groupID = groupID
	s.mu.Unlock()
}

// GroupID returns the API key's group (0 when unknown).
func (s *RequestState) GroupID() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupID
}

// SetBudget stores the request budget.
func (s *RequestState) SetBudget(b *Budget) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.budget = b
	s.mu.Unlock()
}

// Budget returns the request budget (nil when none).
func (s *RequestState) Budget() *Budget {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget
}

// NoteStickyOwner records the session owner seen at selection start.
func (s *RequestState) NoteStickyOwner(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	s.stickyOwner = accountID
	s.mu.Unlock()
}

// StickyOwner returns the recorded session owner.
func (s *RequestState) StickyOwner() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stickyOwner
}

// KeepSticky marks the binding to accountID as kept for this request.
func (s *RequestState) KeepSticky(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	s.keptSticky = accountID
	s.mu.Unlock()
}

// KeptSticky returns the account whose binding must not be replaced.
func (s *RequestState) KeptSticky() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keptSticky
}

// SetLastResort records last-resort soft selection.
func (s *RequestState) SetLastResort(accountID int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastResort = accountID
	s.mu.Unlock()
}

// LastResort returns the last-resort account (0 when none).
func (s *RequestState) LastResort() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastResort
}

// SetEdge records the CDN edge marker.
func (s *RequestState) SetEdge(edge string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.edge = edge
	s.mu.Unlock()
}

// Edge returns the CDN edge marker ("" when direct).
func (s *RequestState) Edge() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edge
}

// MarkFallback records a move to an availability fallback group. It
// reports false when the request already used its single fallback.
func (s *RequestState) MarkFallback(originGroupID int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallbackUsed {
		return false
	}
	s.fallbackUsed = true
	s.originGroupID = originGroupID
	return true
}

// OriginGroupID returns the group the request was moved away from, or 0.
func (s *RequestState) OriginGroupID() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.originGroupID
}

// MarkConfigError records a configuration-caused selection failure.
func (s *RequestState) MarkConfigError() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.configError = true
	s.mu.Unlock()
}

// ConfigError reports a configuration-caused selection failure.
func (s *RequestState) ConfigError() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configError
}

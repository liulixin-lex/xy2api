package resilience

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// RequestState is the mutable per-request resilience state. Gateway routes
// attach it once (middleware); scheduling, forwarding and billing read and
// update it through the request context. All methods are nil-safe so code
// paths without a state (internal calls, tests) keep the 0.3.0 behaviour.
type RequestState struct {
	mu sync.Mutex

	budget *Budget

	// retryAdvice is shared by the handler and service terminal writers.
	retryAdvice         RetryAdvice
	retryAdviceSet      bool
	retryAdviceProvider RetryAdviceProvider

	// prelude is collected by the stream state machine under mu.
	prelude preludeTrace

	// requestID is generated locally, independent of client correlation IDs.
	requestID   string
	firstOutput time.Time
	// firstOutputNoted is set once firstOutput is; later writes skip mu.
	firstOutputNoted atomic.Bool

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
	// lastResortSessionLimited is set when last resort found candidates but
	// each refused the request's new session (session limit); the result
	// then depends on the request.
	lastResortSessionLimited bool

	// edge is the CDN edge marker of the request ("" when direct).
	edge string

	// originGroupID is set when the request was moved to an availability
	// fallback group (design §5).
	originGroupID int64
	fallbackUsed  bool
	// fallbackOrigin is the API key as authenticated, with the group the
	// request was made for; the service package stores and reads it.
	fallbackOrigin any
	// fallbackForcedFast is set when only the serving fallback group's
	// forced Fast made the request priority.
	fallbackForcedFast bool

	// configError is set when selection failed for a configuration reason
	// (channel pricing restriction, Claude-Code-only group) so the response
	// carries x-should-retry: false (design §9.1).
	configError bool

	// groupNoAccount is set when selection found no account for the whole
	// group and model, not only for this request (negative cache, fast fail).
	groupNoAccount bool

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
	// keptBusy is set when keptSticky holds for the current attempt only:
	// L-07 kept a busy owner (cache DESIGN §6.1A.2 step 5). The next
	// selection releases it, so a later attempt may bind its account.
	keptBusy bool

	// avoided is the session's avoid set read at selection (design §4.2).
	avoided map[int64]struct{}
	// failedAccounts are accounts with a failed attempt in this request.
	failedAccounts map[int64]struct{}
	// streamFailures maps an account to its latest failed attempt that the
	// stream handler already reported (design §7.6).
	streamFailures map[int64]any

	// l07 is the service-owned L-07 record of the request; it is stored only
	// when the routing policy armed the request.
	l07 atomic.Value
}

// preludeTrace is storage shared with prelude_trace.go. Its accessors live
// with the stream state machine so attempt resets follow stream staging.
type preludeTrace struct {
	createdAt             time.Time
	releaseEvent          string
	releaseAfterCreatedMS int64
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

// SetAvoided records the session's avoid set so schedulers can tell an
// account the session moved away from from one that failed in this request.
func (s *RequestState) SetAvoided(ids map[int64]struct{}) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.avoided = ids
	s.mu.Unlock()
}

// AddAvoided adds an account the session started avoiding during this
// request (a failure streak). The set is copied, never changed in place.
func (s *RequestState) AddAvoided(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[int64]struct{}, len(s.avoided)+1)
	for id := range s.avoided {
		next[id] = struct{}{}
	}
	next[accountID] = struct{}{}
	s.avoided = next
}

// Avoided reports whether accountID is in the session's avoid set.
func (s *RequestState) Avoided(accountID int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.avoided[accountID]
	return ok
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

// TurnKey returns the conversation key, or "" before selection, without an
// API key or without a session.
func (s *RequestState) TurnKey() string {
	if s == nil {
		return ""
	}
	s.d.mu.Lock()
	apiKeyID := s.d.apiKeyID
	s.d.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Requests without a session are independent of each other; counting
	// them together would end unrelated requests as one failed turn.
	if !s.turnNoted || apiKeyID <= 0 || s.turnSession == "" {
		return ""
	}
	return TurnKey(apiKeyID, s.turnGroupID, s.turnModel, s.turnSession)
}

// Model returns the requested model the request selected for ("" before
// the first selection).
func (s *RequestState) Model() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnModel
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

// MarkAccountFailed records that an upstream attempt on the account failed
// during this request, including streams that broke after output.
func (s *RequestState) MarkAccountFailed(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	if s.failedAccounts == nil {
		s.failedAccounts = map[int64]struct{}{}
	}
	s.failedAccounts[accountID] = struct{}{}
	s.mu.Unlock()
}

// AccountFailed reports whether MarkAccountFailed was called for the account.
// Usage recorded for such an account (partial usage of a broken stream) is
// no evidence that the account works.
func (s *RequestState) AccountFailed(accountID int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.failedAccounts[accountID]
	return ok
}

// ReportStreamFailure records that the stream handler itself reports a
// failed upstream attempt on the account (a failure after output, which no
// failover loop sees) and reports whether this is the first report of that
// attempt. attempt identifies the attempt and must be comparable (a pointer
// such as its *http.Response, or a WebSocket turn number): one attempt can
// show several failure signals (an error event, then response.failed, then
// a missing terminal event) but counts once. Without a state every report
// is new.
func (s *RequestState) ReportStreamFailure(accountID int64, attempt any) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.streamFailures[accountID]; ok && prev == attempt {
		return false
	}
	if s.streamFailures == nil {
		s.streamFailures = map[int64]any{}
	}
	s.streamFailures[accountID] = attempt
	return true
}

// StreamFailureReported reports whether the stream handler reported a
// failed attempt on the account in this request; a failover loop that later
// sees the same attempt as its error must not count it again.
func (s *RequestState) StreamFailureReported(accountID int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.streamFailures[accountID]
	return ok
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
func NewRequestState() *RequestState { return &RequestState{requestID: uuid.NewString()} }

// GatewayRequestID identifies this gateway request, even if a client reuses
// its X-Request-ID. It does not replace any billing or correlation key.
func (s *RequestState) GatewayRequestID() string {
	if s == nil {
		return ""
	}
	return s.requestID
}

// NoteFirstOutput records the first successful content write, once. Opening
// metadata and heartbeats must not call it.
func (s *RequestState) NoteFirstOutput(at time.Time) {
	if s == nil || at.IsZero() || s.firstOutputNoted.Load() {
		return
	}
	s.mu.Lock()
	if s.firstOutput.IsZero() {
		s.firstOutput = at
		s.firstOutputNoted.Store(true)
	}
	s.mu.Unlock()
}

// FirstOutputNoted reports, without the state lock, whether the first
// content write was recorded.
func (s *RequestState) FirstOutputNoted() bool {
	return s != nil && s.firstOutputNoted.Load()
}

// MSSinceFirstOutput is zero before output or for a clock before that write.
func (s *RequestState) MSSinceFirstOutput(now time.Time) int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	at := s.firstOutput
	s.mu.Unlock()
	if at.IsZero() {
		return 0
	}
	return max(0, now.Sub(at).Milliseconds())
}

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
	s.keptBusy = false
	s.mu.Unlock()
}

// KeepStickyForAttempt keeps the binding to accountID for the current
// attempt only (L-07). A binding already kept for the request wins; the
// result reports whether accountID is kept.
func (s *RequestState) KeepStickyForAttempt(accountID int64) bool {
	if s == nil || accountID <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keptSticky != 0 {
		return s.keptSticky == accountID
	}
	s.keptSticky, s.keptBusy = accountID, true
	return true
}

// ReleaseAttemptSticky drops a binding kept only for the previous attempt.
func (s *RequestState) ReleaseAttemptSticky() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.keptBusy {
		s.keptSticky, s.keptBusy = 0, false
	}
	s.mu.Unlock()
}

// SelectionNoted reports whether a selection of this request already ended.
func (s *RequestState) SelectionNoted() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnNoted
}

// SetL07 stores the service-owned L-07 record; v must always be the same
// pointer type.
func (s *RequestState) SetL07(v any) {
	if s == nil || v == nil {
		return
	}
	s.l07.Store(v)
}

// L07 returns the L-07 record, or nil when the request was not armed.
func (s *RequestState) L07() any {
	if s == nil {
		return nil
	}
	return s.l07.Load()
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

// SetFallbackOrigin keeps the caller's view of a request moved to an
// availability fallback group: the API key as authenticated.
func (s *RequestState) SetFallbackOrigin(v any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallbackOrigin = v
}

// FallbackOrigin returns the value stored by SetFallbackOrigin, or nil.
func (s *RequestState) FallbackOrigin() any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fallbackOrigin
}

// SetFallbackForcedFast records whether only the serving fallback group's
// forced Fast made the latest request (or WebSocket turn) priority.
func (s *RequestState) SetFallbackForcedFast(v bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallbackForcedFast = v
}

// FallbackForcedFast reports SetFallbackForcedFast.
func (s *RequestState) FallbackForcedFast() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fallbackForcedFast
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

// MarkGroupNoAccount records that selection found no account for the whole
// group and model: the result does not depend on this request's session,
// so it may answer other requests (design §9.2, §9.3).
func (s *RequestState) MarkGroupNoAccount() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.groupNoAccount = true
	s.mu.Unlock()
}

// GroupNoAccount reports a group-level no-account result.
func (s *RequestState) GroupNoAccount() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupNoAccount
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

// MarkLastResortSessionLimited records that last resort had candidates that
// only refused the request's new session (session limit).
func (s *RequestState) MarkLastResortSessionLimited() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastResortSessionLimited = true
	s.mu.Unlock()
}

// LastResortSessionLimited reports MarkLastResortSessionLimited.
func (s *RequestState) LastResortSessionLimited() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastResortSessionLimited
}

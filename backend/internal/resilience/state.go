package resilience

import (
	"context"
	"strings"
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

	timing            timingTrace
	bodyFirstRead     time.Time
	bodyReceived      time.Time
	bodyReceiptBasis  string
	bodyReceiptRepeat int64
	bodyBytes         int64
	budget            *Budget
	budgetBaseSeconds int64
	budgetBaseSet     bool
	stateSwitch       int

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
	attemptProfile   AttemptProfile
	// selectionEligibility holds a scheduler-owned, typed request gate. The
	// generic accessors keep provider account types out of this package.
	selectionEligibility any

	// groupID is the API key's group (0 when ungrouped or unknown).
	groupID int64

	// d tracks client delivery and disconnects (design §8).
	d delivery

	// stickyOwner is the account the session was bound to when selection
	// started; keptSticky is set when that binding must survive this request
	// because its account is only soft-penalised (design §4.2).
	stickyOwner                int64
	keptSticky                 int64
	stickyBypassReason         string
	stickyBypassLogged         bool
	stickyBypassChangesBinding bool
	// stateRepairAvoidedAccount is diagnostic-only evidence from this request's
	// M3 switch. Unlike avoided, it never imports historical session avoidance.
	stateRepairAvoidedAccount int64

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

	// avoided is the session's avoid set read at selection (design §4.2).
	avoided map[int64]struct{}
	// failedAccounts are accounts with a failed attempt in this request.
	failedAccounts map[int64]struct{}
	// streamFailures maps an account to its latest failed attempt that the
	// stream handler already reported (design §7.6).
	streamFailures map[int64]any

	// opsUpstreamEventSequence mirrors append-only OpsUpstreamErrorsKey
	// entries for this request. Send boundaries use this sequence instead of
	// timestamps so retries in the same millisecond remain distinguishable.
	opsUpstreamEventSequence  uint64
	opsUpstreamSendBoundaries map[int64]uint64
	postOutputFailureFacts    []PostOutputFailureFact
}

// AllowStateSwitch grants one request-scoped state-repair account switch,
// including when the normal Budget is disabled.
func (s *RequestState) AllowStateSwitch() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateSwitch >= 1 {
		return false
	}
	s.stateSwitch++
	return true
}

// PostOutputFailureFact is a request-local snapshot of a stream failure that
// the resilience layer confirmed after client-visible output.
type PostOutputFailureFact struct {
	AccountID       int64
	AccountName     string
	Platform        string
	ProxyID         *int64
	ProxyName       string
	AtUnixMs        int64
	Reason          string
	Category        Category
	Message         string
	SendBoundary    uint64
	HasSendBoundary bool
}

// preludeTrace is storage shared with prelude_trace.go. Its accessors live
// with the stream state machine so attempt resets follow stream staging.
type preludeTrace struct {
	createdAt             time.Time
	releaseEvent          string
	releaseAfterCreatedMS int64
	schema                PreludeReleaseSchema
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

// RecordOpsUpstreamEvent advances the append-only upstream event sequence.
func (s *RequestState) RecordOpsUpstreamEvent() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	s.opsUpstreamEventSequence++
	sequence := s.opsUpstreamEventSequence
	s.mu.Unlock()
	return sequence
}

// MarkOpsUpstreamSendBoundary snapshots the current upstream event sequence
// immediately before an HTTP send for accountID.
func (s *RequestState) MarkOpsUpstreamSendBoundary(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	if s.opsUpstreamSendBoundaries == nil {
		s.opsUpstreamSendBoundaries = make(map[int64]uint64)
	}
	s.opsUpstreamSendBoundaries[accountID] = s.opsUpstreamEventSequence
	s.mu.Unlock()
}

// OpsUpstreamSendBoundary returns the latest send boundary for accountID.
func (s *RequestState) OpsUpstreamSendBoundary(accountID int64) (uint64, bool) {
	if s == nil || accountID <= 0 {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sequence, ok := s.opsUpstreamSendBoundaries[accountID]
	return sequence, ok
}

// OpsUpstreamEventSequence returns the number of upstream events appended
// during this request (or WebSocket turn).
func (s *RequestState) OpsUpstreamEventSequence() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opsUpstreamEventSequence
}

// RecordPostOutputFailureFact stores the latest send boundary with a confirmed
// post-output failure snapshot.
func (s *RequestState) RecordPostOutputFailureFact(fact PostOutputFailureFact) {
	if s == nil || fact.AccountID <= 0 {
		return
	}
	s.mu.Lock()
	if boundary, ok := s.opsUpstreamSendBoundaries[fact.AccountID]; ok {
		fact.SendBoundary = boundary
		fact.HasSendBoundary = true
	}
	if fact.ProxyID != nil {
		proxyID := *fact.ProxyID
		fact.ProxyID = &proxyID
	}
	s.postOutputFailureFacts = append(s.postOutputFailureFacts, fact)
	s.mu.Unlock()
}

// TakePostOutputFailureFacts returns and clears the pending request-local
// facts so repeated logging paths cannot append them twice.
func (s *RequestState) TakePostOutputFailureFacts() []PostOutputFailureFact {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	facts := append([]PostOutputFailureFact(nil), s.postOutputFailureFacts...)
	s.postOutputFailureFacts = nil
	s.mu.Unlock()
	return facts
}

// ResetPostOutputOpsTracking clears only the supplement's request-local state.
func (s *RequestState) ResetPostOutputOpsTracking() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.opsUpstreamEventSequence = 0
	s.opsUpstreamSendBoundaries = nil
	s.postOutputFailureFacts = nil
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

// SessionIdentity returns the actual group and sticky session after selection.
func (s *RequestState) SessionIdentity() (int64, string) {
	group, session, _ := s.session()
	return group, session
}

// SessionFailure reports a failed attempt of the session on an account.
func (s *RequestState) SessionFailure(ctx context.Context, accountID int64, c Category) {
	g, h, rec := s.session()
	if rec == nil || accountID <= 0 {
		return
	}
	rec.Failure(ctx, g, h, accountID, c)
}

type postOutputSessionFailureKey struct{}

// SessionFailureAfterOutput marks only this recorder invocation as a post-output
// failure. Ordinary failure callers keep the existing streak semantics.
func (s *RequestState) SessionFailureAfterOutput(ctx context.Context, accountID int64, c Category) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.SessionFailure(context.WithValue(ctx, postOutputSessionFailureKey{}, true), accountID, c)
}

// IsPostOutputSessionFailure reports the explicit signal sent to the recorder.
func IsPostOutputSessionFailure(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	postOutput, _ := ctx.Value(postOutputSessionFailureKey{}).(bool)
	return postOutput
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

// SetSelectionEligibility replaces the request's gate on every selection.
func SetSelectionEligibility[T any](s *RequestState, gate T) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.selectionEligibility = gate
	s.mu.Unlock()
}

// SelectionEligibility returns the gate only for the caller's account type.
func SelectionEligibility[T any](s *RequestState) (gate T) {
	if s == nil {
		return gate
	}
	s.mu.Lock()
	gate, _ = s.selectionEligibility.(T)
	s.mu.Unlock()
	return gate
}

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
	var baseSeconds int64
	baseSet := false
	if b != nil {
		b.mu.Lock()
		baseSeconds = int64(b.base / time.Second)
		baseSet = true
		b.mu.Unlock()
	}
	s.mu.Lock()
	s.budget = b
	s.budgetBaseSeconds, s.budgetBaseSet = baseSeconds, baseSet
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

// NoteStateRepairAvoidance records this request's state-repair switch only.
func (s *RequestState) NoteStateRepairAvoidance(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	s.stateRepairAvoidedAccount = accountID
	s.mu.Unlock()
}

// StateRepairAvoided reports diagnostic evidence, not a scheduling exclusion.
func (s *RequestState) StateRepairAvoided(accountID int64) bool {
	if s == nil || accountID <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateRepairAvoidedAccount == accountID
}

// NoteStickyBypassReason records the first scheduler reason that caused the
// existing sticky owner to be bypassed. Later checks must not overwrite it.
func (s *RequestState) NoteStickyBypassReason(reason string) {
	if s == nil || strings.TrimSpace(reason) == "" {
		return
	}
	s.mu.Lock()
	if s.stickyBypassReason == "" {
		s.stickyBypassReason = strings.TrimSpace(reason)
	}
	s.mu.Unlock()
}

// StickyBypassReason returns the first recorded sticky bypass reason.
func (s *RequestState) StickyBypassReason() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stickyBypassReason
}

// ResetStickyBypassReason starts a fresh scheduler pass. A request can retry
// selection after an upstream failure; the next pass must report its own first
// reason instead of carrying over the previous pass.
func (s *RequestState) ResetStickyBypassReason() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stickyBypassReason = ""
	s.stickyBypassChangesBinding = false
	s.mu.Unlock()
}

// NoteStickyBypassBindingChange marks a confirmed deletion or migration by
// the current selection, independently of its first recorded reason.
func (s *RequestState) NoteStickyBypassBindingChange() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stickyBypassChangesBinding = true
	s.mu.Unlock()
}

func (s *RequestState) StickyBypassChangesBinding() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stickyBypassChangesBinding
}

// ClaimStickyBypassLog allows at most one sticky bypass event per request.
func (s *RequestState) ClaimStickyBypassLog() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stickyBypassLogged {
		return false
	}
	s.stickyBypassLogged = true
	return true
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

// ClearKeptStickyIfOwner releases a previously kept sticky owner when it still
// matches owner. A concurrent request that kept another owner is preserved.
func (s *RequestState) ClearKeptStickyIfOwner(owner int64) bool {
	if s == nil || owner <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keptSticky != owner {
		return false
	}
	s.keptSticky = 0
	return true
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

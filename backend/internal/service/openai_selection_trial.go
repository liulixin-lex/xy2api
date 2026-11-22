package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type selectionTrialContextKey struct{}

// One selection owns its Room cache and temporary reservations. Session members
// become durable only when the binding commits; request members never do.
type selectionTrial struct {
	service       *OpenAIGatewayService
	leases        SchedTrialLeases
	request       OpenAIAccountScheduleRequest
	owner         int64
	manual        bool
	member        string
	requestMember string
	rooms         map[SchedAccountModel]selectionTrialRoom
	selected      *selectionTrialReservation
}

type selectionTrialRoom struct{ available, known bool }

type selectionTrialReservation struct {
	leases         SchedTrialLeases
	account, group int64
	model, member  string
	session        string
	now            func() time.Time
	request        bool
	generation     string
	reader         SchedTrialRoundReader
	lines          [3]time.Time
	retained       atomic.Bool
	once           sync.Once
}

// The selection snapshot identifies the round observed at reservation time;
// its separate reader checks the authoritative rows only at binding commit.
type trialCheckingView struct {
	schedquality.View
	SchedTrialRoundReader
}

func trialLineDeadlines(state schedquality.StateView) (deadlines [3]time.Time) {
	for i, line := range [...]schedquality.LineView{state.TTFO, state.Error, state.Cache} {
		if line.Trial {
			deadlines[i] = line.TrialUntil
		}
	}
	return deadlines
}

func (r *selectionTrialReservation) rememberRound(view schedquality.View, generation string) {
	r.generation = generation
	r.reader, _ = view.(SchedTrialRoundReader)
	r.lines = trialLineDeadlines(view.StateOf(r.group, r.account, r.model))
}

func (r *selectionTrialReservation) sameTrial(ctx context.Context, now time.Time) (bool, error) {
	if r.reader == nil {
		return false, errors.New("trial round reader unavailable")
	}
	state, err := r.reader.ReadTrialRound(ctx, r.account, r.model, r.group)
	if err != nil || state.Placement(true, now) != schedquality.PlaceTrial {
		return false, err
	}
	for i, until := range trialLineDeadlines(state) {
		if !until.IsZero() && r.lines[i].Equal(until) {
			return true, nil
		}
	}
	return false, nil
}

func (r *selectionTrialReservation) release(ctx context.Context) {
	if r == nil || r.retained.Load() {
		return
	}
	r.once.Do(func() { releaseSelectionTrial(ctx, r.leases, r.account, r.model, r.group, r.member) })
}

func releaseSelectionTrial(ctx context.Context, leases SchedTrialLeases, account int64, model string, group int64, member string) {
	if leases == nil || account == 0 {
		return
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if err := leases.Release(stateCtx, account, model, group, member); err != nil {
		slog.WarnContext(ctx, "sched_quality.trial_release_failed", "component", "sched_quality", "account_id", account, "group_id", group, "error", err)
	}
}

func (s *OpenAIGatewayService) selectionTrialLeases() SchedTrialLeases {
	if s.selectionBoard == nil {
		return nil
	}
	return s.selectionBoard.leases
}

func (s *OpenAIGatewayService) newSelectionTrial(ctx context.Context, req OpenAIAccountScheduleRequest, owner int64) *selectionTrial {
	id := resilience.StateFrom(ctx).GatewayRequestID()
	if id == "" {
		id = resilience.NewRequestState().GatewayRequestID()
	}
	requestMember := TrialLeaseRequestMember(id)
	member := TrialLeaseSessionMember(req.SessionHash)
	if req.SessionHash == "" {
		member = requestMember
	}
	return &selectionTrial{service: s, leases: s.selectionTrialLeases(), request: req, owner: owner, manual: openAISelectionManualOnly(ctx), member: member, requestMember: requestMember, rooms: make(map[SchedAccountModel]selectionTrialRoom)}
}

func (t *selectionTrial) room(ctx context.Context, account int64, model string) bool {
	key := SchedAccountModel{AccountID: account, Model: model}
	if room, ok := t.rooms[key]; ok {
		return room.available
	}
	var result selectionTrialRoom
	if t.leases != nil {
		room, err := t.leases.Room(ctx, account, model, derefGroupID(t.request.GroupID), t.memberFor(ctx, account), t.service.selectionNow())
		result = selectionTrialRoom{available: room && err == nil, known: err == nil}
		if err != nil {
			slog.WarnContext(ctx, "sched_quality.trial_room_failed", "component", "sched_quality", "account_id", account, "error", err)
		}
	}
	t.rooms[key] = result
	return result.available
}

func (t *selectionTrial) isTrial(account *Account) bool {
	gid := derefGroupID(t.request.GroupID)
	return t.isTrialIn(t.service.selectionView(), gid, account, t.service.selectionNow())
}

func (t *selectionTrial) isTrialIn(view schedquality.View, gid int64, account *Account, now time.Time) bool {
	return account.Platform == PlatformOpenAI && !t.manual &&
		selectionState(view, gid, account, t.request.RequestedModel).Placement(selectionAuto(view, gid, now), now) == schedquality.PlaceTrial
}

// Borrowing keeps the old binding. Trial targets are a last choice and use a
// request member, so finishing this request cannot create a trial session.
func (t *selectionTrial) borrowing(ctx context.Context) bool {
	if t.manual || t.owner == 0 || t.request.SessionHash == "" {
		return false
	}
	if resilience.StateFrom(ctx).Avoided(t.owner) && !preserveOpenAIRejectionBinding(ctx, t.request.SessionHash) && !preserveOpenAIGuardianParentBinding(ctx, t.request.SessionHash) {
		return false
	}
	change := pendingOpenAIStickyChange(ctx)
	if change == nil || change.groupID != derefGroupID(t.request.GroupID) || change.trialSessionKey() != t.request.SessionHash {
		return true
	}
	return !change.ownerReleased && (change.target == 0 || change.done)
}

func (t *selectionTrial) memberFor(ctx context.Context, account int64) string {
	if account != t.owner {
		return t.requestMember
	}
	return t.member
}

func (t *selectionTrial) touch(ctx context.Context, account *Account) {
	now := t.service.selectionNow()
	if t.leases == nil || t.request.SessionHash == "" || !t.isTrialIn(t.service.selectionView(), derefGroupID(t.request.GroupID), account, now) {
		return
	}
	if err := t.leases.Touch(ctx, account.ID, OpenAIAttemptModel(account, t.request.RequestedModel), derefGroupID(t.request.GroupID), t.member, now); err != nil {
		slog.WarnContext(ctx, "sched_quality.trial_touch_failed", "component", "sched_quality", "account_id", account.ID, "error", err)
	}
}

// reserve returns false after a lost race. The caller reorders with room=false
// and may try this account again in tier 1 after every remaining tier 0 account.
func (t *selectionTrial) reserve(ctx context.Context, account *Account, full bool) (*selectionTrialReservation, bool) {
	observedAt := t.service.selectionNow()
	view := t.service.selectionView()
	if !t.isTrialIn(view, derefGroupID(t.request.GroupID), account, observedAt) {
		return nil, true
	}
	if t.owner == account.ID && t.request.SessionHash != "" {
		t.touch(ctx, account)
		return nil, true
	}
	model, gid := OpenAIAttemptModel(account, t.request.RequestedModel), derefGroupID(t.request.GroupID)
	member := t.memberFor(ctx, account.ID)
	known := false
	if t.leases != nil {
		acquired, generation, err := t.leases.Acquire(ctx, account.ID, model, gid, member, observedAt)
		known = err == nil
		if err == nil && acquired {
			r := &selectionTrialReservation{leases: t.leases, account: account.ID, group: gid, model: model, member: member, request: t.request.SessionHash == "" || t.borrowing(ctx), session: TrialLeaseSessionMember(t.request.SessionHash), now: t.service.selectionNow}
			guardSelectionReservation(ctx, r)
			r.rememberRound(view, generation)
			return r, true
		}
		if err != nil {
			slog.WarnContext(ctx, "sched_quality.trial_acquire_failed", "component", "sched_quality", "account_id", account.ID, "error", err)
		}
	}
	key := SchedAccountModel{AccountID: account.ID, Model: model}
	t.rooms[key] = selectionTrialRoom{known: known}
	return nil, full
}

func (t *selectionTrial) finish(ctx context.Context, result *AccountSelectionResult) {
	reservation := t.selected
	if result == nil || result.Account == nil {
		reservation.release(ctx)
		return
	}
	if result.Account.ID != t.owner && t.isTrial(result.Account) && t.borrowing(ctx) {
		result.StickyCapacitySpillover = true
	}
	if result.Account.ID == t.owner {
		t.touch(ctx, result.Account)
	}
	if reservation == nil {
		return
	}
	if result.constrainedImmediate() {
		result.vetoTrial = reservation
	}
	change := pendingOpenAIStickyChange(ctx)
	if !reservation.request && t.request.SessionHash != "" && change != nil && change.groupID == derefGroupID(t.request.GroupID) && change.trialSessionKey() == t.request.SessionHash && change.target == result.Account.ID {
		result.rememberVetoPlan(change)
		change.trial = reservation
		change.leases = t.leases
	} else if !reservation.request && t.request.SessionHash != "" {
		if owner, err := t.service.getStickySessionAccountID(ctx, t.request.GroupID, t.request.SessionHash); err == nil && owner == result.Account.ID {
			reservation.promote(ctx)
		}
	}
	result.ReleaseFunc = releaseSelectionTrialWithSlot(ctx, change, reservation, result.ReleaseFunc)
}

// CombineSelectionRelease preserves the selection's cleanup when a handler
// obtains the concurrency slot later through a WaitPlan.
func CombineSelectionRelease(selection *AccountSelectionResult, slotRelease func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if slotRelease != nil {
				slotRelease()
			}
			if selection != nil && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

// Completion holds change.mu across the binding CAS. Cleanup waits for that
// decision so a concurrent cancellation cannot delete a newly committed lease.
func releaseSelectionTrialWithSlot(ctx context.Context, change *openAIStickyChange, reservation *selectionTrialReservation, slotRelease func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if slotRelease != nil {
				slotRelease()
			}
			if change != nil && change.trial == reservation && reservation != nil {
				change.mu.Lock()
				defer change.mu.Unlock()
				// HTTP releases its concurrency slot before the forwarding outcome
				// commits the binding. Completion owns this lease until it is done.
				// A selection that never starts sending expires after TrialLeaseIdle.
				if change.done {
					change.releaseTrial(ctx)
				}
				return
			}
			reservation.release(ctx)
		})
	}
}

// Cleanup needs the account's mapping even if the account is no longer eligible.
func (s *OpenAIGatewayService) selectionTrialOwner(ctx context.Context, id int64) *Account {
	if s.accountRepo != nil {
		if account, err := s.accountRepo.GetByID(ctx, id); err == nil && account != nil {
			return account
		}
	}
	if s.schedulerSnapshot != nil {
		if account, err := s.schedulerSnapshot.GetAccount(ctx, id); err == nil {
			return account
		}
	}
	return nil
}

func (s *OpenAIGatewayService) releaseDeletedTrialOwner(ctx context.Context, groupID *int64, session string, owner int64) {
	if s.selectionTrialLeases() == nil {
		return
	}
	req, ok := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest)
	if !ok || req.RequestedModel == "" {
		return
	} // Missing model metadata: idle expiry.
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if account := s.selectionTrialOwner(stateCtx, owner); account != nil {
		releaseSelectionTrial(stateCtx, s.selectionTrialLeases(), owner, OpenAIAttemptModel(account, req.RequestedModel), derefGroupID(groupID), TrialLeaseSessionMember(session))
	}
}

func (r *selectionTrialReservation) promote(ctx context.Context) {
	if r == nil || r.request || r.retained.Load() {
		return
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	same, err := r.sameTrial(stateCtx, now)
	if err != nil || !same {
		if err != nil {
			slog.WarnContext(ctx, "sched_quality.trial_verify_failed", "account_id", r.account, "model", r.model, "group_id", r.group, "error", err)
		}
		r.release(ctx)
		return
	}
	if err := r.leases.Promote(stateCtx, r.account, r.model, r.group, r.member, r.session, r.generation, now); err != nil {
		if errors.Is(err, ErrTrialGenerationChanged) {
			slog.InfoContext(ctx, "sched_quality.trial_generation_changed", "component", "sched_quality", "account_id", r.account, "group_id", r.group, "model", r.model)
		} else {
			slog.WarnContext(ctx, "sched_quality.trial_promote_failed", "component", "sched_quality", "account_id", r.account, "error", err)
		}
		// The CAS is already committed. Release only our request member: if Redis
		// executed Promote but lost its reply, the session member remains intact.
		r.release(ctx)
		return
	}
	r.retained.Store(true)
}

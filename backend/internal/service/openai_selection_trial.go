package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type selectionTrialContextKey struct{}

// One selection owns its Room cache and temporary reservations. Session members
// become durable only when the binding commits; request members never do.
type selectionTrial struct {
	service  *OpenAIGatewayService
	leases   SchedTrialLeases
	request  OpenAIAccountScheduleRequest
	owner    int64
	manual   bool
	member   string
	rooms    map[SchedAccountModel]bool
	selected *selectionTrialReservation
}

type selectionTrialReservation struct {
	leases         SchedTrialLeases
	account, group int64
	model, member  string
	retained       atomic.Bool
	once           sync.Once
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
	member := TrialLeaseSessionMember(req.SessionHash)
	if req.SessionHash == "" {
		id := resilience.StateFrom(ctx).GatewayRequestID()
		if id == "" {
			id = resilience.NewRequestState().GatewayRequestID()
		}
		member = TrialLeaseRequestMember(id)
	}
	return &selectionTrial{service: s, leases: s.selectionTrialLeases(), request: req, owner: owner, manual: openAISelectionManualOnly(ctx), member: member, rooms: make(map[SchedAccountModel]bool)}
}

func (t *selectionTrial) room(ctx context.Context, account int64, model string) bool {
	key := SchedAccountModel{AccountID: account, Model: model}
	if room, ok := t.rooms[key]; ok {
		return room
	}
	room := false
	if t.leases != nil {
		var err error
		room, err = t.leases.Room(ctx, account, model, derefGroupID(t.request.GroupID), t.member, t.service.selectionNow())
		if err != nil {
			room = false
			slog.WarnContext(ctx, "sched_quality.trial_room_failed", "component", "sched_quality", "account_id", account, "error", err)
		}
	}
	t.rooms[key] = room
	return room
}

func (t *selectionTrial) isTrial(account *Account) bool {
	gid := derefGroupID(t.request.GroupID)
	view, now := t.service.selectionView(), t.service.selectionNow()
	return account.Platform == PlatformOpenAI && !t.manual &&
		selectionState(view, gid, account, t.request.RequestedModel).Placement(selectionAuto(view, gid, now), now) == schedquality.PlaceTrial
}

// Borrowing an existing session cannot create a trial session on another owner.
func (t *selectionTrial) allowed(ctx context.Context, account *Account) bool {
	if !t.isTrial(account) || t.owner == 0 || t.owner == account.ID {
		return true
	}
	change := pendingOpenAIStickyChange(ctx)
	return change != nil && change.ownerReleased
}

func (t *selectionTrial) touch(ctx context.Context, account *Account) {
	if t.leases == nil || t.request.SessionHash == "" || !t.isTrial(account) {
		return
	}
	if err := t.leases.Touch(ctx, account.ID, OpenAIAttemptModel(account, t.request.RequestedModel), derefGroupID(t.request.GroupID), t.member, t.service.selectionNow()); err != nil {
		slog.WarnContext(ctx, "sched_quality.trial_touch_failed", "component", "sched_quality", "account_id", account.ID, "error", err)
	}
}

// reserve returns false after a lost race. The caller reorders with room=false
// and may try this account again in tier 1 after every remaining tier 0 account.
func (t *selectionTrial) reserve(ctx context.Context, account *Account, full bool) (*selectionTrialReservation, bool) {
	if !t.isTrial(account) {
		return nil, true
	}
	if t.owner == account.ID && t.request.SessionHash != "" {
		t.touch(ctx, account)
		return nil, true
	}
	model, gid := OpenAIAttemptModel(account, t.request.RequestedModel), derefGroupID(t.request.GroupID)
	if t.leases != nil {
		acquired, err := t.leases.Acquire(ctx, account.ID, model, gid, t.member, t.service.selectionNow())
		if err == nil && acquired {
			return &selectionTrialReservation{leases: t.leases, account: account.ID, group: gid, model: model, member: t.member}, true
		}
		if err != nil {
			slog.WarnContext(ctx, "sched_quality.trial_acquire_failed", "component", "sched_quality", "account_id", account.ID, "error", err)
		}
	}
	t.rooms[SchedAccountModel{AccountID: account.ID, Model: model}] = false
	return nil, full
}

func (t *selectionTrial) finish(ctx context.Context, result *AccountSelectionResult) {
	reservation := t.selected
	if result == nil || result.Account == nil {
		reservation.release(ctx)
		return
	}
	if result.Account.ID == t.owner {
		t.touch(ctx, result.Account)
	}
	if reservation == nil {
		return
	}
	change := pendingOpenAIStickyChange(ctx)
	if t.request.SessionHash != "" && change != nil && change.groupID == derefGroupID(t.request.GroupID) && change.trialSessionKey() == t.request.SessionHash && change.target == result.Account.ID {
		change.trial = reservation
		change.leases = t.leases
	} else if t.request.SessionHash != "" {
		if owner, err := t.service.getStickySessionAccountID(ctx, t.request.GroupID, t.request.SessionHash); err == nil && owner == result.Account.ID {
			reservation.retained.Store(true)
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
				change.releaseTrial(ctx)
				return
			}
			reservation.release(ctx)
		})
	}
}

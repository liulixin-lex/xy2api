package service

import (
	"context"
	"time"
)

const (
	TrialLeaseLimit = 3
	TrialLeaseIdle  = 10 * time.Minute
)

// SchedTrialLeases shares an account/model's trial capacity across groups.
// session is a member identity made with TrialLeaseSessionMember or
// TrialLeaseRequestMember; use the same identity for every operation on a lease.
// Each operation first removes members idle for at least TrialLeaseIdle.
// On Room/Acquire errors, callers treat capacity as full and rank the account in
// the second tier without blocking selection. Touch/Promote/Release/ReleaseGroup errors
// are logged only. Implementations return errors unchanged. A transport error
// may leave a mutation's outcome unknown; callers must not assume rollback.
type SchedTrialLeases interface {
	// Room reports whether this member already holds a lease or capacity remains.
	// It does not reserve or renew a lease. On error it returns false and the error.
	Room(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) (bool, error)
	// Acquire atomically reserves a slot, or renews an existing member even when
	// full. It returns false, nil when full, and false plus the error on failure.
	Acquire(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) (bool, error)
	// Promote atomically replaces this request reservation with its committed
	// session member. An existing session is renewed without consuming another slot.
	Promote(ctx context.Context, accountID int64, model string, groupID int64, request, session string, now time.Time) error
	// Touch renews only an existing, unexpired member. Missing members are a
	// successful no-op; storage failures are returned as errors.
	Touch(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) error
	// Release removes this member, using storage time for idle cleanup. Missing
	// members are a successful no-op; storage failures are returned as errors.
	Release(ctx context.Context, accountID int64, model string, groupID int64, session string) error
	// ReleaseGroup removes all session and request members for this group, using
	// storage time for idle cleanup. Missing groups succeed; failures return errors.
	ReleaseGroup(ctx context.Context, accountID int64, model string, groupID int64) error
}

// TrialLeaseSessionMember namespaces the sessionHash shared by sampling and
// selection, not the Redis binding key with its "openai:" prefix. Always wrap
// real session keys too, so "r:request-id" cannot alias a request member.
func TrialLeaseSessionMember(session string) string { return "s:" + session }

// TrialLeaseRequestMember namespaces a request, including a candidate session
// awaiting binding commit; Promote converts only successful candidates.
// Release it in each selection's release callback (including switches), rather
// than waiting for the whole request to finish. Idle expiry reclaims leaked slots.
func TrialLeaseRequestMember(requestID string) string { return "r:" + requestID }

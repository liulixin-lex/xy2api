package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// openAIStickyCapacityRetryMax caps the backoff of the sticky short wait
// (openAIStickyCapacityRetryInitial doubling, for
// openAIStickyCapacityRetryDuration).
const openAIStickyCapacityRetryMax = 200 * time.Millisecond

// openAIWSStickyRenewInterval throttles the session binding renewal of one
// WebSocket connection.
const openAIWSStickyRenewInterval = 5 * time.Minute

// WaitOpenAIStickyCapacity takes a slot of a full sticky account with the
// short wait of the sticky saturation paths: it calls try now and, while try
// reports the account full, again after 50ms doubling to 200ms, for at most
// two seconds. It never queues and returns ctx's error once ctx ends.
func WaitOpenAIStickyCapacity(ctx context.Context, try func() (bool, error)) (bool, error) {
	if acquired, err := try(); acquired || err != nil {
		return acquired, err
	}
	deadline := time.Now().Add(openAIStickyCapacityRetryDuration)
	for delay := openAIStickyCapacityRetryInitial; ; delay = min(delay*2, openAIStickyCapacityRetryMax) {
		wait := min(delay, time.Until(deadline))
		if wait <= 0 {
			return false, nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
		if acquired, err := try(); acquired || err != nil {
			return acquired, err
		}
	}
}

// OpenAIWSStickyRenewal keeps the session binding of one WebSocket connection
// alive while its turns succeed, so a session working past the binding TTL
// reconnects to the same account. It never creates a binding and only extends
// one that still names the connection's account. A nil renewal does nothing.
type OpenAIWSStickyRenewal struct {
	svc       *OpenAIGatewayService
	groupID   *int64
	session   string
	accountID int64
	renewedAt atomic.Int64
}

// NewOpenAIWSStickyRenewal starts at the connection's admission binding.
// Connections without a session, or borrowing a full owner's capacity whose
// binding stays with the owner, get none.
func (s *OpenAIGatewayService) NewOpenAIWSStickyRenewal(groupID *int64, sessionHash string, accountID int64, spillover bool) *OpenAIWSStickyRenewal {
	if s == nil || sessionHash == "" || accountID <= 0 || spillover {
		return nil
	}
	r := &OpenAIWSStickyRenewal{svc: s, groupID: groupID, session: sessionHash, accountID: accountID}
	r.renewedAt.Store(time.Now().UnixNano())
	return r
}

// TurnSucceeded renews the binding at most once per interval, with the TTL
// BindStickySession gives the group (the share TTL in a share group).
func (r *OpenAIWSStickyRenewal) TurnSucceeded(ctx context.Context) error {
	if r == nil || r.svc.cache == nil {
		return nil
	}
	ttl := r.svc.openAIWSSessionStickyTTL()
	if shareTTL, ok := r.svc.shareScheduling.shareBindingTTL(derefGroupID(r.groupID)); ok {
		ttl = shareTTL
	}
	now, last := time.Now().UnixNano(), r.renewedAt.Load()
	if time.Duration(now-last) < min(openAIWSStickyRenewInterval, ttl/2) || !r.renewedAt.CompareAndSwap(last, now) {
		return nil
	}
	// Quality routing keeps its own bindings.
	if r.svc.qualityOwnsSticky(ctx, r.groupID, r.session) {
		return nil
	}
	// The connection binds only the current-format key (its ctx carries no
	// legacy hash). EXPIRE never creates a key; a binding replaced between
	// the read and EXPIRE keeps its owner.
	key := r.svc.openAISessionCacheKey(r.session)
	owner, err := r.svc.cache.GetSessionAccountID(ctx, derefGroupID(r.groupID), key)
	if errors.Is(err, ErrStickySessionNotFound) || err == nil && owner != r.accountID {
		return nil
	}
	if err != nil {
		return err
	}
	return r.svc.cache.RefreshSessionTTL(ctx, derefGroupID(r.groupID), key, ttl)
}

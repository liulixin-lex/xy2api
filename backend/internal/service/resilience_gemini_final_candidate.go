package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type geminiFailoverStateKey struct{}
type geminiFailoverState struct {
	limitReached bool
	allowMixed   bool
	excluded     map[int64]struct{}
}

// WithGeminiFailoverState snapshots the handler's account-switch boundary.
// A last-resort label alone does not stop the handler from choosing another account.
func WithGeminiFailoverState(ctx context.Context, limitReached, allowMixed bool, excluded map[int64]struct{}) context.Context {
	snapshot := make(map[int64]struct{}, len(excluded))
	for id := range excluded {
		snapshot[id] = struct{}{}
	}
	return context.WithValue(ctx, geminiFailoverStateKey{}, geminiFailoverState{limitReached, allowMixed, snapshot})
}

func geminiNoFurtherAccount(ctx context.Context, account *Account) bool {
	if account == nil {
		return false
	}
	st := resilience.StateFrom(ctx)
	if b := st.Budget(); b != nil && b.Attempts()+1 >= b.MaxAttempts() {
		return true
	}
	boundary, known := ctx.Value(geminiFailoverStateKey{}).(geminiFailoverState)
	if !known {
		// Compatibility callers without handler state use mixed Gemini groups.
		boundary.allowMixed = true
	}
	if boundary.limitReached {
		return true
	}
	r := Resilience()
	if r == nil || r.fallbackAccounts == nil || st.GroupID() <= 0 {
		return false
	}
	// Read all platforms afresh: soleCandidate's same-platform view cannot
	// prove that the native handler will not fall back to Antigravity.
	accounts, err := r.fallbackAccounts.ListByGroup(ctx, st.GroupID())
	if err != nil {
		return false
	}
	found := false
	for i := range accounts {
		a := &accounts[i]
		if a.ID == account.ID {
			found = true
			continue
		}
		if _, excluded := boundary.excluded[a.ID]; excluded || st.AccountFailed(a.ID) {
			continue
		}
		eligiblePlatform := a.Platform == PlatformGemini ||
			(boundary.allowMixed && a.Platform == PlatformAntigravity && a.IsMixedSchedulingEnabled())
		if !a.IsActive() || !a.Schedulable || !eligiblePlatform {
			continue
		}
		if model := st.Model(); model != "" && !a.IsModelSupported(model) {
			continue
		}
		if tier, _, _ := resiliencePenaltyOf(a, st.Model(), time.Now()); tier != resilience.TierHard {
			return false
		}
	}
	return found
}

type geminiFinalKeepaliveKey struct{}
type geminiFinalKeepalive struct {
	accountID int64
	heartbeat *openAICompactSSEKeepalive
}

// GeminiFinalKeepaliveCommitted prevents later selection changes (including
// revived candidates and cross-platform fallbacks) from replaying a committed request.
func GeminiFinalKeepaliveCommitted(ctx context.Context, accountID int64) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(geminiFinalKeepaliveKey{}).(geminiFinalKeepalive)
	if !ok || v.accountID != accountID {
		return false
	}
	v.heartbeat.mu.Lock()
	defer v.heartbeat.mu.Unlock()
	return v.heartbeat.started
}

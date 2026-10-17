//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// TestResilienceSchedulingMatrix covers tasks 3.7: original or share
// scheduling × single- or two-account group × soft penalty, hard penalty or
// session failure streak × with or without an availability fallback group.
// Each case runs the two steps of a request: the fallback-group decision of
// the API key middleware, then account selection in the chosen group.
func TestResilienceSchedulingMatrix(t *testing.T) {
	const (
		penalised   = int64(1)
		healthy     = int64(2)
		fallbackAcc = int64(99)
		fallbackID  = int64(20)
		session     = "sess-matrix"
	)
	penalise := map[string]func(*Account){
		"soft": func(a *Account) {
			until := time.Now().Add(time.Minute)
			a.TempUnschedulableUntil, a.TempUnschedulableReason = &until, "transient:TRANSIENT HTTP 502"
		},
		"hard": func(a *Account) {
			until := time.Now().Add(time.Hour)
			a.TempUnschedulableUntil, a.TempUnschedulableReason = &until, resilience.BalancePauseReason("HTTP 403: Insufficient account balance")
		},
		"streak": func(*Account) {}, // failures are recorded for the session below
	}
	// want is the served account, "fallback" for a move to the fallback
	// group, or "none" for no available account.
	want := func(size int, penalty string, withFallback bool) string {
		switch {
		case size == 2:
			return "healthy"
		case penalty == "streak":
			return "penalised" // the group is healthy; the avoided account is the only one left
		case withFallback:
			return "fallback"
		case penalty == "soft":
			return "penalised" // last resort
		default:
			return "none"
		}
	}

	for _, scheduler := range []sharesched.Scheduler{sharesched.SchedulerSub2API, sharesched.SchedulerShare} {
		for _, size := range []int{1, 2} {
			for _, penalty := range []string{"soft", "hard", "streak"} {
				for _, withFallback := range []bool{false, true} {
					expected := want(size, penalty, withFallback)
					name := fmt.Sprintf("%s/%d-accounts/%s/fallback=%v", scheduler, size, penalty, withFallback)
					t.Run(name, func(t *testing.T) {
						withResilienceSettings(t, nil)
						ctx := context.Background()

						accounts := []Account{{ID: penalised, Priority: 1, Concurrency: 5}}
						rules := []sharesched.AccountRule{{AccountID: penalised, Tier: 1, Weight: 1}}
						if size == 2 {
							accounts = append(accounts, Account{ID: healthy, Priority: 1, Concurrency: 5})
							rules = append(rules, sharesched.AccountRule{AccountID: healthy, Tier: 1, Weight: 1})
						}
						env := newShareTestEnv(t, shareTestPolicy(scheduler, rules...), accounts...)
						penalise[penalty](env.repo.accountsByID[penalised])

						store := newMemResilienceStore()
						r := NewResilienceService(nil, store)
						env.gateway.accountRepo = groupListingRepo{env.repo}
						env.gateway.resilience = r

						primary := &Group{ID: shareTestGroupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
						if withFallback {
							fb := fallbackID
							primary.AvailabilityFallbackGroupID = &fb
						}
						r.ConfigureFallbackGroups(
							&groupAccountsRepo{byGroup: map[int64][]Account{
								shareTestGroupID: env.repo.accounts,
								fallbackID:       {{ID: fallbackAcc, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}},
							}},
							&fallbackGroupRepo{groups: map[int64]*Group{
								shareTestGroupID: primary,
								fallbackID:       {ID: fallbackID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
							}},
						)
						if penalty == "streak" {
							rec := env.gateway.gatewaySessionRecorder()
							require.NotNil(t, rec)
							for range resilience.Current().Session.StreakThreshold {
								rec.Failure(ctx, shareTestGroupID, session, penalised, resilience.CategoryTransient)
							}
							avoid, err := store.SessionAvoid(ctx, shareTestGroupID, session)
							require.NoError(t, err)
							require.Contains(t, avoid, penalised)
						}

						// Step 1: the API key middleware picks the group.
						gid := shareTestGroupID
						key := &APIKey{ID: 77, GroupID: &gid, Group: primary}
						if fb := r.AvailabilityFallbackGroup(ctx, key); fb != nil {
							require.Equal(t, "fallback", expected, "moved to the fallback group")
							require.Equal(t, fallbackID, fb.ID)
							return
						}
						require.NotEqual(t, "fallback", expected, "expected a move to the fallback group")

						// Step 2: selection in the group.
						st := resilience.NewRequestState()
						reqCtx := resilience.WithRequestState(cacheableContext(), st)
						result, err := env.gateway.SelectAccountWithLoadAwareness(reqCtx, &gid, session, "claude-sonnet-4-5", nil, "", 0)
						if expected == "none" {
							require.ErrorIs(t, err, ErrNoAvailableAccounts)
							return
						}
						require.NoError(t, err)
						require.NotNil(t, result.Account)
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
						switch expected {
						case "healthy":
							require.Equal(t, healthy, result.Account.ID)
							require.Zero(t, st.LastResort())
						case "penalised":
							require.Equal(t, penalised, result.Account.ID)
							if penalty == "soft" {
								require.Equal(t, penalised, st.LastResort(), "served as the last resort")
							}
						}
					})
				}
			}
		}
	}
}

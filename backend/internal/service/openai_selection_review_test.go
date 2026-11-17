//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAISelectionTrialHTTPReleaseBeforeCommit(t *testing.T) {
	for _, migration := range []bool{false, true} {
		for _, success := range []bool{false, true} {
			t.Run(fmt.Sprintf("migration=%t/success=%t", migration, success), func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
				leases := newSelectionTrialLeaseFake()
				selectionSetTrial(f, 1, leases)
				if migration {
					f.repo.accounts = append(f.repo.accounts, selectionFixtureAccount(10, 5))
					f.cache.sessionBindings["openai:session"] = 10
					f.view.States[fakeSchedViewKey{7, 10, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
				}
				ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
				result := selectTrialRuntime(t, f, ctx, "session")
				key := trialTestKey(1, "m", 7, "s:session")
				require.True(t, leases.held[key])
				// Production HTTP handlers release their slot before reporting Forward's outcome.
				result.ReleaseFunc()
				heldAfterSlotRelease := leases.held[key]
				require.Equal(t, []int64{1}, f.releases)
				CompleteOpenAIStickyChange(ctx, 1, success, false)
				t.Logf("after_slot_release=%t after_complete=%t binding=%d", heldAfterSlotRelease, leases.held[key], f.cache.sessionBindings["openai:session"])
				require.True(t, heldAfterSlotRelease, "slot release cannot decide an unfinished binding transaction")
				require.Equal(t, success, leases.held[key])
				if success {
					require.EqualValues(t, 1, f.cache.sessionBindings["openai:session"])
				}
			})
		}
	}
}

func TestOpenAISelectionTrialBorrowUsesRequestLease(t *testing.T) {
	for _, mode := range []string{"excluded", "capacity", "wait", "full", "rear_precedes_trial", "front_precedes_trial"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, 1, leases)
			f.cache.sessionBindings["openai:session"] = 10
			excluded := map[int64]struct{}{10: {}}
			if mode == "capacity" {
				excluded = nil
				f.loads.acquireResults = map[int64]bool{10: false}
			}
			if mode == "wait" {
				f.loads.acquireResults = map[int64]bool{1: false}
			}
			if mode == "full" {
				leases.full[1] = true
			}
			if mode == "rear_precedes_trial" || mode == "front_precedes_trial" {
				f.repo.accounts = append(f.repo.accounts, selectionFixtureAccount(2, 99))
				if mode == "rear_precedes_trial" {
					f.view.States[fakeSchedViewKey{7, 2, "m"}] = schedquality.StateView{Cache: schedquality.LineView{Identity: schedquality.IdentityRear}}
				}
			}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", excluded, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, result)
			want := int64(1)
			if mode == "rear_precedes_trial" || mode == "front_precedes_trial" {
				want = 2
			}
			require.Equal(t, want, result.Account.ID)
			require.True(t, result.StickyCapacitySpillover)
			if mode == "wait" {
				require.NotNil(t, result.WaitPlan)
			}
			requestKey := trialTestKey(1, "m", 7, TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID()))
			if want == 1 && mode != "full" {
				require.True(t, leases.held[requestKey])
			}
			require.False(t, leases.held[trialTestKey(1, "m", 7, "s:session")])
			CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
			require.EqualValues(t, 10, f.cache.sessionBindings["openai:session"])
			require.Empty(t, f.cache.casKeys)
			if result.WaitPlan != nil {
				CombineSelectionRelease(result, nil)()
			} else {
				result.ReleaseFunc()
			}
			require.Empty(t, leases.held)
		})
	}
}

func TestOpenAISelectionTakeoverRearOwnerWithoutMigration(t *testing.T) {
	for _, line := range []string{"cache", "ttfo"} {
		for _, ready := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/idle=%t", line, ready), func(t *testing.T) {
				f := selectionNewSessionFixture()
				f.candidates = f.candidates[:1]
				f.view.Takeovers[7] = fakeSchedTakeover{Idle: 15 * time.Minute, Enabled: true}
				f.binding.remaining = 16 * time.Minute
				if ready {
					f.binding.remaining = 15 * time.Minute
				}
				rear := schedquality.LineView{Identity: schedquality.IdentityRear}
				state := schedquality.StateView{Cache: rear}
				if line == "ttfo" {
					state = schedquality.StateView{TTFO: rear}
				}
				f.state(10, state)
				ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
				result := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
				require.Equal(t, ready, result != nil)
				if result != nil {
					require.Equal(t, "takeover", pendingOpenAIStickyChange(ctx).kind)
					require.Equal(t, "priority_takeover", resilience.StateFrom(ctx).StickyBypassReason())
					CompleteOpenAIStickyChange(ctx, 1, true, false)
					result.ReleaseFunc()
				}
				require.Zero(t, f.slots.held)
			})
		}
	}
}

func TestOpenAISelectionTrialReleaseUsesOwnerModel(t *testing.T) {
	for _, mode := range []string{"commit_missing_mapping", "direct_delete", "direct_delete_missing", "lookup_unavailable"} {
		t.Run(mode, func(t *testing.T) {
			owner := selectionFixtureAccount(10, 5)
			owner.Credentials = map[string]any{"model_mapping": map[string]any{"alias": "owner-model"}}
			f := newSelectionRuntimeFixture(t, owner, selectionFixtureAccount(1, 1))
			leases := newSelectionTrialLeaseFake()
			f.svc.selectionBoard.leases = leases
			f.cache.sessionBindings["openai:session"] = 10
			key := trialTestKey(10, "owner-model", 7, "s:session")
			leases.held[key] = true
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			ctx = context.WithValue(ctx, openAISelectionRequestKey{}, OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), Platform: PlatformOpenAI, SessionHash: "session", RequestedModel: "alias"})
			if mode == "direct_delete_missing" {
				f.svc.cache = &selectionMissingBindingCache{f.cache}
				require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), "session", 10))
			} else if mode == "direct_delete" {
				ctx = withOpenAILegacySessionHash(ctx, "old")
				f.cache.sessionBindings["openai:old"] = 10
				require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), "session", 10))
			} else {
				f.svc.prepareOpenAIStickyChange(ctx, ptrInt64Selection(7), "session")
				require.True(t, releasePendingOpenAIStickyOwner(ctx, ptrInt64Selection(7), "session", 10))
				planOpenAIStickyReplacement(ctx, ptrInt64Selection(7), "session", "alias", &AccountSelectionResult{Account: &f.repo.accounts[1]})
				if mode == "lookup_unavailable" {
					f.repo.accounts = f.repo.accounts[1:]
				}
				CompleteOpenAIStickyChange(ctx, 1, true, false)
			}
			require.Equal(t, mode == "lookup_unavailable", leases.held[key])
			require.NotContains(t, leases.calls, "release:10/alias/7/s:session", "never guess an upstream lease key from the client model")
		})
	}
}

// Match the real Redis missing-key contract, rather than the legacy zero/nil fake.
type selectionMissingBindingCache struct{ *selectionRuntimeCache }

func (c *selectionMissingBindingCache) GetSessionAccountID(ctx context.Context, g int64, key string) (int64, error) {
	id, err := c.selectionRuntimeCache.GetSessionAccountID(ctx, g, key)
	if err == nil && id == 0 {
		return 0, ErrStickySessionNotFound
	}
	return id, err
}

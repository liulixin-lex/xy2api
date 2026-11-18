//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestFirstOutputFollowSimpleModeFullPool(t *testing.T) {
	withResilienceSettings(t, nil)
	y := selectionFixtureAccount(11, 2)
	y.GroupIDs = []int64{99}
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), y)
	f.svc.cfg.RunMode = config.RunModeSimple
	const key = "openai:session"
	f.cache.sessionBindings[key] = 10
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	x := selectTrialRuntime(t, f, ctx, "session")
	require.EqualValues(t, 10, x.Account.ID)
	x.ReleaseFunc()
	change := pendingOpenAIStickyChange(ctx)
	fact := resilience.OwnerFirstOutputCutoff{GroupID: 7, Session: "session", BindingKey: change.key, AccountID: x.Account.ID,
		Generation: change.expected.Generation, AttemptSeq: st.MarkOpsUpstreamSendBoundary(x.Account.ID), FiredAt: time.Now(), Effective: time.Second, CanTransfer: change.canTransfer}
	require.True(t, st.RecordOwnerCutoff(fact))
	recordFirstOutputAvoid(ctx, fact)
	sel, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", map[int64]struct{}{x.Account.ID: {}},
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
	require.NoError(t, err)
	require.NotNil(t, sel)
	defer sel.ReleaseFunc()
	require.Equal(t, y.ID, sel.Account.ID)
	require.Equal(t, stickyFollow, pendingOpenAIStickyChange(ctx).kind)
	followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
	require.Equal(t, y.ID, f.cache.sessionBindings[key])
	require.Equal(t, []string{key}, f.cache.casKeys)
}

func TestFirstOutputFollowOwnerReleasePrecedesBorrow(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, reason   string
		healthy, avoid bool
		owner          int64
	}{
		{"hard_release", "owner_should_clear", false, false, 11},
		{"hard_release_with_avoid", "owner_should_clear", false, true, 11},
		{"healthy_borrow", "first_output_avoid", true, true, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := selectionFixtureAccount(10, 1)
			x.Schedulable = tc.healthy
			f := newSelectionRuntimeFixture(t, x, selectionFixtureAccount(11, 2))
			const key = "openai:session"
			f.cache.sessionBindings[key] = x.ID
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			if tc.avoid {
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), x.ID, time.Minute))
			}
			logs := captureOpenAIObservationLogs(t)
			sel := selectTrialRuntime(t, f, ctx, "session")
			defer sel.ReleaseFunc()
			require.EqualValues(t, 11, sel.Account.ID)
			require.Equal(t, tc.reason, st.StickyBypassReason())
			require.Contains(t, logs.String(), `"reason":"`+tc.reason+`"`)
			followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
			require.Equal(t, tc.owner, f.cache.sessionBindings[key])
			if tc.healthy {
				require.Empty(t, f.cache.casKeys)
			} else {
				require.Equal(t, "owner_released", pendingOpenAIStickyChange(ctx).kind)
				require.Equal(t, []string{key}, f.cache.casKeys)
			}
		})
	}
}

func TestFirstOutputFollowStickyOwnerEligibilityOrder(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"model", "session_hash"} {
		for _, tc := range []struct {
			name, reason         string
			healthy, firstOutput bool
		}{
			{"eligible", "first_output_avoid", true, true},
			{"disabled", "owner_should_clear", false, true},
			{"ordinary_exclusion", "owner_excluded", false, false},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				x := selectionFixtureAccount(10, 1)
				x.Schedulable = tc.healthy
				f := newSelectionRuntimeFixture(t, x)
				f.cache.sessionBindings["openai:session"] = x.ID
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				group := int64(7)
				f.svc.prepareOpenAIStickyChange(ctx, &group, "session")
				if tc.firstOutput {
					ctx = context.WithValue(ctx, firstOutputOnlyOwnerKey{}, x.ID)
				}
				ex := map[int64]struct{}{x.ID: {}}
				if path == "model" {
					require.Nil(t, f.svc.tryStickySessionHit(ctx, &group, PlatformOpenAI, "session", "m", ex, false, x.ID, OpenAIEndpointCapabilityResponses))
				} else {
					sel, _, err := f.svc.selectBySessionHash(ctx, OpenAIAccountScheduleRequest{
						GroupID: &group, Platform: PlatformOpenAI, SessionHash: "session", RequestedModel: "m", StickyAccountID: x.ID, ExcludedIDs: ex,
						RequiredTransport: OpenAIUpstreamTransportHTTPSSE, RequiredCapability: OpenAIEndpointCapabilityResponses,
					})
					require.NoError(t, err)
					require.Nil(t, sel)
				}
				require.Equal(t, tc.reason, st.StickyBypassReason())
				require.Equal(t, tc.reason == "owner_should_clear", pendingOpenAIStickyChange(ctx).ownerReleased)
				require.Empty(t, f.attempts)
			})
		}
	}
}

func TestFirstOutputFollowEmptyScopePreservesBindingChanges(t *testing.T) {
	withResilienceSettings(t, nil)
	const key = "openai:session"
	for _, kind := range []string{"create", "owner_released", "takeover"} {
		t.Run(kind, func(t *testing.T) {
			type outcome struct {
				selected, target, owner int64
				kind, reason            string
				slots                   []int64
				cas                     []string
			}
			run := func(readScope bool) outcome {
				x := selectionFixtureAccount(10, 5)
				x.Schedulable = kind != "owner_released"
				f := newSelectionRuntimeFixture(t, x, selectionFixtureAccount(11, 1))
				if kind != "create" {
					f.cache.sessionBindings[key] = x.ID
				}
				ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
				if !readScope {
					ctx = WithOpenAITransferProtected(ctx)
				}
				sel := selectTrialRuntime(t, f, ctx, "session")
				defer sel.ReleaseFunc()
				change := pendingOpenAIStickyChange(ctx)
				followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
				return outcome{sel.Account.ID, change.target, f.cache.sessionBindings[key], change.kind, change.reason, f.attempts, f.cache.casKeys}
			}
			before := run(false)
			require.Equal(t, kind, before.kind)
			require.EqualValues(t, 11, before.owner)
			require.Equal(t, []string{key}, before.cas)
			require.Equal(t, before, run(true))
		})
	}
}

//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/config"
	"maps"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func newCollabThreadFixture(t *testing.T, accounts ...Account) *selectionRuntimeFixture {
	t.Helper()
	f := newSelectionRuntimeFixture(t, accounts...)
	f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = false
	f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = false
	return f
}

func collabAffinityIdentity(thread, parent string) resilience.CollabTreeHint {
	return resilience.CollabTreeHint{Scope: resilience.CollabRecordScope{Tenant: "tenant-digest", Group: "group-digest", Tree: "tree-digest"}, Thread: resilience.CollabDigest(thread), Parent: resilience.CollabDigest(parent)}
}

func collabAffinitySettings() *resilience.Settings {
	s := resilience.Defaults()
	s.Collab = resilience.CollabSettings{TreeAffinity: true}
	return &s
}

func collabAffinityContext(store *resilience.CollabTreeAffinity, settings *resilience.Settings, thread, parent string) context.Context {
	role := resilience.CollabRoleRoot
	if parent != "" {
		role = resilience.CollabRoleChild
	}
	return WithOpenAICollabTreeAffinity(selectionReviewContext(), store, settings, 7, collabAffinityIdentity(thread, parent), role, true, &config.Config{})
}

func prepareAffinityTest(f *selectionRuntimeFixture, ctx context.Context, session string) context.Context {
	f.svc.cfg.JWT = config.JWTConfig{Secret: "synthetic-affinity-secret"}
	if openAIRejectionScope(ctx).session == "" {
		ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: "child-execution", preserveBinding: true, selectionPreservesBinding: &atomic.Bool{}})
	}
	ctx = withOpenAINoStickyMigration(ctx)
	f.svc.PrepareOpenAICollabThreadBinding(ctx, 1, session, PlatformOpenAI, OpenAIClientTransportHTTP, true)
	return ctx
}

func collabAffinitySelect(f *selectionRuntimeFixture, ctx context.Context, model string, excluded map[int64]struct{}) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "shared-session", model, excluded, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
}

func TestCollabAffinityAloneNeverAddsSlotWait(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, on := range []bool{false, true} {
		for _, mode := range []string{"free", "busy", "unsupported-model", "excluded", "wrong-group", "cooldown", "unavailable", "constraint", "missing", "admission-veto", "privacy", "profit"} {
			t.Run(fmt.Sprintf("on=%t/%s", on, mode), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := newCollabThreadFixture(t, selectionFixtureAccount(1, 2), selectionFixtureAccount(2, 1))
					f.loads.acquireResults = map[int64]bool{1: mode != "busy", 2: true}
					s := resilience.NewCollabTreeAffinity(32, time.Hour)
					settings := collabAffinitySettings()
					root := collabAffinityContext(s, settings, "root", "")
					if mode != "missing" {
						NoteOpenAICollabAdmission(root, 1, false)
					}
					var excluded map[int64]struct{}
					switch mode {
					case "unsupported-model":
						f.repo.accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"other-model": "other-model"}}
					case "excluded":
						excluded = map[int64]struct{}{1: {}}
					case "wrong-group":
						f.repo.accounts[0].GroupIDs = []int64{8}
					case "cooldown":
						until := time.Now().Add(time.Minute)
						f.repo.accounts[0].TempUnschedulableUntil = &until
					case "unavailable":
						f.repo.accounts[0].Schedulable = false
					case "privacy":
						f.repo.accounts[1].Extra = map[string]any{"privacy_mode": PrivacyModeTrainingOff}
					case "profit":
						f.repo.accounts[0].RateMultiplier = floatPtr(1)
						f.repo.accounts[1].RateMultiplier = floatPtr(0.2)
					case "admission-veto":
						f.loads.after = func(id int64) {
							if id == 1 {
								f.repo.accounts[0].Schedulable = false
							}
						}
					}
					settings.Collab.TreeAffinity = on
					base := selectionReviewContext()
					if mode == "privacy" {
						base = context.WithValue(base, openAIGroupPrivacyRequirementContextKey{}, openAIGroupPrivacyRequirement{groupID: 7, required: true})
					}
					if mode == "profit" {
						base = context.WithValue(base, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 7, platform: PlatformOpenAI, threshold: 0.5, ceilingOn: true, pricingAt: time.Now()})
					}
					ctx := WithOpenAICollabTreeAffinity(base, s, settings, 7, collabAffinityIdentity("child", "root"), resilience.CollabRoleChild, true, &config.Config{})
					if !on {
						require.Equal(t, base, ctx, "rollback with live hints does no work")
					}
					if mode == "constraint" {
						ctx = WithOpenAISelectionCandidateConstraint(ctx, 1, "m", 99)
					}
					start := time.Now()
					selected, decision, err := collabAffinitySelect(f, ctx, "m", excluded)
					require.NoError(t, err)
					require.NotNil(t, selected)
					defer selected.ReleaseFunc()
					require.True(t, selected.Acquired)
					require.Nil(t, selected.WaitPlan)
					require.Equal(t, start, time.Now(), "a hint miss must not enter the sticky capacity retry")
					want := int64(2)
					if on && mode == "free" {
						want = 1
						require.False(t, decision.StickySessionHit)
					}
					require.Equal(t, want, selected.Account.ID)
					if !on {
						require.Equal(t, []int64{2}, f.attempts)
					}
				})
			})
		}
	}
}

func TestCollabAffinityDoesNotCrossEffectiveGroups(t *testing.T) {
	s := resilience.NewCollabTreeAffinity(32, time.Hour)
	settings := collabAffinitySettings()
	root := collabAffinityContext(s, settings, "root", "")
	NoteOpenAICollabAdmission(root, 1, false)
	ctx := collabAffinityContext(s, settings, "child", "root")
	require.False(t, NoteOpenAICollabCiphertextFailure(ctx, 2, time.Now().Add(time.Minute)))
	require.EqualValues(t, 1, openAICollabHint(ctx, 0))
	changed := context.WithValue(ctx, openAISelectionRequestKey{}, OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(8)})
	require.Zero(t, openAICollabHint(changed, 0))
	require.Nil(t, openAICollabBindingFrom(changed))
	require.Nil(t, openAICollabAffinityFrom(changed))
}

func TestCollabAffinityCrossModelGrandchildAndParentPrecedence(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newCollabThreadFixture(t, selectionFixtureAccount(1, 3), selectionFixtureAccount(2, 1), selectionFixtureAccount(3, 2))
	s := resilience.NewCollabTreeAffinity(32, time.Hour)
	settings := collabAffinitySettings()
	root := collabAffinityContext(s, settings, "root", "")
	NoteOpenAICollabAdmission(root, 1, false)
	child := collabAffinityContext(s, settings, "child", "root")
	selected, _, err := collabAffinitySelect(f, child, "child-model", nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, selected.Account.ID)
	selected.ReleaseFunc()
	NoteOpenAICollabAdmission(child, 1, false)
	grandchildIdentity := collabAffinityIdentity("grandchild", "child")
	grandchildIdentity.Scope.Tree = ""
	grandchild := WithOpenAICollabTreeAffinity(selectionReviewContext(), s, settings, 7, grandchildIdentity, resilience.CollabRoleChild, true, &config.Config{})
	selected, _, err = collabAffinitySelect(f, grandchild, "grandchild-model", nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, selected.Account.ID)
	selected.ReleaseFunc()
	f.cache.sessionBindings["openai:parent-session"] = 3
	withParent := context.WithValue(child, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{currentSessionHash: "parent-session"})
	selected, decision, err := collabAffinitySelect(f, withParent, "child-model", nil)
	require.NoError(t, err)
	require.EqualValues(t, 3, selected.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerGuardianParent, decision.Layer)
	selected.ReleaseFunc()
}

func TestCollabAffinityChildRetryMovesBeforeFinalOutcome(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(3, 3))
	f.cache.sessionBindings["openai:shared-session"] = 1
	s := resilience.NewCollabTreeAffinity(32, time.Hour)
	settings := collabAffinitySettings()
	root := collabAffinityContext(s, settings, "root", "")
	NoteOpenAICollabAdmission(root, 1, false)
	child := prepareAffinityTest(f, collabAffinityContext(s, settings, "child", "root"), "shared-session")
	initial, _, initialErr := collabAffinitySelect(f, child, "m", nil)
	require.NoError(t, initialErr)
	require.EqualValues(t, 1, initial.Account.ID)
	initial.ReleaseFunc()
	expires := time.Now().Add(time.Minute)
	require.True(t, NoteOpenAICollabCiphertextFailure(child, 1, expires))
	// A concurrent client retry sees avoidance before any success/final hook.
	retry := prepareAffinityTest(f, collabAffinityContext(s, settings, "child", "root"), "shared-session")
	selected, _, err := collabAffinitySelect(f, retry, "m", nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, selected.Account.ID)
	selected.ReleaseFunc()
	require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(child, ptrInt64Selection(7), "shared-session", 3))
	retry = prepareAffinityTest(f, collabAffinityContext(s, settings, "child", "root"), "shared-session")
	selected, _, err = collabAffinitySelect(f, retry, "another-model", nil)
	require.NoError(t, err)
	require.EqualValues(t, 3, selected.Account.ID, "recovered thread wins over a different tree account")
	selected.ReleaseFunc()
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:shared-session"])
	rootRetry := collabAffinityContext(s, settings, "root", "")
	selected, _, err = collabAffinitySelect(f, rootRetry, "m", nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, selected.Account.ID)
	selected.ReleaseFunc()
	require.False(t, NoteOpenAICollabCiphertextFailure(root, 1, expires), "root keeps today's rejection path")
	NoteOpenAICollabAdmission(root, 2, false)
	NoteOpenAICollabSuccess(root, 2, false)
	hint, ok := s.LookupTreeHint(root, collabAffinityIdentity("root", "").Scope)
	require.True(t, ok)
	require.EqualValues(t, 2, hint.AccountID)
	require.Equal(t, resilience.CollabTreeHintStatusConfirmed, hint.Status)
}

func TestCollabAffinityOptionalBorrowAndOffLeaveBindings(t *testing.T) {
	s := resilience.NewCollabTreeAffinity(32, time.Hour)
	settings := collabAffinitySettings()
	root := collabAffinityContext(s, settings, "root", "")
	NoteOpenAICollabAdmission(root, 1, false)
	NoteOpenAICollabSuccess(root, 1, false)
	for _, mode := range []string{"optional", "borrow", "off"} {
		t.Run(mode, func(t *testing.T) {
			copy := *settings
			copy.Collab.TreeAffinity = mode != "off"
			ctx := collabAffinityContext(s, &copy, "child", "root")
			if mode == "optional" {
				ctx = resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true})
			}
			if mode != "borrow" {
				require.False(t, NoteOpenAICollabCiphertextFailure(ctx, 1, time.Now().Add(time.Minute)))
			}
			NoteOpenAICollabAdmission(ctx, 2, mode == "borrow")
			NoteOpenAICollabSuccess(ctx, 2, mode == "borrow")
			hint, ok := s.LookupTreeHint(ctx, collabAffinityIdentity("root", "").Scope)
			require.True(t, ok)
			require.EqualValues(t, 1, hint.AccountID)
			_, ok = s.LookupThreadRoute(ctx, hint.Scope, "child")
			require.False(t, ok)
		})
	}
}

func TestCollabAffinityLeaseGatesAndDelivery(t *testing.T) {
	for _, lossless := range []bool{false, true} {
		t.Run(fmt.Sprint(lossless), func(t *testing.T) {
			s := resilience.NewCollabTreeAffinity(32, time.Hour)
			settings := collabAffinitySettings()
			settings.Collab.Ladder, settings.Collab.LosslessFirst = lossless, lossless
			ctx := collabAffinityContext(s, settings, "root", "")
			identity := collabAffinityIdentity("root", "")
			lease := resilience.CollabDeliveryLease{Key: resilience.CollabDeliveryKey{Scope: identity.Scope, Thread: "child", Item: "item", Delivery: "delivery", Digest: "ciphertext"}, Issued: resilience.CollabIssuedToken{Scope: identity.Scope, Token: "token"}, AccountID: 1}
			event := resilience.CollabDeliveryEvent{Phase: resilience.CollabDeliveryPhaseWritten, EventType: "response.output_item.done", Executable: true, BytesWritten: 100}
			bad := event
			bad.WriteError = errors.New("synthetic write failure")
			require.False(t, NoteOpenAICollabLeaseWritten(ctx, bad, lease, true))
			bad = event
			bad.Phase = resilience.CollabDeliveryPhaseReceived
			require.False(t, NoteOpenAICollabLeaseWritten(ctx, bad, lease, true))
			bad = event
			bad.EventType = "response.function_call_arguments.delta"
			require.False(t, NoteOpenAICollabLeaseWritten(ctx, bad, lease, true))
			bad = event
			bad.Generation++
			require.False(t, NoteOpenAICollabLeaseWritten(ctx, bad, lease, true))
			require.False(t, NoteOpenAICollabLeaseWritten(ctx, event, lease, false))
			require.False(t, openAICollabLeaseSuppressesMigration(ctx))
			require.True(t, NoteOpenAICollabLeaseWritten(ctx, event, lease, true))
			require.Equal(t, lossless, openAICollabLeaseSuppressesMigration(ctx))
			require.Equal(t, !lossless, firstOutputMigrationAllowed(ctx, "session"))
			child := collabAffinityContext(s, settings, "child", "root")
			child = withOpenAINoStickyMigration(child)
			require.True(t, OpenAIQueueReselectionAllowed(child), "queue recovery is independent of quality suppression")
			ReleaseOpenAICollabLease(ctx, lease.Key, resilience.CollabLeaseReleaseConsumed)
			require.False(t, openAICollabLeaseSuppressesMigration(ctx))
		})
	}
}

func TestCollabAffinityLeaseSuppressesQualityEvidenceOnlyWhenOptedIn(t *testing.T) {
	for _, lossless := range []bool{false, true} {
		t.Run(fmt.Sprint(lossless), func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			c, _, _ := qualityTestContext(svc, 1, 0, "gpt-6-astra", "")
			s := resilience.NewCollabTreeAffinity(32, time.Hour)
			settings := collabAffinitySettings()
			settings.Collab.Ladder, settings.Collab.LosslessFirst = lossless, lossless
			identity := collabAffinityIdentity("root", "")
			ctx := WithOpenAICollabTreeAffinity(c.Request.Context(), s, settings, 0, identity, resilience.CollabRoleRoot, false, &config.Config{})
			require.True(t, s.AcquireDeliveryLease(ctx, resilience.CollabDeliveryLease{Key: resilience.CollabDeliveryKey{Scope: identity.Scope, Thread: "child", Item: "item", Delivery: "delivery", Digest: "ciphertext"}, Issued: resilience.CollabIssuedToken{Scope: identity.Scope, Token: "token"}, AccountID: 1}))
			account := selectionFixtureAccount(1, 1)
			svc.qualityObserver(ctx, &account, "gpt-6-astra").ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
			entry, exists := svc.openaiQualityStates.Load(qualityRequest(ctx).scope)
			local, _ := entry.(*openAIQualityLocal)
			if lossless {
				if exists {
					require.Zero(t, local.state.Generation)
				}
			} else {
				require.True(t, exists)
				require.Positive(t, local.state.Generation)
			}
		})
	}
}

func TestCollabAffinityRejectionWrapperKeepsExistingTiming(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
			f.cache.sessionBindings["openai:shared-session"] = 1
			s := resilience.NewCollabTreeAffinity(32, time.Hour)
			settings := collabAffinitySettings()
			settings.Collab.TreeAffinity = on
			ctx := collabAffinityContext(s, settings, "child", "root")
			ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: "child-execution", preserveBinding: true})
			ctx = prepareAffinityTest(f, ctx, "shared-session")
			f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 1, true)
			rootAvoid, err := f.svc.resilience.store.SessionAvoid(ctx, 7, "shared-session")
			require.NoError(t, err)
			childAvoid, err := f.svc.resilience.store.SessionAvoid(ctx, 7, "child-execution")
			require.NoError(t, err)
			if on {
				require.Contains(t, childAvoid, int64(1))
				require.Empty(t, rootAvoid)
				require.EqualValues(t, 1, f.cache.sessionBindings["openai:shared-session"])
			} else {
				require.Contains(t, rootAvoid, int64(1))
				require.Empty(t, childAvoid)
				require.Zero(t, f.cache.sessionBindings["openai:shared-session"])
			}
		})
	}
}

func TestCollabAffinityAvoidanceFallbackRetainsTenant(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
	s := resilience.NewCollabTreeAffinity(32, time.Hour)
	settings := collabAffinitySettings()
	first := collabAffinityIdentity("child", "root")
	second := first
	second.Scope.Tenant = "another-tenant"
	for _, identity := range []resilience.CollabTreeHint{first, second} {
		ctx := WithOpenAICollabTreeAffinity(selectionReviewContext(), s, settings, 7, identity, resilience.CollabRoleChild, true, &config.Config{})
		ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: string(identity.Scope.Tenant) + ":execution", preserveBinding: true, selectionPreservesBinding: &atomic.Bool{}})
		ctx = prepareAffinityTest(f, ctx, "shared-session")
		f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 1, true)
		avoided, err := f.svc.resilience.store.SessionAvoid(ctx, 7, openAIRejectionScope(ctx).session)
		require.NoError(t, err)
		require.Contains(t, avoided, int64(1))
		if identity == first {
			other, err := f.svc.resilience.store.SessionAvoid(ctx, 7, "another-execution")
			require.NoError(t, err)
			require.Empty(t, other)
		}
	}
}

func TestCollabAffinityLeaseSuppressesPendingQualityMigration(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprint(hard), func(t *testing.T) {
			f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
			c, _, session := qualityTestContext(f.svc, 1, 7, "gpt-6-astra", "")
			q := qualityRequest(c.Request.Context())
			q.loaded = true
			q.state = OpenAIQualityState{Generation: 1, Owner: 1, Rotation: "rotation", Expires: time.Now().Add(time.Hour).UnixMilli(), Avoid: map[string]OpenAIQualityAvoid{"1": {Provider: "provider", Until: time.Now().Add(time.Hour).UnixMilli()}}}
			s := resilience.NewCollabTreeAffinity(32, time.Hour)
			settings := collabAffinitySettings()
			settings.Collab.Ladder, settings.Collab.LosslessFirst = true, true
			identity := collabAffinityIdentity("root", "")
			ctx := WithOpenAICollabTreeAffinity(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()), s, settings, 7, identity, resilience.CollabRoleRoot, false, &config.Config{})
			require.True(t, s.AcquireDeliveryLease(ctx, resilience.CollabDeliveryLease{Key: resilience.CollabDeliveryKey{Scope: identity.Scope, Thread: "child", Item: "item", Delivery: "delivery", Digest: "ciphertext"}, Issued: resilience.CollabIssuedToken{Scope: identity.Scope, Token: "token"}, AccountID: 1}))
			var excluded map[int64]struct{}
			if hard {
				excluded = map[int64]struct{}{1: {}}
			}
			selection, _, err := f.svc.SelectAccountWithScheduler(ctx, ptrInt64Selection(7), "", session, "gpt-6-astra", excluded, OpenAIUpstreamTransportHTTPSSE, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			defer selection.ReleaseFunc()
			if hard {
				require.EqualValues(t, 2, selection.Account.ID)
			} else {
				require.EqualValues(t, 1, selection.Account.ID)
			}
			require.EqualValues(t, 1, q.state.Generation, "suppression does not rewrite quality state")
		})
	}
}

func TestCollabAffinityLeasePreservesQualityOwnerButNotHardExclusions(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			f := newCollabThreadFixture(t, selectionFixtureAccount(1, 2), selectionFixtureAccount(2, 1))
			f.cache.sessionBindings["openai:shared-session"] = 1
			f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
			before := maps.Clone(f.cache.sessionBindings)
			s := resilience.NewCollabTreeAffinity(32, time.Hour)
			settings := collabAffinitySettings()
			settings.Collab.Ladder, settings.Collab.LosslessFirst = true, true
			ctx := collabAffinityContext(s, settings, "root", "")
			scope := collabAffinityIdentity("root", "").Scope
			require.True(t, s.AcquireDeliveryLease(ctx, resilience.CollabDeliveryLease{Key: resilience.CollabDeliveryKey{Scope: scope, Thread: "child", Item: "item", Delivery: "delivery", Digest: "ciphertext"}, Issued: resilience.CollabIssuedToken{Scope: scope, Token: "token"}, AccountID: 1}))
			var excluded map[int64]struct{}
			if blocked {
				excluded = map[int64]struct{}{1: {}}
			}
			selected, _, err := collabAffinitySelect(f, ctx, "m", excluded)
			require.NoError(t, err)
			defer selected.ReleaseFunc()
			if blocked {
				require.EqualValues(t, 2, selected.Account.ID)
			} else {
				require.EqualValues(t, 1, selected.Account.ID)
				require.Equal(t, before, f.cache.sessionBindings)
			}
		})
	}
}

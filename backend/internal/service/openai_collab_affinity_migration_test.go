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

// The ordinary guardian-parent lookup remains the first authority, including
// legacy adoption. Hint bookkeeping must add no session/alias operations.
func RunCollabMigrationParentLookup(t *testing.T, cache GatewayCache, store ResilienceStore, reset func(), operations func() int64) {
	withResilienceSettings(t, nil)
	for migration := range 4 {
		for _, legacy := range []string{"", "legacy"} {
			for _, primary := range []bool{false, true} {
				var outcomes []string
				for _, on := range []bool{false, true} {
					reset()
					f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(3, 2))
					f.svc.cache, f.svc.resilience.store = cache, store
					f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
					f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
					ctx := context.WithValue(selectionReviewContext(), openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{currentSessionHash: "v2:parent", legacySessionHash: legacy})
					if primary {
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:v2:parent", 3, time.Hour))
					}
					if legacy != "" {
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:"+legacy, 3, time.Hour))
					}
					settings := collabAffinitySettings()
					settings.Collab.TreeAffinity = on
					local := resilience.NewCollabTreeAffinity(32, time.Hour)
					before := operations()
					root := WithOpenAICollabTreeAffinity(ctx, local, settings, 7, collabAffinityIdentity("root", ""), resilience.CollabRoleRoot, true)
					NoteOpenAICollabAdmission(root, 1, false)
					ctx = WithOpenAICollabTreeAffinity(ctx, local, settings, 7, collabAffinityIdentity("child", "root"), resilience.CollabRoleChild, true)
					require.Equal(t, before, operations(), "hint setup/admission is local")
					result, decision, err := f.svc.SelectAccountWithScheduler(ctx, ptrInt64Selection(7), "", "v2:parent", "m", nil, OpenAIUpstreamTransportHTTPSSE, false)
					require.NoError(t, err)
					want := int64(1)
					if primary || migration&1 != 0 && legacy != "" {
						want = 3
					}
					require.Equal(t, want, result.Account.ID)
					result.ReleaseFunc()
					count := operations() - before
					parent, parentErr := cache.GetSessionAccountID(ctx, 7, "openai:v2:parent")
					outcomes = append(outcomes, fmt.Sprintf("selected=%d layer=%s operations=%d parent=%d err=%v", result.Account.ID, decision.Layer, count, parent, parentErr))
				}
				require.Equal(t, outcomes[0], outcomes[1], "migration=%d legacy=%s primary=%t", migration, legacy, primary)
				t.Logf("migration=%d legacy=%s primary=%t %s", migration, legacy, primary, outcomes[1])
			}
		}
	}
}

func TestCollabAffinityMigrationIndependent(t *testing.T) {
	withResilienceSettings(t, nil)
	for migration := range 4 {
		for _, legacy := range []string{"", "legacy"} {
			for _, mode := range []string{"hint", "degraded", "trial_full", "trial_available"} {
				t.Run(fmt.Sprintf("migration=%d/legacy=%s/%s", migration, legacy, mode), func(t *testing.T) {
					f := newCollabThreadFixture(t, selectionFixtureAccount(1, 2), selectionFixtureAccount(2, 1))
					f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
					f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
					store := resilience.NewCollabTreeAffinity(32, time.Hour)
					settings := collabAffinitySettings()
					base := withOpenAILegacySessionHash(selectionReviewContext(), legacy)
					root := WithOpenAICollabTreeAffinity(base, store, settings, 7, collabAffinityIdentity("root", ""), resilience.CollabRoleRoot, true)
					NoteOpenAICollabAdmission(root, 1, false)
					NoteOpenAICollabSuccess(root, 1, false)
					child := WithOpenAICollabTreeAffinity(base, store, settings, 7, collabAffinityIdentity("child", "root"), resilience.CollabRoleChild, true)
					require.EqualValues(t, 1, openAICollabHint(child, 0))
					NoteOpenAICollabAdmission(child, 1, false)
					identity := collabAffinityIdentity("grandchild", "child")
					identity.Scope.Tree = ""
					grandchild := WithOpenAICollabTreeAffinity(base, store, settings, 7, identity, resilience.CollabRoleChild, true)
					require.EqualValues(t, 1, openAICollabHint(grandchild, 0))
					leases := newSelectionTrialLeaseFake()
					if mode == "degraded" {
						f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
					}
					if mode == "trial_full" || mode == "trial_available" {
						selectionSetTrial(f, 1, leases)
						leases.full[1] = mode == "trial_full"
					}
					selected, _, err := collabAffinitySelect(f, grandchild, "m", nil)
					require.NoError(t, err)
					want := int64(1)
					if mode == "degraded" || mode == "trial_full" {
						want = 2
					}
					require.Equal(t, want, selected.Account.ID)
					if mode == "trial_available" {
						require.NotEmpty(t, leases.held)
					}
					selected.ReleaseFunc()
					for _, lossless := range []bool{false, true} {
						settings.Collab.Ladder, settings.Collab.LosslessFirst = lossless, lossless
						for _, delivery := range []resilience.CollabDigest{"first", "second"} {
							lease := resilience.CollabDeliveryLease{Key: resilience.CollabDeliveryKey{Scope: collabAffinityIdentity("root", "").Scope, Thread: "child", Item: "item", Delivery: delivery, Digest: "ciphertext"}, Issued: resilience.CollabIssuedToken{Scope: collabAffinityIdentity("root", "").Scope, Token: "token"}, AccountID: 1}
							// Distinct item identities avoid reusing released deliveries.
							lease.Key.Item = resilience.CollabDigest(fmt.Sprint(lossless))
							require.True(t, NoteOpenAICollabLeaseWritten(root, resilience.CollabDeliveryEvent{Phase: resilience.CollabDeliveryPhaseWritten, EventType: "response.output_item.done", Executable: true, BytesWritten: 1}, lease, true))
						}
						require.Equal(t, lossless, openAICollabLeaseSuppressesMigration(child))
						key := resilience.CollabDeliveryKey{Scope: collabAffinityIdentity("root", "").Scope, Thread: "child", Item: resilience.CollabDigest(fmt.Sprint(lossless)), Delivery: "first", Digest: "ciphertext"}
						ReleaseOpenAICollabLease(child, key, resilience.CollabLeaseReleaseConsumed)
						require.Equal(t, lossless, openAICollabLeaseSuppressesMigration(child))
						key.Delivery = "second"
						ReleaseOpenAICollabLease(child, key, resilience.CollabLeaseReleaseTerminated)
						require.False(t, openAICollabLeaseSuppressesMigration(child))
					}
				})
			}
		}
	}
}

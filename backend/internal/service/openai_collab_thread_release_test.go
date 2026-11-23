//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// All seven boolean switches and all three local-ending modes, including
// combinations whose dependent features are inactive.
func collabReleaseSettings(combination int) *resilience.Settings {
	s := collabAffinitySettings()
	s.Collab = resilience.CollabSettings{
		IncrementalDeclarations: combination&1 != 0, Records: combination&2 != 0,
		TreeAffinity: combination&4 != 0, LosslessFirst: combination&8 != 0,
		Ladder: combination&16 != 0, RecordRouting: combination&32 != 0,
		StrictMarkerVeto: combination&64 != 0,
		LocalEnding:      resilience.CollabLocalEndingSettings{Mode: []resilience.CollabLocalEndingMode{resilience.CollabLocalEndingOff, resilience.CollabLocalEndingGroups, resilience.CollabLocalEndingAll}[combination/128], Groups: []int64{7}},
	}
	return s
}

// Shipped behavior: never use enableUnshippedCollabThreadBinding here.
func TestCollabThreadBindingReleaseGate(t *testing.T) {
	require.False(t, openAICollabThreadBindingEnabled)
	for migration := range 4 {
		for _, legacy := range []string{"", "legacy"} {
			f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
			f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
			f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
			local := resilience.NewCollabTreeAffinity(16, time.Hour)
			base := withOpenAILegacySessionHash(withOpenAINoStickyMigration(selectionReviewContext()), legacy)
			for combination := range 384 {
				settings := collabReleaseSettings(combination)
				ctx := WithOpenAICollabTreeAffinity(base, local, settings, 7, collabAffinityIdentity("child", "root"), resilience.CollabRoleChild, true)
				before := openAICollabAffinityFrom(ctx)
				require.Zero(t, collabMinAllocsPerRun(10, func() {
					if f.svc.PrepareOpenAICollabThreadBinding(ctx, 1, "v2:tree", PlatformOpenAI, OpenAIClientTransportHTTP, true) {
						panic("thread binding shipped")
					}
				}), "migration=%d legacy=%s switches=%d", migration, legacy, combination)
				require.Nil(t, openAICollabBindingFrom(ctx))
				require.Same(t, before, openAICollabAffinityFrom(ctx), "binding gate must preserve hints, leases and queue recovery")
				// Compare complete setup against the retained affinity features.
				// Those features own their existing context allocation; binding adds none.
				var runs []func()
				for _, prepare := range []bool{false, true} {
					runs = append(runs, func() {
						configured := WithOpenAICollabTreeAffinity(base, local, settings, 7, collabAffinityIdentity("child", "root"), resilience.CollabRoleChild, true)
						if prepare {
							f.svc.PrepareOpenAICollabThreadBinding(configured, 1, "v2:tree", PlatformOpenAI, OpenAIClientTransportHTTP, true)
						}
						collabSetupContext = configured
					})
				}
				// Warm both paths before measuring either, including shared state.
				for range 64 {
					for _, run := range runs {
						run()
					}
				}
				counts := []float64{collabMinAllocsPerRun(10, runs[0]), collabMinAllocsPerRun(10, runs[1])}
				require.Equal(t, counts[0], counts[1])
			}
		}
	}
	// The gate precedes context access, eligibility checks and key derivation.
	var unavailable *OpenAIGatewayService
	require.False(t, unavailable.PrepareOpenAICollabThreadBinding(nil, 0, "", "", "", false))
}

func TestCollabThreadBindingReleaseKeepsAffinity(t *testing.T) {
	f := newCollabThreadFixture(t, selectionFixtureAccount(1, 2), selectionFixtureAccount(2, 1))
	local := resilience.NewCollabTreeAffinity(32, time.Hour)
	settings := collabReleaseSettings(383)
	root := WithOpenAICollabTreeAffinity(selectionReviewContext(), local, settings, 7, collabAffinityIdentity("root", ""), resilience.CollabRoleRoot, true)
	require.False(t, f.svc.PrepareOpenAICollabThreadBinding(root, 1, "v2:tree", PlatformOpenAI, OpenAIClientTransportHTTP, true))
	NoteOpenAICollabAdmission(root, 1, false)
	NoteOpenAICollabSuccess(root, 1, false)
	child := WithOpenAICollabTreeAffinity(withOpenAINoStickyMigration(selectionReviewContext()), local, settings, 7, collabAffinityIdentity("child", "root"), resilience.CollabRoleChild, true)
	require.False(t, f.svc.PrepareOpenAICollabThreadBinding(child, 1, "v2:tree", PlatformOpenAI, OpenAIClientTransportHTTP, true))
	require.EqualValues(t, 1, openAICollabHint(child, 0))
	require.True(t, OpenAIQueueReselectionAllowed(child))
	identity := collabAffinityIdentity("root", "")
	lease := resilience.CollabDeliveryLease{Key: resilience.CollabDeliveryKey{Scope: identity.Scope, Thread: "child", Item: "item", Delivery: "delivery", Digest: "ciphertext"}, Issued: resilience.CollabIssuedToken{Scope: identity.Scope, Token: "token"}, AccountID: 1}
	event := resilience.CollabDeliveryEvent{Phase: resilience.CollabDeliveryPhaseWritten, EventType: "response.output_item.done", Executable: true, BytesWritten: 1}
	require.True(t, NoteOpenAICollabLeaseWritten(root, event, lease, true))
	require.True(t, openAICollabLeaseSuppressesMigration(child))
	ReleaseOpenAICollabLease(child, lease.Key, resilience.CollabLeaseReleaseConsumed)
	require.False(t, openAICollabLeaseSuppressesMigration(child))
}

// Compare shipped child ciphertext effects with today's ordinary tree writers,
// including detached session failures. Real Lua verifies operation parity.
func RunCollabReleaseParity(t *testing.T, cache GatewayCache, store ResilienceStore, leases SchedTrialLeases, reset func(), operations func() int64) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Session.StreakThreshold = 1 })
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for migration := range 4 {
		for combination := range 384 {
			var outcomes [2]string
			for path := range 2 {
				reset()
				resilience.ProcessNegativeCache().InvalidateGroup(7)
				f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
				f.svc.cache, f.svc.resilience.store, f.svc.selectionBoard.leases = cache, store, leases
				f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
				f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
				legacy := ""
				if combination%2 != 0 {
					legacy = "release-parent"
				}
				ctx, session := collabRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "release-child", false, false, false, legacy)
				local := resilience.NewCollabTreeAffinity(32, time.Hour)
				if path == 1 {
					ctx = WithOpenAICollabTreeAffinity(ctx, local, collabReleaseSettings(combination), 7, collabAffinityIdentity("release-child", "root"), resilience.CollabRoleChild, true)
				}
				before := operations()
				require.False(t, f.svc.PrepareOpenAICollabThreadBinding(ctx, 1, session, PlatformOpenAI, OpenAIClientTransportHTTP, true))
				require.Equal(t, before, operations())
				require.Nil(t, openAICollabBindingFrom(ctx))
				require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:"+session, 1, time.Hour))
				if legacy != "" {
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:"+legacy, 1, time.Hour))
				}
				before = operations()
				require.False(t, NoteOpenAICollabCiphertextFailure(ctx, 1, time.Now().Add(time.Minute)))
				f.svc.AvoidOpenAICollabStateAccount(ctx, 7, session, 1, true)
				avoided, err := store.SessionAvoid(ctx, 7, session)
				require.NoError(t, err)
				require.Contains(t, avoided, int64(1))
				threadAvoid, err := store.SessionAvoid(ctx, 7, openAIRejectionScope(ctx).session)
				require.NoError(t, err)
				require.Empty(t, threadAvoid)
				result := selectTrialRuntime(t, f, ctx, session)
				require.EqualValues(t, 2, result.Account.ID)
				CompleteOpenAIStickyChange(ctx, 2, true, false)
				require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, ptrInt64Selection(7), session, 2))
				result.ReleaseFunc()
				f.svc.collabSessionRecorder(ctx).Failure(context.Background(), 7, session, 2, resilience.CategoryTransient)
				count := operations() - before
				owner, missing, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
				outcomes[path] = fmt.Sprintf("account=%d wait=%v operations=%d owner=%d missing=%t err=%v bypass=%t", result.Account.ID, result.WaitPlan, count, owner, missing, err, resilience.StateFrom(ctx).StickyBypassChangesBinding())
				for _, call := range f.cache.casKeys {
					require.False(t, strings.Contains(call, "collab-thread-binding"))
				}
			}
			require.Equal(t, outcomes[0], outcomes[1], "migration=%d switches=%d", migration, combination)
		}
		t.Logf("migration=%d: 384 switch combinations, identical ciphertext effects, selection and operation counts", migration)
	}
}

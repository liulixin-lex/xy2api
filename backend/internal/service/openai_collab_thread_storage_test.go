//go:build unit

package service

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabThreadBindingTrialIdentity(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"delete", "promote", "touch_tree", "touch_thread", "touch_after_bind", "recorder"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
			f.cache.sessionBindings["openai:shared-session"] = 1
			ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "shared-session")
			b := openAICollabBindingFrom(ctx)
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, 1, leases)
			selectionSetTrial(f, 2, leases)
			treeMember, childMember := TrialLeaseSessionMember(b.session), TrialLeaseSessionMember(b.key)
			root := trialTestKey(1, "m", 7, treeMember)
			leases.held[root] = true
			req := OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), SessionHash: b.session, RequestedModel: "m"}
			ctx = context.WithValue(ctx, openAISelectionRequestKey{}, req)
			if mode == "touch_thread" || mode == "delete" || mode == "recorder" {
				require.NoError(t, b.write(ctx, 1, time.Hour))
				leases.held[trialTestKey(1, "m", 7, childMember)] = true
			}
			trial := f.svc.newSelectionTrial(ctx, req, 1)
			ctx = context.WithValue(ctx, selectionTrialContextKey{}, trial)
			if mode == "touch_after_bind" {
				require.NoError(t, b.write(ctx, 1, time.Hour))
				leases.held[trialTestKey(1, "m", 7, childMember)] = true
			}
			switch mode {
			case "delete":
				require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), b.session, 1))
				require.Zero(t, trial.owner)
				require.False(t, leases.held[trialTestKey(1, "m", 7, childMember)])
			case "recorder":
				r := f.svc.collabSessionRecorder(ctx)
				for range resilience.Current().Session.StreakThreshold {
					r.Failure(context.Background(), 7, b.session, 1, resilience.CategoryTransient)
				}
				require.Zero(t, trial.owner)
				require.False(t, leases.held[trialTestKey(1, "m", 7, childMember)])
			case "promote":
				require.NoError(t, b.write(ctx, 0, time.Hour))
				trial.owner = 0
				r, ok := trial.reserve(ctx, &f.repo.accounts[1], false)
				require.True(t, ok)
				r.promote(context.Background())
				require.True(t, leases.held[trialTestKey(2, "m", 7, childMember)])
				require.False(t, leases.held[trialTestKey(2, "m", 7, treeMember)])
			default:
				trial.touch(ctx, &f.repo.accounts[0])
				member := treeMember
				if mode == "touch_thread" || mode == "touch_after_bind" {
					member = childMember
				}
				require.Equal(t, []string{"touch:" + trialTestKey(1, "m", 7, member)}, leases.calls)
			}
			require.True(t, leases.held[root], "a child's cleanup cannot release the tree's seat")
		})
	}
}

// Compare the storage effects separately from the selection oracle so failed
// attempts and provisional claims are observed before completion can promote.
func TestCollabThreadBindingStorageOracle(t *testing.T) {
	random := rand.New(rand.NewSource(23))
	for i := range 300 {
		owner, target := int64(random.Intn(3)), int64(1+random.Intn(2))
		atomicCache, inherited := random.Intn(2) == 0, random.Intn(2) == 0
		action := i % 4
		ttl := []time.Duration{10 * time.Second, time.Hour}[random.Intn(2)]
		synctest.Test(t, func(t *testing.T) {
			outcome := func(on bool) string {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
				NewOpenAISelectionBoard(f.svc, func() schedquality.View { return f.view }, newSelectionTrialLeaseFake())
				cache := &collabEffectsCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
				f.svc.cache = cache
				if !atomicCache {
					f.svc.cache = struct{ GatewayCache }{cache}
				}
				session := "v2:shared-session"
				ctx := selectionReviewContext()
				var binding *openAICollabThreadBinding
				if on {
					ctx = prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), session)
					binding = openAICollabBindingFrom(ctx)
				}
				if !on || inherited {
					if owner > 0 {
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:"+session, owner, time.Minute))
					}
				} else {
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:"+session, 1, time.Minute))
					require.NoError(t, binding.write(ctx, owner, time.Minute))
				}
				tree, treeErr := cache.GetSessionAccountID(ctx, 7, "openai:"+session)
				time.Sleep(5 * time.Second)
				var err error
				switch action {
				case 0, 1:
					ctx = context.WithValue(ctx, openAIStickyPromotionKey{}, action == 1)
					err = f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), session, target, ttl)
				case 2:
					err = f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), session, ttl, target)
				case 3:
					err = f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), session, ttl)
				}
				id, readErr := f.svc.getStickySessionAccountID(ctx, ptrInt64Selection(7), session)
				remaining := time.Duration(0)
				if id > 0 {
					remaining = time.Until(cache.until["openai:"+session])
					if binding != nil && binding.present() {
						remaining = time.Until(binding.until)
					}
				}
				if on {
					after, afterErr := cache.GetSessionAccountID(ctx, 7, "openai:"+session)
					require.Equal(t, tree, after)
					require.Equal(t, treeErr, afterErr)
				}
				return fmt.Sprintf("owner=%d ttl=%v err=%v read=%v", id, remaining, err, readErr)
			}
			require.Equal(t, outcome(false), outcome(true), "case=%d owner=%d target=%d atomic=%v inherited=%v action=%d ttl=%v", i, owner, target, atomicCache, inherited, action, ttl)
		})
	}
}

func TestCollabThreadBindingPromotionKeepsTreeAlive(t *testing.T) {
	for _, operation := range []string{"promote", "detached_refresh", "detached_commit"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
				cache := &collabEffectsCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
				f.svc.cache = cache
				ctx := prepareAffinityTest(f, withOpenAILegacySessionHash(collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "legacy"), "v2:shared-session")
				b := openAICollabBindingFrom(ctx)
				for _, key := range []string{"openai:" + b.session, "openai:legacy"} {
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, key, 1, time.Minute))
				}
				require.NoError(t, b.write(ctx, 2, time.Minute))
				time.Sleep(10 * time.Second)
				switch operation {
				case "promote":
					ctx = context.WithValue(ctx, openAIStickyPromotionKey{}, true)
					require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), b.session, 2, time.Hour))
				case "detached_refresh":
					require.NoError(t, b.refresh(context.Background(), time.Hour, 2))
				case "detached_commit":
					versioned := collabThreadVersionedCache{b}
					expected, err := versioned.GetSessionBinding(ctx, 7, b.session)
					require.NoError(t, err)
					result, err := versioned.CommitSessionBinding(context.Background(), 7, b.session, expected, 3, time.Hour, BindingCommitOptions{})
					require.NoError(t, err)
					require.Equal(t, BindingCommitted, result.Status)
				}
				require.Equal(t, time.Hour, time.Until(cache.until["openai:"+b.session]))
				require.Equal(t, 10*time.Minute, time.Until(cache.until["openai:legacy"]))
				require.EqualValues(t, 1, cache.values["openai:"+b.session])
				require.EqualValues(t, 1, cache.values["openai:legacy"])
			})
		})
	}
}

func TestCollabThreadBindingLookupPreservesCacheGuard(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "shared-session")
	require.NoError(t, openAICollabBindingFrom(ctx).write(ctx, 1, time.Minute))
	f.svc.cache = nil
	id, missing, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), "shared-session")
	require.NoError(t, err)
	require.Zero(t, id)
	require.False(t, missing)
}

func TestCollabThreadBindingDeleteBookkeepingOracle(t *testing.T) {
	for _, capability := range []string{"plain", "atomic", "reporting"} {
		for _, ownerChecked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/owner=%t", capability, ownerChecked), func(t *testing.T) {
				outcome := func(on bool) bool {
					f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
					NewOpenAISelectionBoard(f.svc, func() schedquality.View { return f.view }, newSelectionTrialLeaseFake())
					cache := &collabEffectsCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
					f.svc.cache = struct{ GatewayCache }{cache}
					if capability == "atomic" {
						f.svc.cache = cache
					} else if capability == "reporting" {
						f.svc.cache = f.cache
					}
					session := "v2:shared-session"
					ctx := selectionReviewContext()
					require.NoError(t, f.svc.cache.SetSessionAccountID(ctx, 7, "openai:"+session, 1, time.Hour))
					if on {
						ctx = prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), session)
					}
					if ownerChecked {
						require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), session, 1))
					} else {
						require.NoError(t, f.svc.deleteStickySessionAccountID(ctx, ptrInt64Selection(7), session))
					}
					return resilience.StateFrom(ctx).StickyBypassChangesBinding()
				}
				require.Equal(t, outcome(false), outcome(true))
			})
		}
	}
}

func TestCollabThreadBindingDetachedChangeLease(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
	f.cache.sessionBindings["openai:shared-session"] = 1
	ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "shared-session")
	b := openAICollabBindingFrom(ctx)
	require.NoError(t, b.write(ctx, 1, time.Hour))
	cache := collabThreadVersionedCache{b}
	expected, err := cache.GetSessionBinding(ctx, 7, b.session)
	require.NoError(t, err)
	leases := newSelectionTrialLeaseFake()
	tree := trialTestKey(1, "m", 7, TrialLeaseSessionMember(b.session))
	child := trialTestKey(1, "m", 7, TrialLeaseSessionMember(b.key))
	leases.held[tree], leases.held[child] = true, true
	change := &openAIStickyChange{groupID: 7, session: b.session, key: "openai:" + b.session, kind: "migrate", expected: expected, target: 2, ttl: time.Hour, cache: cache, leases: leases, fromModel: "m"}
	change.mu.Lock()
	change.completeLocked(context.Background(), true, false, false)
	owner, _, err := b.effective(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, owner)
	require.True(t, leases.held[tree])
	require.False(t, leases.held[child])
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:shared-session"])
}

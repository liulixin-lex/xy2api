//go:build unit

package service

// Binding scenarios here exercise unfinished code through the test-only hook.
// They do not establish equivalence or enable thread bindings in this release.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

var collabTemporalSteps = flag.Int("collab-temporal-steps", 240, "random steps per thread binding oracle seed")

type CollabTemporalStore struct {
	Cache      GatewayCache
	Store      ResilienceStore
	Leases     SchedTrialLeases
	Reset      func()
	Advance    func(time.Duration)
	Operations func() int64
}

type collabTemporalAction struct {
	kind     string
	instance int
	account  int64
	ttl      time.Duration
}

type collabTemporalPlacement struct {
	ctx    context.Context
	result *AccountSelectionResult
}

// RunCollabTemporalOracle runs independent ordinary and enabled stores, each
// shared by two service instances. Every step checks its result, effective
// binding and network operation count before advancing to the next step.
func RunCollabTemporalOracle(t *testing.T, stores [2]CollabTemporalStore) {
	withResilienceSettings(t, nil)
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, seed := range []int64{71, 307, 991} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			var fixtures [2][2]*selectionRuntimeFixture
			var locals [2][2]*resilience.CollabTreeAffinity
			var pending [2][2]*collabTemporalPlacement
			var session string
			for path, storage := range stores {
				storage.Reset()
				for instance := range 2 {
					f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(3, 3))
					f.svc.cache, f.svc.resilience.store, f.svc.selectionBoard.leases = storage.Cache, storage.Store, storage.Leases
					f.loads.acquireResults = map[int64]bool{1: true, 2: true, 3: true}
					fixtures[path][instance], locals[path][instance] = f, resilience.NewCollabTreeAffinity(32, time.Hour)
					_, session = collabAtomicRequest(t, f, locals[path][instance], "temporal-child", path == 1, false)
				}
				require.NoError(t, storage.Cache.SetSessionAccountID(context.Background(), 7, "openai:"+session, 1, time.Hour))
			}
			// This is the normal-selection reproduction: abandon an eager
			// replacement, recover its predecessor, expire the claim, then retry
			// on the other instance with the predecessor busy and replacement free.
			actions := []collabTemporalAction{
				{kind: "disable", account: 1}, {kind: "select", instance: 0},
				{kind: "abandon", instance: 0}, {kind: "healthy", account: 1},
				{kind: "busy", account: 1}, {kind: "advance", ttl: 31 * time.Second},
				{kind: "select_deadline", instance: 1}, {kind: "promote", instance: 1},
				{kind: "healthy", account: 1},
			}
			random := rand.New(rand.NewSource(seed))
			kinds := []string{"select", "select_deadline", "select_wait", "claim", "promote", "abandon", "delete", "failure", "refresh", "busy", "disable", "healthy", "advance"}
			ttls := []time.Duration{time.Second, 29 * time.Second, 30 * time.Second, 31 * time.Second, time.Hour, time.Hour + time.Second}
			for range *collabTemporalSteps {
				actions = append(actions, collabTemporalAction{kind: kinds[random.Intn(len(kinds))], instance: random.Intn(2), account: int64(1 + random.Intn(3)), ttl: ttls[random.Intn(len(ttls))]})
			}
			coverage := map[string]int{}
			for step, action := range actions {
				coverage[action.kind]++
				var outcomes [2]string
				for path, storage := range stores {
					// The two simulated deployments must not share the package's
					// process-global negative cache with one another.
					resilience.ProcessNegativeCache().InvalidateGroup(7)
					f := fixtures[path][action.instance]
					ctx, _ := collabAtomicRequest(t, f, locals[path][action.instance], "temporal-child", path == 1, false)
					before := storage.Operations()
					outcome := ""
					switch action.kind {
					case "select", "select_deadline", "select_wait":
						if previous := pending[path][action.instance]; previous != nil && previous.result.ReleaseFunc != nil {
							previous.result.ReleaseFunc()
						}
						pending[path][action.instance] = nil
						selectionCtx := ctx
						if action.kind == "select_wait" {
							// Exercise the ordinary wait-plan branch after its short retry.
							selectionCtx = context.WithValue(ctx, openAILegacyStickyCapacityKey{}, &openAILegacyStickyCapacity{retried: true})
						}
						selectionCtx, cancel := context.WithTimeout(selectionCtx, 500*time.Millisecond)
						result, _, err := f.svc.SelectAccountWithSchedulerForCapability(selectionCtx, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
						cancel()
						outcome = fmt.Sprintf("error=%v", err)
						if result != nil {
							outcome += fmt.Sprintf(" account=%d acquired=%t wait=%+v", result.Account.ID, result.Acquired, result.WaitPlan)
							pending[path][action.instance] = &collabTemporalPlacement{ctx: ctx, result: result}
						}
					case "promote", "abandon":
						if previous := pending[path][action.instance]; previous != nil {
							if action.kind == "promote" && previous.result.Acquired {
								CompleteOpenAIStickyChange(previous.ctx, previous.result.Account.ID, true, false)
								outcome = fmt.Sprint(f.svc.BindStickySessionAfterProfitAdmission(previous.ctx, ptrInt64Selection(7), session, previous.result.Account.ID))
							}
							if previous.result.ReleaseFunc != nil {
								previous.result.ReleaseFunc()
							}
							pending[path][action.instance] = nil
						}
					case "claim":
						outcome = fmt.Sprint(f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), session, action.account, time.Hour))
					case "delete":
						outcome = fmt.Sprint(f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), session, action.account))
					case "refresh":
						outcome = fmt.Sprint(f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), session, action.ttl, action.account))
					case "failure":
						cache := storage.Cache.(OpenAIStickyVersionedCache)
						if b := openAICollabBindingFrom(ctx); b != nil {
							cache = collabThreadVersionedCache{b}
						}
						expected, err := cache.GetSessionBinding(ctx, 7, "openai:"+session)
						outcome = fmt.Sprint(err)
						if err == nil {
							written, err := cache.RecordSessionBindingFailure(ctx, 7, "openai:"+session, stickyChangeAvoidSession(session), expected, action.account, time.Minute)
							outcome = fmt.Sprintf("recorded=%t error=%v", written > 0, err)
						}
					case "busy", "disable", "healthy":
						for _, instance := range fixtures[path] {
							if action.kind != "busy" {
								instance.repo.accounts[action.account-1].Schedulable = action.kind == "healthy"
							}
							instance.loads.acquireResults[action.account] = action.kind != "busy"
						}
					case "advance":
						storage.Advance(action.ttl)
					}
					count := storage.Operations() - before
					owner, missing, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
					outcomes[path] = fmt.Sprintf("%s operations=%d owner=%d missing=%t lookup=%v", outcome, count, owner, missing, err)
				}
				require.Equal(t, outcomes[0], outcomes[1], "seed=%d step=%d action=%+v", seed, step, action)
			}
			for _, paths := range pending {
				for _, placement := range paths {
					if placement != nil && placement.result.ReleaseFunc != nil {
						placement.result.ReleaseFunc()
					}
				}
			}
			for _, kind := range kinds {
				require.Positive(t, coverage[kind], kind)
			}
			t.Logf("seed=%d steps=%d zero differences; coverage=%v", seed, len(actions), coverage)
		})
	}
}

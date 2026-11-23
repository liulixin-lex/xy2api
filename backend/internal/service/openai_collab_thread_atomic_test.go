//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func collabAtomicContext(t *testing.T, f *selectionRuntimeFixture, local *resilience.CollabTreeAffinity, thread string, on bool) context.Context {
	ctx, _ := collabAtomicRequest(t, f, local, thread, on, false)
	return ctx
}

func collabAtomicRequest(t *testing.T, f *selectionRuntimeFixture, local *resilience.CollabTreeAffinity, thread string, on, guardian bool, legacy ...string) (context.Context, string) {
	t.Helper()
	f.svc.cfg.JWT.Secret = "synthetic-affinity-secret"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 1, GroupID: ptrInt64Selection(7), Group: &Group{ID: 7, Platform: PlatformOpenAI}})
	c.Request.Header.Set("session_id", "root")
	c.Request.Header.Set("thread-id", thread)
	c.Request.Header.Set(codexParentThreadIDHeader, "root")
	c.Request.Header.Set(openAISubagentHeader, "collab_spawn")
	body := []byte(`{"model":"m","input":[{"type":"function_call_output","call_id":"call_example","output":[{"type":"encrypted_content","encrypted_content":"synthetic"}]}]}`)
	if guardian {
		body = []byte(`{"model":"m","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"synthetic"}]}]}`)
	}
	session := f.svc.GenerateSessionHash(c, body)
	ctx := WithOpenAIEncryptedContentMigrationGuard(c.Request.Context(), body)
	ctx = f.svc.WithOpenAIGuardianParentAffinity(ctx, c, body, "m")
	ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
	if len(legacy) > 0 {
		ctx = withOpenAILegacySessionHash(ctx, legacy[0])
	}
	cfg := collabAffinitySettings()
	cfg.Collab.TreeAffinity = on
	ctx = WithOpenAICollabTreeAffinity(ctx, local, cfg, 7, collabAffinityIdentity(thread, "root"), resilience.CollabRoleChild, true)
	active := on && !f.svc.openAISessionHashReadOldFallbackEnabled() && !f.svc.openAISessionHashDualWriteOldEnabled() && f.svc.openAILegacySessionCacheKey(ctx, session) == ""
	require.Equal(t, active, f.svc.PrepareOpenAICollabThreadBinding(ctx, 1, session, PlatformOpenAI, OpenAIClientTransportHTTP, true))
	return ctx, session
}

// RunCollabAtomicStorage is shared with the repository-backed external test so
// the scheduler and detached writers exercise the actual binding scripts.
func RunCollabAtomicStorage(t *testing.T, cache GatewayCache, store ResilienceStore, leases SchedTrialLeases, advance func(time.Duration), members func(int64) []string, operations func() int64) {
	withResilienceSettings(t, nil)
	fixture := func(t *testing.T, name string) (*selectionRuntimeFixture, context.Context, context.Context) {
		f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(3, 3))
		f.svc.cache = cache
		f.svc.resilience.store = store
		f.svc.selectionBoard.leases = leases
		a := collabAtomicContext(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), name, true)
		b := openAICollabBindingFrom(a)
		require.NoError(t, cache.SetSessionAccountID(a, 7, "openai:"+b.session, 1, time.Hour))
		require.NoError(t, b.write(a, 0, time.Hour))
		other := collabAtomicContext(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), name, true)
		return f, a, other
	}
	t.Run("overlapping_claims", func(t *testing.T) {
		f, a, b := fixture(t, t.Name())
		binding := openAICollabBindingFrom(a)
		for i, ctx := range []context.Context{a, b} {
			require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), binding.session, int64(i+2), time.Hour))
		}
		id, err := cache.GetSessionAccountID(a, 7, binding.key)
		require.NoError(t, err)
		require.EqualValues(t, 2, id)
		advance(31 * time.Second)
		_, err = cache.GetSessionAccountID(a, 7, binding.key)
		require.ErrorIs(t, err, ErrStickySessionNotFound, "unpromoted claim must expire")
	})
	t.Run("inherited_refresh_ttl", func(t *testing.T) {
		f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
		f.svc.cache = cache
		f.svc.selectionBoard.leases = leases
		ctx := collabAtomicContext(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), t.Name(), true)
		binding := openAICollabBindingFrom(ctx)
		require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:"+binding.session, 1, time.Minute))
		require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), binding.session, time.Hour, 1))
		_, err := cache.GetSessionAccountID(ctx, 7, binding.key)
		require.ErrorIs(t, err, ErrStickySessionNotFound)
		_, remaining, err := cache.(gatewaySessionBindingReader).GetSessionAccountIDWithTTL(ctx, 7, "openai:"+binding.session)
		require.NoError(t, err)
		require.InDelta(t, time.Hour.Milliseconds(), remaining.Milliseconds(), 100)
	})
	t.Run("generation_and_failure_scope", func(t *testing.T) {
		f, a, b := fixture(t, t.Name())
		binding := openAICollabBindingFrom(a)
		require.NoError(t, binding.write(a, 2, time.Hour))
		one := collabThreadVersionedCache{binding}
		expected, err := one.GetSessionBinding(a, 7, "")
		require.NoError(t, err)
		written, err := one.RecordSessionBindingFailure(a, 7, "", stickyChangeAvoidSession(binding.session), expected, 3, time.Minute)
		require.NoError(t, err)
		require.Positive(t, written)
		require.Contains(t, f.svc.stickyChangeAvoid(a, ptrInt64Selection(7), binding.session), int64(3))
		soft, err := store.SessionAvoid(a, 7, binding.scope)
		require.NoError(t, err)
		require.NotContains(t, soft, int64(3))
		two := openAICollabBindingFrom(b)
		require.NoError(t, two.deleteOwner(b, 2))
		require.NoError(t, two.write(b, 2, time.Hour))
		result, err := one.CommitSessionBinding(a, 7, "", expected, 3, time.Hour, BindingCommitOptions{})
		require.NoError(t, err)
		require.Equal(t, BindingChanged, result.Status)
		written, err = one.RecordSessionBindingFailure(a, 7, "", stickyChangeAvoidSession(binding.session), expected, 1, time.Minute)
		require.NoError(t, err)
		require.Zero(t, written, "a stale generation cannot record a failure")
		expected, err = one.GetSessionBinding(a, 7, "")
		require.NoError(t, err)
		result, err = one.CommitSessionBinding(a, 7, "", expected, 3, time.Hour, BindingCommitOptions{FailureScope: stickyChangeAvoidSession(binding.session)})
		require.NoError(t, err)
		require.Equal(t, BindingCommitted, result.Status)
		require.NotContains(t, f.svc.stickyChangeAvoid(a, ptrInt64Selection(7), binding.session), int64(3))
	})
	t.Run("completed_plan", func(t *testing.T) {
		f, a, _ := fixture(t, t.Name())
		binding := openAICollabBindingFrom(a)
		r := selectTrialRuntime(t, f, a, binding.session)
		CompleteOpenAIStickyChange(a, r.Account.ID, true, false)
		r.ReleaseFunc()
		require.True(t, pendingOpenAIStickyChange(a).done)
		require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(a, ptrInt64Selection(7), binding.session, r.Account.ID))
		id, _, err := binding.effective(a)
		require.NoError(t, err)
		require.Equal(t, r.Account.ID, id)
	})
	t.Run("storage_operations", func(t *testing.T) {
		for _, alias := range []bool{false, true} {
			for _, action := range []string{"lookup", "claim", "refresh", "delete", "plan"} {
				var counts []int64
				for _, on := range []bool{false, true} {
					f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
					f.svc.cache, f.svc.selectionBoard.leases = cache, leases
					f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = alias
					f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = alias
					legacy := ""
					if alias {
						legacy = "synthetic-legacy"
					}
					a, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), fmt.Sprintf("operation-%s-%t-%t", action, alias, on), on, false, legacy)
					require.NoError(t, cache.SetSessionAccountID(a, 7, "openai:"+session, 1, time.Hour))
					if alias {
						require.NoError(t, cache.SetSessionAccountID(a, 7, "openai:"+legacy, 1, time.Hour))
					}
					if binding := openAICollabBindingFrom(a); binding != nil {
						require.NoError(t, binding.write(a, 1, time.Hour))
					}
					before := operations()
					switch action {
					case "lookup":
						_, _, _ = f.svc.lookupStickySessionAccount(a, ptrInt64Selection(7), session)
					case "claim":
						require.NoError(t, f.svc.setStickySessionAccountID(a, ptrInt64Selection(7), session, 1, time.Hour))
					case "refresh":
						require.NoError(t, f.svc.refreshStickySessionTTL(a, ptrInt64Selection(7), session, time.Hour, 1))
					case "delete":
						require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(a, ptrInt64Selection(7), session, 1))
					case "plan":
						f.svc.prepareOpenAIStickyChange(a, ptrInt64Selection(7), session)
					}
					counts = append(counts, operations()-before)
				}
				require.Equal(t, counts[0], counts[1], "alias=%t operation=%s", alias, action)
				t.Logf("alias=%t operation=%s tree=%d thread=%d", alias, action, counts[0], counts[1])
			}
		}
	})
	t.Run("pending_cas", func(t *testing.T) {
		_, a, b := fixture(t, t.Name())
		one, two := collabThreadVersionedCache{openAICollabBindingFrom(a)}, collabThreadVersionedCache{openAICollabBindingFrom(b)}
		ea, _ := one.GetSessionBinding(a, 7, "")
		eb, _ := two.GetSessionBinding(b, 7, "")
		first, err := one.CommitSessionBinding(a, 7, "", ea, 2, time.Hour, BindingCommitOptions{})
		require.NoError(t, err)
		require.Equal(t, BindingCommitted, first.Status)
		second, err := two.CommitSessionBinding(b, 7, "", eb, 3, time.Hour, BindingCommitOptions{})
		require.NoError(t, err)
		require.Equal(t, BindingChanged, second.Status)
	})
	t.Run("stale_local_read", func(t *testing.T) {
		f, a, b := fixture(t, t.Name())
		one := openAICollabBindingFrom(a)
		require.NoError(t, one.write(a, 2, time.Hour))
		require.NoError(t, openAICollabBindingFrom(b).write(b, 3, time.Hour))
		returning := collabAtomicContext(t, f, one.affinity.store, t.Name(), true)
		id, _, err := openAICollabBindingFrom(returning).effective(returning)
		require.NoError(t, err)
		require.EqualValues(t, 3, id)
	})
	t.Run("delete_then_refresh", func(t *testing.T) {
		for initial := int64(1); initial <= 3; initial++ {
			for target := int64(1); target <= 3; target++ {
				f, a, b := fixture(t, fmt.Sprintf("%s/%d/%d", t.Name(), initial, target))
				binding := openAICollabBindingFrom(a)
				require.NoError(t, binding.write(a, initial, time.Minute))
				b = collabAtomicContext(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), fmt.Sprintf("%s/%d/%d", t.Name(), initial, target), true)
				_, _, _ = openAICollabBindingFrom(b).effective(b)
				require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(a, ptrInt64Selection(7), binding.session, initial))
				require.NoError(t, f.svc.refreshStickySessionTTL(b, ptrInt64Selection(7), binding.session, time.Hour, target))
				id, err := cache.GetSessionAccountID(a, 7, binding.key)
				require.NoError(t, err)
				require.Equal(t, target, id)
			}
		}
	})
	t.Run("concurrent_commits", func(t *testing.T) {
		_, a, b := fixture(t, t.Name())
		contexts := []context.Context{a, b}
		var expected [2]StickySessionBinding
		for i, ctx := range contexts {
			expected[i], _ = (collabThreadVersionedCache{openAICollabBindingFrom(ctx)}).GetSessionBinding(ctx, 7, "")
		}
		var results [2]BindingCommitResult
		var errs [2]error
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, ctx := range contexts {
			wg.Go(func() {
				<-start
				results[i], errs[i] = (collabThreadVersionedCache{openAICollabBindingFrom(ctx)}).CommitSessionBinding(ctx, 7, "", expected[i], int64(i+2), time.Hour, BindingCommitOptions{})
			})
		}
		close(start)
		wg.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		require.ElementsMatch(t, []BindingCommitStatus{BindingCommitted, BindingChanged}, []BindingCommitStatus{results[0].Status, results[1].Status})
	})
	t.Run("inherited_trial", func(t *testing.T) {
		f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
		f.svc.cache = cache
		selectionSetTrial(f, 1, newSelectionTrialLeaseFake())
		f.svc.selectionBoard.leases = leases
		local := resilience.NewCollabTreeAffinity(32, time.Hour)
		a := collabAtomicContext(t, f, local, t.Name(), true)
		binding := openAICollabBindingFrom(a)
		require.NoError(t, cache.SetSessionAccountID(a, 7, "openai:"+binding.session, 1, time.Hour))
		member := TrialLeaseSessionMember(binding.session)
		ok, _, err := leases.Acquire(a, 1, "m", 7, member, time.Now())
		require.NoError(t, err)
		require.True(t, ok)
		for range 2 {
			ctx := collabAtomicContext(t, f, local, t.Name(), true)
			result := selectTrialRuntime(t, f, ctx, binding.session)
			require.EqualValues(t, 1, result.Account.ID)
			result.ReleaseFunc()
			require.False(t, openAICollabBindingFrom(ctx).present())
			advance(6 * time.Minute)
		}
		_, err = leases.Room(a, 1, "m", 7, "r:another", time.Now())
		require.NoError(t, err)
		require.Contains(t, members(1), "7:"+member)
	})
	t.Run("overlapping_placement", func(t *testing.T) {
		f, a, b := fixture(t, t.Name())
		selectionSetTrial(f, 2, newSelectionTrialLeaseFake())
		selectionSetTrial(f, 3, newSelectionTrialLeaseFake())
		f.svc.selectionBoard.leases = leases
		binding := openAICollabBindingFrom(a)
		var results []*AccountSelectionResult
		for i, ctx := range []context.Context{a, b} {
			target := int64(i + 2)
			result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", binding.session, "m", map[int64]struct{}{1: {}, 5 - target: {}}, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
			require.NoError(t, err)
			require.Equal(t, target, result.Account.ID)
			results = append(results, result)
		}
		for i, ctx := range []context.Context{a, b} {
			CompleteOpenAIStickyChange(ctx, int64(i+2), true, false)
			results[i].ReleaseFunc()
		}
		id, err := cache.GetSessionAccountID(a, 7, binding.key)
		require.NoError(t, err)
		require.EqualValues(t, 2, id)
		require.NotContains(t, members(3), "7:"+TrialLeaseSessionMember(binding.key), "losing placement must not promote its trial member")
		require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(a, ptrInt64Selection(7), binding.session, 2))
		id, _, err = binding.effective(a)
		require.NoError(t, err)
		require.EqualValues(t, 2, id, "completed plan still defers owner deletion")
	})
}

//go:build unit

package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabThreadBindingProductionActivation(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	f.svc.cfg.JWT.Secret = "synthetic-affinity-secret"
	engine := NewSchedQualityEngineService(nil, nil, nil, nil, nil)
	engine.SetTrialLeases(newSelectionTrialLeaseFake())
	ProvideOpenAISelectionBoard(f.svc, engine)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 1, GroupID: ptrInt64Selection(7), Group: &Group{ID: 7, Platform: PlatformOpenAI}})
	c.Request.Header.Set("session_id", "root")
	c.Request.Header.Set("thread-id", "child")
	c.Request.Header.Set(codexParentThreadIDHeader, "root")
	body := []byte(`{"model":"m","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"synthetic"}]}]}`)
	session := f.svc.GenerateSessionHash(c, body)
	require.True(t, openAIAtomicStickySession(session))
	ctx := WithOpenAIEncryptedContentMigrationGuard(c.Request.Context(), body)
	ctx = f.svc.WithOpenAIGuardianParentAffinity(ctx, c, body, "m")
	_, guardian := openAIGuardianParentAffinityFromContext(ctx)
	require.True(t, guardian)
	require.NotEmpty(t, openAIRejectionScope(ctx).session)
	identity := resilience.CollabTreeHint{
		Scope:  resilience.CollabRecordScope{Tenant: f.svc.CollabDigest(resilience.CollabDigestDomainScope, "tenant"), Group: f.svc.CollabDigest(resilience.CollabDigestDomainScope, "group"), Tree: f.svc.CollabDigest(resilience.CollabDigestDomainTree, "root")},
		Thread: f.svc.CollabDigest(resilience.CollabDigestDomainThread, "child"), Parent: f.svc.CollabDigest(resilience.CollabDigestDomainThread, "root"),
	}
	ctx = WithOpenAICollabTreeAffinity(ctx, resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), 7, identity, resilience.CollabRoleChild, openAIRequestHasEncryptedState(body))
	require.True(t, f.svc.PrepareOpenAICollabThreadBinding(ctx, 1, session, PlatformOpenAI, OpenAIClientTransportHTTP, true))
	require.NotNil(t, openAICollabBindingFrom(ctx))
}

// Expiry and claim semantics match the scalar binding store. No listener is needed.
type collabEffectsCache struct {
	GatewayCache
	values map[string]int64
	until  map[string]time.Time
}

func (c *collabEffectsCache) GetSessionAccountID(ctx context.Context, group int64, key string) (int64, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "read", 0, 0)
		defer finish()
	}

	if time.Now().Before(c.until[key]) {
		return c.values[key], nil
	}
	return 0, ErrStickySessionNotFound
}
func (c *collabEffectsCache) GetSessionAccountIDWithTTL(ctx context.Context, group int64, key string) (int64, time.Duration, error) {
	id, err := c.GetSessionAccountID(ctx, group, key)
	return id, time.Until(c.until[key]), err
}
func (c *collabEffectsCache) SetSessionAccountID(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "set", id, ttl)
		defer finish()
	}

	c.values[key], c.until[key] = id, time.Now().Add(ttl)
	return nil
}
func (c *collabEffectsCache) RefreshSessionTTL(ctx context.Context, group int64, key string, ttl time.Duration) error {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "refresh", 0, ttl)
		defer finish()
	}

	if _, err := c.GetSessionAccountID(ctx, group, key); err == nil {
		c.until[key] = time.Now().Add(ttl)
	}
	return nil
}
func (c *collabEffectsCache) ClaimSessionAccountID(ctx context.Context, group int64, key string, id int64, ttl time.Duration, promote bool) (int64, error) {
	if id <= 0 || ttl <= 0 {
		return 0, errors.New("invalid sticky binding claim")
	}
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "claim", id, ttl)
		defer finish()
	}

	owner, err := c.GetSessionAccountID(ctx, group, key)
	if !promote {
		ttl = min(ttl, 30*time.Second)
	}
	if err != nil || owner == id && promote {
		_ = c.SetSessionAccountID(ctx, group, key, id, ttl)
		return id, nil
	}
	return owner, nil
}
func (c *collabEffectsCache) DeleteSessionAccountIDIfOwner(ctx context.Context, group int64, key string, id int64) error {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "delete", id, 0)
		defer finish()
	}

	if owner, _ := c.GetSessionAccountID(ctx, group, key); owner == id {
		delete(c.values, key)
		delete(c.until, key)
	}
	return nil
}
func (c *collabEffectsCache) DeleteSessionAccountID(ctx context.Context, group int64, key string) error {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "delete", 0, 0)
		defer finish()
	}

	delete(c.values, key)
	delete(c.until, key)
	return nil
}

func TestCollabThreadBindingClaimAndRefresh(t *testing.T) {
	for _, initial := range []string{"none", "own", "tree", "missing"} {
		for _, action := range []string{"claim", "promote", "refresh", "refresh_other"} {
			t.Run(initial+"/"+action, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
					cache := &collabEffectsCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
					f.svc.cache = cache
					session := "v2:shared-session"
					if initial == "tree" {
						require.NoError(t, cache.SetSessionAccountID(context.Background(), 7, "openai:"+session, 1, time.Minute))
					}
					ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), session)
					b := openAICollabBindingFrom(ctx)
					if initial == "own" || initial == "none" {
						id := int64(1)
						if initial == "none" {
							id = 0
						}
						require.NoError(t, b.write(ctx, id, time.Minute))
					}
					time.Sleep(10 * time.Second)
					owner := int64(1)
					if action == "refresh_other" {
						owner = 2
					}
					if action == "promote" {
						ctx = context.WithValue(ctx, openAIStickyPromotionKey{}, true)
					}
					if action == "refresh" || action == "refresh_other" {
						require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), session, time.Hour, owner))
					} else {
						require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), session, owner, time.Hour))
					}
					id, _, err := b.effective(ctx)
					require.NoError(t, err)
					want := owner
					if initial == "own" || initial == "tree" {
						want = 1
					}
					require.Equal(t, want, id)
					unchanged := action == "claim" || action == "refresh_other"
					if initial == "tree" {
						require.False(t, b.present(), "an existing tree claim must stay inherited")
						ttl := time.Hour
						if unchanged {
							ttl = 50 * time.Second
						}
						require.Equal(t, ttl, time.Until(cache.until["openai:"+session]))
					} else {
						ttl := time.Hour
						if action == "claim" {
							ttl = 30 * time.Second
						}
						if initial == "own" && unchanged {
							ttl = 50 * time.Second
						}
						require.Equal(t, ttl, time.Until(b.until))
					}
				})
			})
		}
	}
}

func TestCollabThreadBindingInvalidExpectedOwner(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	cache := &collabEffectsCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
	f.svc.cache = cache
	ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "v2:shared-session")
	b := openAICollabBindingFrom(ctx)
	require.NoError(t, b.write(ctx, 1, time.Minute))
	require.Error(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), b.session, time.Hour, 0))
	owner, _, err := b.effective(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, owner)
}

func TestCollabThreadBindingTreeTTLOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		cache := &collabEffectsCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
		f.svc.cache = cache
		ctx := prepareAffinityTest(f, withOpenAILegacySessionHash(collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "legacy"), "v2:shared-session")
		b := openAICollabBindingFrom(ctx)
		require.NoError(t, b.write(ctx, 2, time.Hour))
		for _, key := range []string{"openai:v2:shared-session", "openai:legacy"} {
			require.NoError(t, cache.SetSessionAccountID(ctx, 7, key, 1, time.Minute))
		}
		time.Sleep(30 * time.Second)
		require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), b.session, time.Hour, 2))
		require.EqualValues(t, 1, cache.values["openai:v2:shared-session"])
		require.EqualValues(t, 1, cache.values["openai:legacy"])
		require.Equal(t, time.Hour, time.Until(cache.until["openai:v2:shared-session"]))
		require.Equal(t, 10*time.Minute, time.Until(cache.until["openai:legacy"]))
		delete(cache.until, "openai:v2:shared-session")
		delete(cache.until, "openai:legacy")
		require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), b.session, time.Hour, 2))
		require.NotContains(t, cache.until, "openai:v2:shared-session")
		require.NotContains(t, cache.until, "openai:legacy")
	})
}

func TestCollabThreadBindingDeleteEffects(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, operation := range []string{"delete", "owner", "avoid", "ciphertext", "recorder"} {
		for _, pending := range []bool{false, true} {
			t.Run(operation+"/pending="+map[bool]string{false: "no", true: "yes"}[pending], func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
				f.cache.sessionBindings["openai:shared-session"] = 1
				ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "shared-session")
				b := openAICollabBindingFrom(ctx)
				leases := newSelectionTrialLeaseFake()
				selectionSetTrial(f, 1, leases)
				req := OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), SessionHash: b.session, RequestedModel: "m"}
				ctx = context.WithValue(ctx, openAISelectionRequestKey{}, req)
				trial := f.svc.newSelectionTrial(ctx, req, 1)
				ctx = context.WithValue(ctx, selectionTrialContextKey{}, trial)
				root := trialTestKey(1, "m", 7, TrialLeaseSessionMember(b.session))
				child := trialTestKey(1, "m", 7, TrialLeaseSessionMember(b.key))
				leases.held[root], leases.held[child] = true, true
				change := &openAIStickyChange{groupID: 7, session: b.session, expected: StickySessionBinding{AccountID: 1}}
				if pending {
					resilience.StateFrom(ctx).SetStickyChange(change)
				}
				switch operation {
				case "delete":
					require.NoError(t, f.svc.deleteStickySessionAccountID(ctx, req.GroupID, b.session))
				case "owner":
					require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, req.GroupID, b.session, 1))
				case "avoid":
					f.svc.AvoidOpenAIStateAccount(ctx, 7, b.scope, 1)
				case "ciphertext":
					require.True(t, NoteOpenAICollabCiphertextFailure(ctx, 1, time.Now().Add(time.Minute)))
				case "recorder":
					rec := f.svc.collabSessionRecorder(ctx)
					for range resilience.Current().Session.StreakThreshold {
						rec.Failure(context.Background(), 7, b.session, 1, resilience.CategoryTransient)
					}
				}
				if pending {
					require.False(t, b.present())
					require.Equal(t, operation != "delete", change.ownerReleased)
					require.False(t, resilience.StateFrom(ctx).StickyBypassChangesBinding())
				} else {
					require.True(t, b.present())
					require.True(t, resilience.StateFrom(ctx).StickyBypassChangesBinding())
					if operation != "delete" {
						require.Zero(t, trial.owner)
						require.False(t, leases.held[child])
					}
				}
				require.True(t, leases.held[root], "the tree still owns its trial seat")
				require.EqualValues(t, 1, f.cache.sessionBindings["openai:shared-session"])
			})
		}
	}
}

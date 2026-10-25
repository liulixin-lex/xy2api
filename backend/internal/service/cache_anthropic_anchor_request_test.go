//go:build unit

package service

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func l04RequestFixture(t *testing.T, caps CacheM4BuildCapabilities, trace bool) (*GatewayService, *gin.Context, *APIKey, *ParsedRequest) {
	t.Helper()
	policies, store, _ := m4PolicyFixture(t, WithCacheM4BuildCapabilities(caps), WithProviderCacheTrace())
	store.rows["cache_anthropic_identity_policy"] = `{"rev":7,"spec":{"anchor_group_ids":[31,77],"gemini_anchor":true,"rollout_percent":100,"max_inflight_per_anchor":2}}`
	if trace {
		store.rows["cache_observability_policy"] = `{"rev":1,"spec":{"enabled":true,"platforms":["anthropic","gemini"],"sample_percent":100}}`
	}
	require.NoError(t, policies.Refresh(context.Background()))
	cfg := testConfig()
	cfg.JWT.Secret = "l04-synthetic-key"
	group := &Group{ID: 31, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	key := &APIKey{ID: 101, GroupID: &group.ID, Group: group}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Set("api_key", key)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","system":"common system","messages":[{"role":"user","content":"first user"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	parsed.GroupID = key.GroupID
	svc := &GatewayService{cfg: cfg, providerTraceEngine: &OpenAIGatewayService{cfg: cfg, cachePolicies: policies}}
	svc.BeginProviderCacheTrace(c, PlatformAnthropic, "messages", parsed.Body.Bytes(), parsed)
	return svc, c, key, parsed
}

func TestL04RequestObservationIndependentAndTenantScoped(t *testing.T) {
	var hashes []string
	for _, trace := range []bool{false, true} {
		s, c, key, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, trace)
		require.Same(t, key, s.PrepareCacheAnchor(c, key, p, PlatformAnthropic, false))
		hash := s.GenerateSessionHash(p)
		require.Equal(t, p.AnchorV2.value, hash)
		require.True(t, s.CacheAnchorApplied(p))
		hashes = append(hashes, hash)
		s.BindProviderCacheTraceSession(c, hash, "")
		if trace {
			tr := cacheTraceFromGin(c)
			require.Equal(t, "anchor_v2", tr.data.IDSource)
			require.Equal(t, "none", tr.data.L04Skip)
			require.NotEmpty(t, tr.data.SessionDigest)
		} else {
			require.Nil(t, cacheTraceFromGin(c))
		}
		s.ReleaseCacheAnchor(p)
		require.Empty(t, s.anchorInflight.entries)
		key2 := *key
		key2.ID++
		s.PrepareCacheAnchor(c, &key2, p, PlatformAnthropic, false)
		require.NotEqual(t, hash, s.GenerateSessionHash(p))
		s.ReleaseCacheAnchor(p)
	}
	require.Equal(t, hashes[0], hashes[1], "observation must not affect decisions")
}

func TestL04RequestDisabledAndCountTokensZeroWork(t *testing.T) {
	for _, caps := range []CacheM4BuildCapabilities{{}, {L04: true}} {
		s, c, key, p := l04RequestFixture(t, caps, false)
		if caps.L04 {
			q := *s.providerTraceEngine.cachePolicies.Snapshot()
			q.master = false
			s.providerTraceEngine.cachePolicies.snapshot.Store(&q)
			c.Set(cachePolicySnapshotGinKey, &q)
		}
		before := s.GenerateSessionHash(p)
		key.Group.ClaudeCodeOnly = true // resolving this without an early return panics (nil repo)
		require.Same(t, key, s.PrepareCacheAnchor(c, key, p, PlatformAnthropic, false))
		require.Equal(t, before, s.GenerateSessionHash(p))
		require.Nil(t, p.cacheL04)
		require.Empty(t, s.anchorInflight.entries)
		require.Zero(t, testing.AllocsPerRun(100, func() { s.PrepareCacheAnchor(c, key, p, PlatformAnthropic, false) }))
	}
	s, c, key, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
	key.Group.ClaudeCodeOnly = true
	require.Same(t, key, s.PrepareCacheAnchor(c, key, p, PlatformAnthropic, true))
	require.Nil(t, p.cacheL04)
	require.Empty(t, p.AnchorV2.value)
	require.Empty(t, s.anchorInflight.entries)
	require.Equal(t, "count_tokens", cacheTraceFromGin(c).data.L04Skip)
}

func TestL04RequestSkipOrderAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, skip string
		mutate     func(*GatewayService, *APIKey, *ParsedRequest)
	}{
		{"missing key", "no_api_key", func(_ *GatewayService, k *APIKey, _ *ParsedRequest) { k.ID = 0 }},
		{"composite", "not_anthropic_group", func(_ *GatewayService, k *APIKey, _ *ParsedRequest) { k.Group.Platform = PlatformComposite }},
		{"not listed", "not_in_group", func(_ *GatewayService, k *APIKey, _ *ParsedRequest) { k.Group.ID = 32 }},
		{"empty text", "empty_anchor", func(_ *GatewayService, _ *APIKey, p *ParsedRequest) {
			require.NoError(t, p.ReplaceBody([]byte(`{"model":"claude-sonnet-4-5","messages":[]}`)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
			tc.mutate(s, k, p)
			s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
			require.Equal(t, tc.skip, p.cacheL04.skip)
			require.Empty(t, p.AnchorV2.value)
			s.GenerateSessionHash(p)
			require.False(t, s.CacheAnchorApplied(p))
		})
	}
	s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
	s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
	p.AnchorV2.servingGroupID = 99
	require.NotEqual(t, p.AnchorV2.value, s.GenerateSessionHash(p))
	require.Equal(t, "scope_mismatch", p.cacheL04.skip)
	require.Empty(t, s.anchorInflight.entries)
}

func TestL04RequestEarlierTiersAndFanin(t *testing.T) {
	for _, body := range []string{
		`{"model":"claude-sonnet-4-5","metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_11111111-2222-4333-8444-555555555555"},"messages":[{"role":"user","content":"first"}]}`,
		`{"model":"claude-sonnet-4-5","system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"first"}]}`,
	} {
		s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
		require.NoError(t, p.ReplaceBody([]byte(body)))
		s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
		hash := s.GenerateSessionHash(p)
		require.NotEmpty(t, hash)
		require.NotEqual(t, p.AnchorV2.value, hash)
		require.Equal(t, "earlier_tier", p.cacheL04.skip)
		require.Empty(t, s.anchorInflight.entries)
	}
	s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
	s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
	hash := s.GenerateSessionHash(p)
	var requests []*ParsedRequest
	for i := 0; i < 2; i++ {
		q, err := ParseGatewayRequest(NewRequestBodyRef(p.Body.Bytes()), PlatformAnthropic)
		require.NoError(t, err)
		q.GroupID = k.GroupID
		s.PrepareCacheAnchor(c, k, q, PlatformAnthropic, false)
		got := s.GenerateSessionHash(q)
		if i == 0 {
			require.Equal(t, hash, got)
		} else {
			require.NotEqual(t, hash, got)
			require.Equal(t, "fanin_cap", q.cacheL04.skip)
		}
		requests = append(requests, q)
	}
	for _, q := range requests {
		s.ReleaseCacheAnchor(q)
	}
	s.ReleaseCacheAnchor(p)
	require.Empty(t, s.anchorInflight.entries)
}

type l04NamespaceCache struct {
	*mockGatewayCacheForPlatform
	writes []int64
}

func (m *l04NamespaceCache) GetSessionAccountID(ctx context.Context, gid int64, hash string) (int64, error) {
	id, err := m.mockGatewayCacheForPlatform.GetSessionAccountID(ctx, gid, hash)
	if err != nil {
		return 0, ErrStickySessionNotFound
	}
	return id, nil
}
func (m *l04NamespaceCache) SetSessionAccountID(ctx context.Context, gid int64, hash string, id int64, ttl time.Duration) error {
	m.writes = append(m.writes, gid)
	return m.mockGatewayCacheForPlatform.SetSessionAccountID(ctx, gid, hash, id, ttl)
}

func TestL04ClaudeCodeFallbackStickyNamespaceC2C3(t *testing.T) {
	for _, profit := range []bool{false, true} {
		t.Run(map[bool]string{false: "C2_layer3_wait", true: "C3_profit_gate"}[profit], func(t *testing.T) {
			s, c, key, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
			fallback := gatewayProfitTestGroup(77, PlatformAnthropic)
			fallback.ProfitControlEnabled = profit
			key.Group.RateMultiplier = 0.5 // Production billing origin supplies the profit denominator.
			key.Group.ClaudeCodeOnly = true
			key.Group.FallbackGroupID = &fallback.ID
			s.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{31: key.Group, 77: fallback}}
			cheap := gatewayProfitTestAccount(1, PlatformAnthropic, 0.2, 77)
			expensive := gatewayProfitTestAccount(2, PlatformAnthropic, 0.9, 77)
			s.accountRepo = &mockAccountRepoForPlatform{accounts: []Account{expensive, cheap}, accountsByID: map[int64]*Account{1: &cheap, 2: &expensive}}
			cache := &l04NamespaceCache{mockGatewayCacheForPlatform: &mockGatewayCacheForPlatform{}}
			s.cache = cache
			s.cfg.RunMode = config.RunModeStandard
			s.cfg.Gateway.Scheduling.LoadBatchEnabled = true
			if profit {
				s.concurrencyService = NewConcurrencyService(stubConcurrencyCache{})
				ctx, _ := WithGatewayTokenRequestPricing(c.Request.Context())
				c.Request = c.Request.WithContext(ctx)
			} else {
				s.concurrencyService = NewConcurrencyService(&mockConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: false}, loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 100}, 2: {AccountID: 2, LoadRate: 100}}})
			}
			require.Same(t, key, s.PrepareCacheAnchor(c, key, p, PlatformAnthropic, false))
			require.Equal(t, int64(31), *p.GroupID, "prepare alone cannot change routing")
			require.Equal(t, int64(31), *key.GroupID, "authenticated key is not mutated")
			hash := s.GenerateSessionHash(p)
			require.True(t, s.CacheAnchorApplied(p))
			servingGroupID := p.GroupID
			require.Equal(t, int64(77), p.AnchorV2.servingGroupID)
			selection, err := s.SelectAccountWithLoadAwareness(c.Request.Context(), servingGroupID, hash, "", nil, "", 0)
			require.NoError(t, err)
			require.NotNil(t, selection)
			if profit {
				require.Equal(t, cheap.ID, selection.Account.ID)
				require.True(t, selection.ProfitGateActive())
			} else {
				require.NotNil(t, selection.WaitPlan)
				require.False(t, selection.Acquired)
			}
			if selection.ReleaseFunc != nil {
				defer selection.ReleaseFunc()
			}
			bindCtx := ContextWithSelectionProfitGate(c.Request.Context(), selection)
			require.NoError(t, s.BindStickySessionAfterProfitAdmission(bindCtx, servingGroupID, hash, selection.Account.ID))
			require.NotEmpty(t, cache.writes)
			for _, gid := range cache.writes {
				require.Equal(t, int64(77), gid, "every actual cache write uses the serving namespace")
			}
			s.BindProviderCacheTraceSession(c, hash, "")
			require.Equal(t, int64(77), cacheTraceFromGin(c).data.GroupID)
			s.ReleaseCacheAnchor(p)
		})
	}
}

func TestL04RequestGroupSwitchRecomputesEligibility(t *testing.T) {
	s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
	s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
	a := s.GenerateSessionHash(p)
	b := *k
	b.Group = &Group{ID: 77, Platform: PlatformAnthropic}
	b.GroupID = &b.Group.ID
	s.PrepareCacheAnchor(c, &b, p, PlatformAnthropic, false)
	bh := s.GenerateSessionHash(p)
	require.NotEqual(t, a, bh)
	require.NotContains(t, s.anchorInflight.entries, a)
	s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
	require.Equal(t, a, s.GenerateSessionHash(p))
	b.Group = &Group{ID: 32, Platform: PlatformAnthropic}
	b.GroupID = &b.Group.ID
	s.PrepareCacheAnchor(c, &b, p, PlatformAnthropic, false)
	require.Equal(t, "not_in_group", p.cacheL04.skip)
	require.NotEqual(t, a, s.GenerateSessionHash(p))
	require.Empty(t, s.anchorInflight.entries)
	s.ReleaseCacheAnchor(p)
}

func TestL04GeminiPrecedenceAndEmptyDigest(t *testing.T) {
	s, c, k, _ := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
	k.Group.Platform = PlatformGemini
	for _, body := range []string{`{"model":"gemini-2.5-pro","systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"parts":[{"text":"first"}]}]}`, `{"model":"gemini-2.5-pro","contents":[]}`} {
		p, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformGemini)
		require.NoError(t, err)
		require.Nil(t, p.GroupID)
		s.PrepareCacheAnchor(c, k, p, PlatformGemini, false)
		require.Nil(t, p.GroupID, "Gemini only gains serving scope on application")
		hash := s.GenerateSessionHash(p)
		if p.AnchorV2.value != "" {
			require.Equal(t, k.GroupID, p.GroupID)
			require.Equal(t, p.AnchorV2.value, hash)
			s.ReleaseCacheAnchor(p)
			s.PrepareCacheAnchor(c, k, p, PlatformGemini, false)
			s.CacheAnchorEarlierTier(p)
			require.Equal(t, "earlier_tier", p.cacheL04.skip)
		} else {
			require.Equal(t, "empty_anchor", p.cacheL04.skip)
		}
		s.ReleaseCacheAnchor(p)
	}
}

func TestL04RequestTwoTenantDigestsAndTraceServingGroup(t *testing.T) {
	var hashes, digests []string
	for tenant := int64(101); tenant <= 102; tenant++ {
		s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
		k.ID = tenant
		k.Group.ClaudeCodeOnly = true
		fallback := &Group{ID: 77, Platform: PlatformAnthropic, Hydrated: true, Status: StatusActive}
		k.Group.FallbackGroupID = &fallback.ID
		s.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{31: k.Group, 77: fallback}}
		// Begin fixes the authenticated tenant; preparation only changes trace scope.
		s.BeginProviderCacheTrace(c, PlatformAnthropic, "messages", p.Body.Bytes(), p)
		require.Same(t, k, s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false))
		require.Equal(t, int64(31), *p.GroupID)
		current, _ := c.Get("api_key")
		require.Same(t, k, current)
		require.Same(t, k, BillingAPIKey(c.Request.Context(), k))
		hash := s.GenerateSessionHash(p)
		s.BindProviderCacheTraceSession(c, hash, "")
		tr := cacheTraceFromGin(c)
		require.Equal(t, int64(77), tr.data.GroupID)
		require.Equal(t, cacheProviderSessionKey{platform: PlatformAnthropic, digest: tr.data.SessionDigest}, tr.providerKey)
		want := s.providerTraceEngine.cacheProviderSessionScope(PlatformAnthropic, tenant, 77, hash)
		require.Equal(t, want, tr.providerKey)
		require.Equal(t, want.digest, ProviderCacheTraceSessionDigest(c.Request.Context()))
		require.Equal(t, int64(31), *k.GroupID)
		hashes = append(hashes, hash)
		digests = append(digests, tr.data.SessionDigest)
		s.ReleaseCacheAnchor(p)
	}
	require.NotEqual(t, hashes[0], hashes[1])
	require.NotEqual(t, digests[0], digests[1])
}

func TestL04RequestExclusionAndCounterFaultBoundaries(t *testing.T) {
	t.Run("unresolved group", func(t *testing.T) {
		s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
		k.Group.ClaudeCodeOnly = true
		s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
		require.Equal(t, "group_unresolved", p.cacheL04.skip)
		require.Empty(t, p.AnchorV2.value)
		require.Empty(t, s.anchorInflight.entries)
	})
	t.Run("runtime platform changed", func(t *testing.T) {
		s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
		k.Group.Platform = PlatformComposite
		// Empty secret and nil repository cannot be observed after this gate.
		s.cfg.JWT.Secret = ""
		s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
		require.Equal(t, "not_anthropic_group", p.cacheL04.skip)
		require.Empty(t, p.AnchorV2.value)
		require.Empty(t, s.anchorInflight.entries)
	})
	t.Run("share active", func(t *testing.T) {
		s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
		share := NewShareSchedulingService(nil, nil, nil)
		share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true}, groups: map[int64]*shareGroupPolicy{31: {groupID: 31, policy: sharesched.Policy{Scheduler: sharesched.SchedulerShare}}}})
		s.shareScheduling = share
		s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
		require.Equal(t, "share_active", p.cacheL04.skip)
		require.Empty(t, p.AnchorV2.value)
		require.Empty(t, s.anchorInflight.entries)
	})
	t.Run("poisoned counter", func(t *testing.T) {
		s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
		s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
		s.anchorInflight.poisoned = true
		require.NotEqual(t, p.AnchorV2.value, s.GenerateSessionHash(p))
		require.Equal(t, "fanin_cap", p.cacheL04.skip)
		require.False(t, s.CacheAnchorApplied(p))
	})
}

func TestL04CompatIngressNeverUsesMessagesShapedBody(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			s, c, k, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
			raw := []byte(`{"model":"claude-sonnet-4-5","system":"body cannot self-declare native ingress","messages":[{"role":"system","content":"system A"},{"role":"user","content":"hi"}],"input":"hi","instructions":"system B"}`)
			var err error
			p, err = ParseGatewayRequest(NewRequestBodyRef(raw), protocol)
			require.NoError(t, err)
			p.GroupID = k.GroupID
			before := s.GenerateSessionHash(p)
			s.PrepareCompatCacheAnchor(c, k, p, PlatformAnthropic)
			require.Empty(t, p.AnchorV2.value)
			require.Equal(t, "empty_anchor", p.cacheL04.skip)
			require.Equal(t, before, s.GenerateSessionHash(p))
			require.Equal(t, raw, p.Body.Bytes())
			require.Empty(t, s.anchorInflight.entries)
		})
	}
}

func TestL04GeminiNativeAnchorPrecedesGenericLegacyTiers(t *testing.T) {
	s, c, k, _ := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
	k.Group.Platform = PlatformGemini
	p, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"gemini-2.5-flash","metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_11111111-2222-4333-8444-555555555555"},"contents":[{"parts":[{"text":"first native user"}]}]}`)), PlatformGemini)
	require.NoError(t, err)
	s.PrepareCacheAnchor(c, k, p, PlatformGemini, false)
	require.Equal(t, p.AnchorV2.value, s.GenerateCacheAnchorSession(p))
	require.Equal(t, "none", p.cacheL04.skip)
	s.ReleaseCacheAnchor(p)
}

func TestL04MiddlewareServingClonePreservesBillingOrigin(t *testing.T) {
	s, c, origin, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true}, true)
	// Mirror #125's authenticated serving clone, including its billing state.
	ctx := resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState())
	RememberAvailabilityFallbackOrigin(ctx, origin)
	state := resilience.StateFrom(ctx)
	require.True(t, state.MarkFallback(*origin.GroupID))
	c.Request = c.Request.WithContext(ctx)
	serving := *origin
	serving.Group = &Group{ID: 77, Platform: PlatformAnthropic, Hydrated: true, Status: StatusActive}
	serving.GroupID = &serving.Group.ID
	c.Set("api_key", &serving)
	s.BeginProviderCacheTrace(c, PlatformAnthropic, "messages", p.Body.Bytes(), p)
	require.Same(t, &serving, s.PrepareCacheAnchor(c, &serving, p, PlatformAnthropic, false))
	require.Same(t, origin, BillingAPIKey(c.Request.Context(), &serving))
	hash := s.GenerateSessionHash(p)
	s.BindProviderCacheTraceSession(c, hash, "")
	require.Equal(t, int64(77), p.AnchorV2.servingGroupID)
	require.Equal(t, int64(77), cacheTraceFromGin(c).data.GroupID)
	require.Equal(t, int64(31), *origin.GroupID)
	s.ReleaseCacheAnchor(p)
}

type l04ControlStickyCall struct {
	operation string
	group     int64
	hash      string
	account   int64
}

type l04ControlNamespaceCache struct {
	*mockGatewayCacheForPlatform
	calls []l04ControlStickyCall
}

func (m *l04ControlNamespaceCache) GetSessionAccountID(_ context.Context, group int64, hash string) (int64, error) {
	m.calls = append(m.calls, l04ControlStickyCall{"get", group, hash, 2})
	return 2, nil
}
func (m *l04ControlNamespaceCache) SetSessionAccountID(ctx context.Context, group int64, hash string, account int64, ttl time.Duration) error {
	m.calls = append(m.calls, l04ControlStickyCall{"set", group, hash, account})
	return m.mockGatewayCacheForPlatform.SetSessionAccountID(ctx, group, hash, account, ttl)
}

// The preexisting selector resolves A to B internally. Skipped L04 requests
// must preserve that entire baseline sequence, including the handler's A read,
// A prefetch and A final bind; only applied requests may consistently use B.
func TestL04ClaudeCodeSkippedRequestsKeepBaselineStickySequence(t *testing.T) {
	for _, scenario := range []string{"not_in_group", "arm_off", "compat", "earlier_tier", "fanin_cap", "share_active"} {
		t.Run(scenario, func(t *testing.T) {
			type result struct {
				calls              []l04ControlStickyCall
				hash               string
				prefetch, selected int64
			}
			var results []result
			for _, enabled := range []bool{false, true} {
				s, c, key, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: enabled}, false)
				fallback := gatewayProfitTestGroup(77, PlatformAnthropic)
				key.Group.ClaudeCodeOnly = true
				key.Group.FallbackGroupID = &fallback.ID
				s.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{31: key.Group, 77: fallback}}
				account := gatewayProfitTestAccount(2, PlatformAnthropic, 0.2, 77)
				s.accountRepo = &mockAccountRepoForPlatform{accounts: []Account{account}, accountsByID: map[int64]*Account{2: &account}}
				cache := &l04ControlNamespaceCache{mockGatewayCacheForPlatform: &mockGatewayCacheForPlatform{}}
				s.cache = cache
				s.cfg.RunMode = config.RunModeStandard
				s.cfg.Gateway.Scheduling.LoadBatchEnabled = true
				s.concurrencyService = NewConcurrencyService(stubConcurrencyCache{})
				q := *s.providerTraceEngine.cachePolicies.Snapshot()
				switch scenario {
				case "not_in_group":
					q.anchorPolicy.groups = []int64{31}
				case "arm_off":
					value := deriveCacheAnchorV2(s.cfg.JWT.Secret, key.ID, 77, p.Model, PlatformAnthropic, p.SystemRaw(), p.MessagesRaw())
					a := CacheAnchorV2{value: value, servingGroupID: 77}
					bucket, ok := a.cohortForGroup(77)
					require.True(t, ok)
					require.Positive(t, bucket)
					q.anchorPolicy.rollout = bucket // exact h == rollout boundary is off
				case "earlier_tier":
					require.NoError(t, p.ReplaceBody([]byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_11111111-2222-4333-8444-555555555555"},"messages":[{"role":"user","content":"first"}]}`)))
				case "fanin_cap":
					s.anchorInflight.poisoned = true
				case "share_active":
					share := NewShareSchedulingService(nil, nil, nil)
					share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true}, groups: map[int64]*shareGroupPolicy{77: {groupID: 77, policy: sharesched.Policy{Scheduler: sharesched.SchedulerShare}}}})
					s.shareScheduling = share
				}
				s.providerTraceEngine.cachePolicies.snapshot.Store(&q)
				c.Set(cachePolicySnapshotGinKey, &q)
				if scenario == "compat" {
					require.Same(t, key, s.PrepareCompatCacheAnchor(c, key, p, PlatformAnthropic))
				} else {
					require.Same(t, key, s.PrepareCacheAnchor(c, key, p, PlatformAnthropic, false))
				}
				require.Equal(t, int64(31), *p.GroupID, "prepare does not switch the sticky namespace")
				hash := s.GenerateSessionHash(p)
				require.False(t, s.CacheAnchorApplied(p))
				if enabled {
					want := scenario
					if want == "compat" {
						want = "empty_anchor"
					}
					require.Equal(t, want, p.cacheL04.skip)
				}
				routingGroupID := key.GroupID
				if s.CacheAnchorApplied(p) {
					routingGroupID = p.GroupID
				}
				bound, err := s.GetCachedSessionAccountID(c.Request.Context(), routingGroupID, hash)
				require.NoError(t, err)
				require.Equal(t, int64(2), bound)
				ctx := WithPrefetchedStickySession(c.Request.Context(), bound, *routingGroupID, false)
				prefetch, ok := PrefetchedStickyGroupIDFromContext(ctx)
				require.True(t, ok)
				require.Equal(t, int64(31), prefetch)
				// Share scheduling is intentionally not re-exercised here; its
				// exclusion above must preserve the same sticky IO as the baseline.
				if scenario == "share_active" {
					s.shareScheduling = nil
				}
				selection, err := s.SelectAccountWithLoadAwareness(ctx, routingGroupID, hash, "", nil, "", 0)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, int64(2), selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				require.NoError(t, s.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(ctx, selection), routingGroupID, hash, selection.Account.ID))
				require.Equal(t, int64(31), cache.calls[0].group)
				require.Equal(t, int64(31), cache.calls[len(cache.calls)-1].group)
				results = append(results, result{cache.calls, hash, prefetch, selection.Account.ID})
				s.ReleaseCacheAnchor(p)
			}
			require.Equal(t, results[0], results[1], "enabled-but-skipped must preserve every sticky call, prefetch, selection and hash")
		})
	}
}

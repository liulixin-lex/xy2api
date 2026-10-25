//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func l03RequestFixture(t *testing.T, trace bool) (*GatewayService, *gin.Context, *APIKey, *ParsedRequest, *Account) {
	t.Helper()
	s, c, key, p := l04RequestFixture(t, CacheM4BuildCapabilities{L04: true, L03: true}, trace)
	snapshot := *s.providerTraceEngine.cachePolicies.Snapshot()
	snapshot.anchorPolicy.metadataGroups = []int64{31, 77}
	snapshot.anchorPolicy.metadataAccounts = []int64{501, 502}
	snapshot.anchorPolicy.metadataRollout = 100
	s.providerTraceEngine.cachePolicies.snapshot.Store(&snapshot)
	c.Set(cachePolicySnapshotGinKey, &snapshot)
	a := &Account{ID: 501, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "base_url": "https://relay.invalid"}}
	return s, c, key, p, a
}

func l03Prepare(t *testing.T, s *GatewayService, c *gin.Context, k *APIKey, p *ParsedRequest) context.Context {
	t.Helper()
	require.Same(t, k, s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false))
	s.GenerateSessionHash(p)
	return s.beginCacheMetadataAttempt(c.Request.Context(), c, p, nil)
}

func TestL03RequestSkipOrderAndClientPriority(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*GatewayService, *gin.Context, *APIKey, *ParsedRequest, *Account)
		mpt        bool
	}{
		{name: "not_in_metadata_group", want: "not_in_metadata_group", change: func(s *GatewayService, _ *gin.Context, _ *APIKey, _ *ParsedRequest, _ *Account) {
			q := s.providerTraceEngine.cachePolicies.snapshot.Load()
			q.anchorPolicy.metadataGroups = nil
		}},
		{name: "arm_off", want: "arm_off", change: func(s *GatewayService, _ *gin.Context, _ *APIKey, _ *ParsedRequest, _ *Account) {
			q := s.providerTraceEngine.cachePolicies.snapshot.Load()
			q.anchorPolicy.metadataRollout = 0
		}},
		{name: "mpt_before_cli_and_account", want: "mpt_enabled", mpt: true, change: func(_ *GatewayService, c *gin.Context, _ *APIKey, _ *ParsedRequest, a *Account) {
			c.Request.Header.Set("User-Agent", "claude-cli/2.1.78")
			a.Type = AccountTypeOAuth
		}},
		{name: "cli_before_account", want: "claude_cli_ua", change: func(_ *GatewayService, c *gin.Context, _ *APIKey, _ *ParsedRequest, a *Account) {
			c.Request.Header.Set("User-Agent", "CLAUDE-CLI/2.1.78")
			a.Type = AccountTypeOAuth
		}},
		{name: "not_apikey", want: "not_apikey", change: func(_ *GatewayService, _ *gin.Context, _ *APIKey, _ *ParsedRequest, a *Account) {
			a.Type = AccountTypeOAuth
		}},
		{name: "official_before_account_list", want: "official_endpoint", change: func(_ *GatewayService, _ *gin.Context, _ *APIKey, _ *ParsedRequest, a *Account) {
			a.ID = 999
			a.Credentials["base_url"] = "https://API.ANTHROPIC.COM:443/v1"
		}},
		{name: "account_list", want: "not_in_metadata_accounts", change: func(_ *GatewayService, _ *gin.Context, _ *APIKey, _ *ParsedRequest, a *Account) { a.ID = 999 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, k, p, a := l03RequestFixture(t, true)
			tc.change(s, c, k, p, a)
			ctx := l03Prepare(t, s, c, k, p)
			defer s.ReleaseCacheAnchor(p)
			original := append([]byte{}, p.Body.Bytes()...)
			out := s.applyCacheMetadata(ctx, c, a, p.Body.Bytes(), tc.mpt)
			require.Equal(t, original, out)
			require.Equal(t, original, p.Body.Bytes())
			tr := cacheTraceFromGin(c)
			require.Equal(t, tc.want, tr.data.L03Skip)
			require.False(t, tr.data.L03Applied)
			if tc.want == "not_in_metadata_group" {
				require.Equal(t, "none", tr.data.L03Arm)
			}
		})
	}
	for _, raw := range []string{`"unparseable-client-id"`, `" "`, `"user_opaque"`, `0`, `false`, `{}`, `[]`, `null`} {
		t.Run("client="+raw, func(t *testing.T) {
			s, c, k, p, a := l03RequestFixture(t, true)
			body, err := sjson.SetRawBytes(p.Body.Bytes(), "metadata.user_id", []byte(raw))
			require.NoError(t, err)
			require.NoError(t, p.ReplaceBody(body))
			c.Request.Header.Set("User-Agent", "claude-cli/2.1.78")
			a.Type = AccountTypeOAuth
			ctx := l03Prepare(t, s, c, k, p)
			defer s.ReleaseCacheAnchor(p)
			require.Equal(t, body, s.applyCacheMetadata(ctx, c, a, body, true))
			require.Equal(t, "client_user_id", cacheTraceFromGin(c).data.L03Skip)
		})
	}
}

func TestL03EarlierTierSharedLeaseKeepsLocalIdentity(t *testing.T) {
	s, c, k, p, a := l03RequestFixture(t, true)
	body := []byte(m2bClaudePayload(false))
	require.NoError(t, p.ReplaceBody(body))
	ctx := l03Prepare(t, s, c, k, p)
	defer s.ReleaseCacheAnchor(p)
	local := s.GenerateSessionHash(p)
	require.Equal(t, "earlier_tier", p.cacheL04.skip)
	require.False(t, p.AnchorV2.armed)
	require.Equal(t, int64(31), *p.GroupID)
	out := s.applyCacheMetadata(ctx, c, a, body, false)
	require.NotEmpty(t, gjson.GetBytes(out, "metadata.user_id").String())
	require.Equal(t, body, p.Body.Bytes())
	require.Equal(t, local, s.GenerateSessionHash(p))
	require.Equal(t, "cacheable_content", cacheTraceFromGin(c).data.IDSource)
	require.Equal(t, "earlier_tier", cacheTraceFromGin(c).data.L04Skip)
	require.True(t, cacheTraceFromGin(c).data.L03Applied)
	require.Equal(t, 1, s.anchorInflight.entries[p.AnchorV2.value])
	s.ReleaseCacheAnchor(p)
	require.Empty(t, s.anchorInflight.entries)
}

func TestL03EarlierTierFaninAndScopeFailClosed(t *testing.T) {
	for _, scope := range []bool{false, true} {
		t.Run(fmt.Sprint(scope), func(t *testing.T) {
			s, c, k, p, a := l03RequestFixture(t, true)
			require.NoError(t, p.ReplaceBody([]byte(m2bClaudePayload(false))))
			ctx := l03Prepare(t, s, c, k, p)
			defer s.ReleaseCacheAnchor(p)
			reason := "scope_mismatch"
			if scope {
				p.AnchorV2.servingGroupID++
			} else {
				reason = "fanin_cap"
				s.anchorInflight.capacity = cacheAnchorInflightHardCap
				first, ok := s.anchorInflight.acquire(p.AnchorV2.value, 2)
				require.True(t, ok)
				defer first.release()
				second, ok := s.anchorInflight.acquire(p.AnchorV2.value, 2)
				require.True(t, ok)
				defer second.release()
			}
			out := s.applyCacheMetadata(ctx, c, a, p.Body.Bytes(), false)
			require.Equal(t, p.Body.Bytes(), out)
			require.Equal(t, reason, cacheTraceFromGin(c).data.L04Skip)
			require.Equal(t, reason, cacheTraceFromGin(c).data.L03Skip)
		})
	}
}

func TestL03EveryAttemptRechecksAccountWithoutChangingSticky(t *testing.T) {
	s, c, k, p, a := l03RequestFixture(t, true)
	ctx := l03Prepare(t, s, c, k, p)
	defer s.ReleaseCacheAnchor(p)
	original := append([]byte{}, p.Body.Bytes()...)
	local := s.GenerateSessionHash(p)
	first := ParseMetadataUserID(gjson.GetBytes(s.applyCacheMetadata(ctx, c, a, original, false), "metadata.user_id").String())
	require.NotNil(t, first)
	a.ID = 502
	second := ParseMetadataUserID(gjson.GetBytes(s.applyCacheMetadata(ctx, c, a, original, false), "metadata.user_id").String())
	require.NotNil(t, second)
	require.Equal(t, first.SessionID, second.SessionID)
	require.NotEqual(t, first.DeviceID, second.DeviceID)
	require.Equal(t, original, p.Body.Bytes())
	require.Equal(t, local, s.GenerateSessionHash(p))
	a.Credentials["base_url"] = "https://api.anthropic.com"
	ctx = s.beginCacheMetadataAttempt(c.Request.Context(), c, p, a)
	require.False(t, cacheMetadataApplied(ctx), "new attempt clears the previous result before any builder can fail")
	require.Equal(t, original, s.applyCacheMetadata(ctx, c, a, original, false))
	require.Equal(t, "official_endpoint", cacheTraceFromGin(c).data.L03Skip)
	require.False(t, cacheTraceFromGin(c).data.L03Applied)
	require.Equal(t, 1, s.anchorInflight.entries[p.AnchorV2.value], "one request owns exactly one lease across attempts")
}

func TestL03ClosedCapabilityAndMasterZeroTouch(t *testing.T) {
	for _, capability := range []bool{false, true} {
		s, c, k, p, a := l03RequestFixture(t, false)
		q := s.providerTraceEngine.cachePolicies.snapshot.Load()
		if capability {
			q.master = false
		} else {
			q.anchorPolicy.metadataAvailable = false
		}
		ctx := l03Prepare(t, s, c, k, p)
		defer s.ReleaseCacheAnchor(p)
		require.Same(t, c.Request.Context(), ctx)
		original := p.Body.Bytes()
		require.Zero(t, testing.AllocsPerRun(50, func() {
			got := s.applyCacheMetadata(ctx, c, a, original, false)
			if &got[0] != &original[0] {
				panic("closed body changed")
			}
		}))
	}
}

func TestL03CompatCountTokensAndGeminiExclusions(t *testing.T) {
	for _, kind := range []string{"chat", "responses", "count_tokens", "gemini"} {
		t.Run(kind, func(t *testing.T) {
			s, c, k, p, a := l03RequestFixture(t, true)
			original := p.Body.Bytes()
			switch kind {
			case "chat", "responses":
				s.PrepareCompatCacheAnchor(c, k, p, PlatformAnthropic)
			case "count_tokens":
				s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, true)
			case "gemini":
				k.Group.Platform = PlatformGemini
				s.PrepareCacheAnchor(c, k, p, PlatformGemini, false)
			}
			s.GenerateSessionHash(p)
			defer s.ReleaseCacheAnchor(p)
			ctx := s.beginCacheMetadataAttempt(c.Request.Context(), c, p, a)
			require.Equal(t, original, s.applyCacheMetadata(ctx, c, a, original, false))
			if kind == "gemini" {
				require.NotEqual(t, "both", p.cacheL04.arm)
			} else if kind == "count_tokens" {
				require.Nil(t, p.cacheL04)
				require.Equal(t, "count_tokens", cacheTraceFromGin(c).data.L03Skip)
			} else {
				require.Equal(t, "empty_anchor", cacheTraceFromGin(c).data.L03Skip)
			}
		})
	}
}

func TestL03NativeForwardWireAndObservationIndependence(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var observedBody string
			var observedUsage ClaudeUsage
			var observedClient string
			for _, trace := range []bool{false, true} {
				gate, c, k, p, a := l03RequestFixture(t, trace)
				recorder := httptest.NewRecorder()
				writerContext, _ := gin.CreateTestContext(recorder)
				c.Writer = writerContext.Writer
				require.NoError(t, p.ReplaceBody([]byte(m2bClaudePayload(stream))))
				gate.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
				local := gate.GenerateSessionHash(p)
				gate.BindProviderCacheTraceSession(c, local, "")
				upstream := &anthropicHTTPUpstreamRecorder{resp: m2bClaudeResponse(stream)}
				s := newForwardPartialUsageServiceForTest(upstream)
				s.cfg = gate.cfg
				s.providerTraceEngine = gate.providerTraceEngine
				// The lease belongs to the same service as session derivation in production.
				// This fixture's cacheable tier defers acquisition until this Forward.
				result, err := s.Forward(c.Request.Context(), c, a, p)
				require.NoError(t, err)
				require.NotNil(t, result)
				wire := upstream.lastBody
				uid := gjson.GetBytes(wire, "metadata.user_id").String()
				require.NotNil(t, ParseMetadataUserID(uid))
				require.Empty(t, upstream.lastReq.Header.Get("X-Claude-Code-Session-Id"))
				require.NotContains(t, recorder.Body.String(), uid)
				require.Equal(t, local, gate.GenerateSessionHash(p))
				if trace {
					require.Equal(t, observedBody, string(wire))
					require.Equal(t, observedUsage, result.Usage)
					require.Equal(t, observedClient, recorder.Body.String())
				} else {
					observedBody = string(wire)
					observedUsage = result.Usage
					observedClient = recorder.Body.String()
				}
				s.ReleaseCacheAnchor(p)
				require.Empty(t, s.anchorInflight.entries)
			}
		})
	}
}

func TestL03DebugAndLocalSyncKeepIdentityPrivate(t *testing.T) {
	s, c, k, p, a := l03RequestFixture(t, false)
	body, err := sjson.SetBytes(p.Body.Bytes(), "metadata.client_field", "preserve-me")
	require.NoError(t, err)
	require.NoError(t, p.ReplaceBody(body))
	ctx := l03Prepare(t, s, c, k, p)
	defer s.ReleaseCacheAnchor(p)
	generated := s.applyCacheMetadata(ctx, c, a, body, false)
	uid := gjson.GetBytes(generated, "metadata.user_id").String()
	require.NotEmpty(t, uid)
	restored := withoutCacheMetadata(ctx, generated, body)
	require.Equal(t, body, restored)
	require.Equal(t, body, p.Body.Bytes())
	file, err := os.CreateTemp(t.TempDir(), "debug")
	require.NoError(t, err)
	defer file.Close()
	s.debugGatewayBodyFile.Store(file)
	sid := ParseMetadataUserID(uid).SessionID
	headers := http.Header{"X-Claude-Code-Session-Id": []string{sid}}
	s.debugLogGatewaySnapshot("UPSTREAM_FORWARD", s.cacheMetadataDebugHeaders(ctx, headers), s.cacheMetadataDebugBody(ctx, generated), nil)
	require.NoError(t, file.Sync())
	logged, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	require.NotContains(t, string(logged), uid)
	require.NotContains(t, string(logged), sid)
	require.Equal(t, sid, headers.Get("X-Claude-Code-Session-Id"), "snapshot redaction leaves wire headers unchanged")
	req, err := http.NewRequest(http.MethodPost, "https://relay.invalid", nil)
	require.NoError(t, err)
	line := buildClaudeMimicDebugLine(req, cacheMetadataRedactedBody(ctx, generated), a, "apikey", false)
	require.NotContains(t, line, uid)
	require.NotContains(t, line, sid)
	require.Contains(t, string(logged), "preserve-me")
}

func TestL03PassthroughForwardRetainsOriginalLocalBody(t *testing.T) {
	gate, c, k, p, a := l03RequestFixture(t, true)
	a.Extra = map[string]any{"anthropic_passthrough": true}
	require.NoError(t, p.ReplaceBody([]byte(m2bClaudePayload(false))))
	original := append([]byte{}, p.Body.Bytes()...)
	gate.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
	hash := gate.GenerateSessionHash(p)
	gate.BindProviderCacheTraceSession(c, hash, "")
	upstream := &anthropicHTTPUpstreamRecorder{resp: m2bClaudeResponse(false)}
	s := newForwardPartialUsageServiceForTest(upstream)
	s.cfg = gate.cfg
	s.providerTraceEngine = gate.providerTraceEngine
	result, err := s.Forward(c.Request.Context(), c, a, p)
	require.NoError(t, err)
	require.NotNil(t, result)
	uid := gjson.GetBytes(upstream.lastBody, "metadata.user_id").String()
	require.NotNil(t, ParseMetadataUserID(uid))
	require.NotContains(t, string(p.Body.Bytes()), uid)
	require.Equal(t, original, p.Body.Bytes())
	require.Equal(t, hash, gate.GenerateSessionHash(p))
	require.Empty(t, upstream.lastReq.Header.Get("X-Claude-Code-Session-Id"))
	if upstream.lastReq.GetBody != nil {
		reader, err := upstream.lastReq.GetBody()
		require.NoError(t, err)
		replayed, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		require.Equal(t, upstream.lastBody, replayed)
	}
	s.ReleaseCacheAnchor(p)
	require.Empty(t, s.anchorInflight.entries)
}

func TestL03NestedCohortAndClientEmptyString(t *testing.T) {
	// Independent anth-cohort| fixture bucket =77. Exercise the consumer with the
	// same candidate at the exact nested threshold, including an explicit empty ID.
	for _, metadata := range []int{77, 78} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			s, c, k, p, a := l03RequestFixture(t, true)
			body, err := sjson.SetBytes(p.Body.Bytes(), "metadata.user_id", "")
			require.NoError(t, err)
			require.NoError(t, p.ReplaceBody(body))
			s.PrepareCacheAnchor(c, k, p, PlatformAnthropic, false)
			p.AnchorV2 = CacheAnchorV2{value: "a2:00112233445566778899aabbccddeeff", servingGroupID: 31}
			d := p.cacheL04
			d.policy.rollout = 78
			d.policy.metadataRollout = metadata
			d.arm = p.AnchorV2.armForGroup(31, 78, metadata, true)
			s.GenerateSessionHash(p)
			ctx := s.beginCacheMetadataAttempt(c.Request.Context(), c, p, a)
			defer s.ReleaseCacheAnchor(p)
			got := s.applyCacheMetadata(ctx, c, a, body, false)
			if metadata == 77 {
				require.Equal(t, body, got)
				require.Equal(t, "arm_off", cacheTraceFromGin(c).data.L03Skip)
				require.Equal(t, "off", cacheTraceFromGin(c).data.L03Arm)
			} else {
				require.NotEmpty(t, gjson.GetBytes(got, "metadata.user_id").String())
				require.Equal(t, "none", cacheTraceFromGin(c).data.L03Skip)
				require.Equal(t, "on", cacheTraceFromGin(c).data.L03Arm)
			}
		})
	}
}

func TestL03CompatForwardOutboundMatchesClosedBuild(t *testing.T) {
	for _, kind := range []string{"chat", "responses"} {
		t.Run(kind, func(t *testing.T) {
			var baseline []byte
			var baselineHeaders http.Header
			for _, enabled := range []bool{false, true} {
				gate, c, k, _, a := l03RequestFixture(t, true)
				if !enabled {
					gate.providerTraceEngine.cachePolicies.snapshot.Load().anchorPolicy.metadataAvailable = false
				}
				body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"system","content":"system A"},{"role":"user","content":"hi"}]}`)
				protocol := PlatformAnthropic
				if kind == "responses" {
					protocol = "responses"
					body = []byte(`{"model":"claude-sonnet-4-5","max_output_tokens":64,"instructions":"system A","input":"hi"}`)
				}
				p, err := ParseGatewayRequest(NewRequestBodyRef(body), protocol)
				require.NoError(t, err)
				p.GroupID = k.GroupID
				gate.PrepareCompatCacheAnchor(c, k, p, PlatformAnthropic)
				gate.GenerateSessionHash(p)
				require.Empty(t, p.AnchorV2.value)
				upstream := &anthropicHTTPUpstreamRecorder{resp: m2bClaudeResponse(true)}
				s := newForwardPartialUsageServiceForTest(upstream)
				s.cfg = gate.cfg
				s.providerTraceEngine = gate.providerTraceEngine
				var result *ForwardResult
				if kind == "chat" {
					result, err = s.ForwardAsChatCompletions(c.Request.Context(), c, a, body, p)
				} else {
					result, err = s.ForwardAsResponses(c.Request.Context(), c, a, body, p)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				if enabled {
					require.Equal(t, baseline, upstream.lastBody)
					require.Equal(t, baselineHeaders, upstream.lastReq.Header)
					require.Equal(t, "empty_anchor", cacheTraceFromGin(c).data.L03Skip)
				} else {
					baseline = append([]byte{}, upstream.lastBody...)
					baselineHeaders = upstream.lastReq.Header.Clone()
				}
				s.ReleaseCacheAnchor(p)
			}
		})
	}
}

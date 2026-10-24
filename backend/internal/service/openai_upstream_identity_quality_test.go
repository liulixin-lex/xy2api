//go:build unit

package service

// L-01 must preserve an existing quality rotation, including its wire bytes
// and source attribution. These requests execute the production forwarding path.
import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type l01QualityRequestVariant func(*gin.Context, *CachePolicySnapshot, []byte) []byte

const l01QualityRotationFixture = "8bd0c67e7f184e109a08cd527334e892"

type l01QualityReproOutcome struct {
	wire, client  string
	sessionHeader string
	header        http.Header
	code          int
	result        *OpenAIForwardResult
	trace         *cacheTrace
	body          []byte
	session       string
	generation    uint64
	rotation      string
	arm           string
	percent       int
}

func l01QualityReproBody(protocol string) []byte {
	field := "input"
	if protocol == "chat" {
		field = "messages"
	}
	return []byte(fmt.Sprintf(`{"model":"gpt-6-astra","%s":[{"role":"user","content":"quality-l01-synthetic"}],"stream":false}`, field))
}

// Real existing GenerateSessionHash establishes both routing scope and quality
// context. The fixture installs a loaded pre-existing quality decision, exactly
// as the existing quality tests do. No fake L01 marker or event is installed.
func l01QualityReproContext(t *testing.T, protocol string, observer bool, arm string, apiID int64, variants ...l01QualityRequestVariant) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *Account, *httpUpstreamRecorder, []byte) {
	t.Helper()
	body := l01QualityReproBody(protocol)
	path := "/v1/responses"
	if protocol == "chat" {
		path = "/v1/chat/completions"
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(withOpenAIQualityRouting(c.Request.Context()))
	group := int64(10)
	c.Set("api_key", &APIKey{ID: apiID, GroupID: &group})
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-quality-l01-secret"}}
	cfg.Gateway.OpenAIQualityRouting.Mode = "enforce"
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	response := traceForwardOK()
	response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(traceForwardSSE, "gpt-5.4", "gpt-6-astra")))
	upstream := &httpUpstreamRecorder{resp: response}
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	traceTestConfig(s)
	traceTestSetPolicy(s, map[string]any{"enabled": observer})
	snapshot := *s.cachePolicies.Snapshot()
	snapshot.master = arm != "policy_off"
	snapshot.upstream = l01ChatTestPolicy(arm != "policy_off", true).upstream
	for _, variant := range variants {
		body = variant(c, &snapshot, body)
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	// Derive only the already-existing routing scope before selecting a valid
	// control percentage; the caller uses the same body/api/group for all arms.
	session := s.GenerateSessionHash(c, body)
	require.NotEmpty(t, session)
	if arm == "control" {
		bucket := sha256SumCacheCohort(session)
		require.Positive(t, bucket, "caller must choose a realizable control cohort")
		snapshot.upstream.RolloutPercent = bucket
	}
	snapshot.initCohortViews()
	c.Set(cachePolicySnapshotGinKey, &snapshot)
	// Discard only the preparatory context's observation so the real request
	// is created from the final snapshot; no emitted or computed field is forged.
	c.Request = c.Request.WithContext(withOpenAIQualityRouting(httptest.NewRequest(http.MethodPost, path, nil).Context()))
	s.GenerateSessionHash(c, body)
	q := qualityRequest(c.Request.Context())
	require.NotNil(t, q)
	account := newOpenAIRejectedFieldTestAccount()
	account.ID = 41
	q.loaded = true
	q.state = OpenAIQualityState{Generation: 1, Revision: 1,
		Rotation: l01QualityRotationFixture, Expires: time.Now().Add(time.Hour).UnixMilli(),
		Avoid: map[string]OpenAIQualityAvoid{strconv.FormatInt(account.ID, 10): {
			Provider: qualityProvider(account), Until: time.Now().Add(time.Hour).UnixMilli(),
		}}}
	require.Equal(t, l01QualityRotationFixture, s.qualityRotation(c.Request.Context(), account))
	require.False(t, q.pinned)
	return s, c, recorder, account, upstream, body
}

func l01QualityReproRun(t *testing.T, protocol string, observer bool, arm string, apiID int64, variants ...l01QualityRequestVariant) l01QualityReproOutcome {
	t.Helper()
	s, c, recorder, account, upstream, body := l01QualityReproContext(t, protocol, observer, arm, apiID, variants...)
	var result *OpenAIForwardResult
	var err error
	if protocol == "chat" {
		result, err = s.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
	} else {
		result, err = s.Forward(c.Request.Context(), c, account, body)
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, 2000, result.Usage.InputTokens)
	require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
	if observer {
		require.NotNil(t, result.CacheTrace, "positive observation control")
	} else {
		require.Nil(t, result.CacheTrace, "observer-off must use the real disabled path")
		require.Nil(t, cacheTraceFromGin(c))
	}
	q := qualityRequest(c.Request.Context())
	q.mu.Lock()
	generation, rotation := q.state.Generation, q.state.Rotation
	q.mu.Unlock()
	return l01QualityReproOutcome{wire: cacheTraceWireSnapshot(t, upstream.lastReq, upstream.lastBody),
		client: recorder.Body.String(), sessionHeader: upstream.lastReq.Header.Get("session_id"), header: recorder.Header().Clone(), code: recorder.Code,
		result: result, trace: result.CacheTrace, body: append([]byte(nil), upstream.lastBody...),
		session: c.GetString(openAICacheSessionGinKey), generation: generation, rotation: rotation,
		arm: arm, percent: CachePolicyFromGin(c).UpstreamIdentity().RolloutPercent}
}

func TestL01QualityRotationPreservesBaseline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []string{"responses", "chat"} {
		for _, observer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/observer=%t", protocol, observer), func(t *testing.T) {
				// Find a nonzero existing session bucket so a real active rollout
				// (not policy_off or a manually forced cohort) reaches control.
				apiID := int64(99)
				for ; apiID < 355; apiID++ {
					s, c, _, _, _, body := l01QualityReproContext(t, protocol, false, "policy_off", apiID)
					if sha256SumCacheCohort(s.GenerateSessionHash(c, body)) > 0 {
						break
					}
				}
				require.Less(t, apiID, int64(355))

				// Chat's unchanged production converter stamps time.Now().Unix().
				// Retry only a wall-clock boundary, before testing any equality;
				// never ignore/edit a client field or retry an assertion failure.
				var runs []l01QualityReproOutcome
				stableSecond := false
				for attempt := 0; attempt < 5; attempt++ {
					second := time.Now().Unix()
					runs = nil
					for _, arm := range []string{"policy_off", "control", "on"} {
						runs = append(runs, l01QualityReproRun(t, protocol, observer, arm, apiID))
					}
					if second == time.Now().Unix() {
						stableSecond = true
						break
					}
				}
				require.True(t, stableSecond, "clock boundary prevented a byte-comparable client response fixture")
				baseline := runs[0]
				require.Equal(t, l01QualityRotationFixture, gjson.GetBytes(baseline.body, "prompt_cache_key").String(), "positive control: existing quality rotation must really execute")
				for _, got := range runs[1:] {
					t.Run(got.arm, func(t *testing.T) {
						wirePCK := gjson.GetBytes(got.body, "prompt_cache_key").String()
						t.Logf("L01_QUALITY_REPRO protocol=%s observer=%t arm=%s wire_equal=%t usage_equal=%t client_equal=%t pck_bytes=%d pck_is_uuid=%t session_header_present=%t header_equals_pck=%t", protocol, observer, got.arm, baseline.wire == got.wire, baseline.result.Usage == got.result.Usage, baseline.client == got.client, len(wirePCK), cacheTraceUUID.MatchString(wirePCK), got.sessionHeader != "", got.sessionHeader == wirePCK)
						assert.Equal(t, baseline.wire, got.wire, "L01 must defer to quality: exact upstream bytes")
						assert.Equal(t, baseline.result.Usage, got.result.Usage, "every parsed usage field")
						assert.Equal(t, baseline.code, got.code)
						assert.Equal(t, baseline.header, got.header)
						assert.Equal(t, baseline.client, got.client, "exact client response, including its timestamp")
						assert.Equal(t, baseline.session, got.session, "local routing identity is immutable")
						assert.Equal(t, baseline.generation, got.generation)
						assert.Equal(t, baseline.rotation, got.rotation, "L01 must not rewrite quality state")
						assert.Equal(t, l01QualityRotationFixture, gjson.GetBytes(got.body, "prompt_cache_key").String())
						if observer {
							got.trace.mu.Lock()
							data := got.trace.data
							got.trace.mu.Unlock()
							t.Logf("L01_QUALITY_OBSERVER applied=%t skip=%s pck_source=%s pck_is_uuid=%t session_source=%s", data.L01Applied, data.L01Skip, data.PCKClassOut, data.PCKOutIsUUID, data.SessOutRelation)
							assert.False(t, data.L01Applied, "a quality-rotated request must skip L01")
							assert.NotEqual(t, "none", data.L01Skip)
							if got.arm == "on" {
								assert.Equal(t, "quality_rotation", data.L01Skip)
							} else {
								assert.Equal(t, "arm_off", data.L01Skip)
							}
							assert.NotEqual(t, "gw_uuid", data.PCKClassOut, "do not invent an L01 source for the rotated wire key")
							assert.NotEqual(t, "eq_pck", data.SessOutRelation, "do not invent header/pck equality")
						}
					})
				}
			})
		}
	}
}

func TestL01QualityRotationSkipPriority(t *testing.T) {
	for _, protocol := range []string{"responses", "chat"} {
		for _, tc := range []struct {
			name, pck, expected        string
			present, header, compatOff bool
		}{
			{name: "client_pck_before_header_and_quality", pck: "synthetic-client-pck", present: true, header: true, expected: "client_pck"},
			{name: "client_empty_before_header_and_quality", present: true, header: true, expected: "client_empty"},
			{name: "header_before_quality", header: true, expected: "header_only_off"},
			{name: "compat_switch_before_quality", compatOff: true, expected: "chat_compat_off"},
		} {
			if protocol == "responses" && tc.compatOff {
				continue
			}
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				variant := func(c *gin.Context, policy *CachePolicySnapshot, body []byte) []byte {
					policy.upstream.ChatCompatUUID = !tc.compatOff
					if tc.header {
						c.Request.Header.Set("session_id", "synthetic-client-session")
					}
					if tc.present {
						return []byte(strings.TrimSuffix(string(body), "}") + `,"prompt_cache_key":` + strconv.Quote(tc.pck) + `}`)
					}
					return body
				}
				var baseline, candidate l01QualityReproOutcome
				stableSecond := false
				for attempt := 0; attempt < 5; attempt++ {
					second := time.Now().Unix()
					baseline = l01QualityReproRun(t, protocol, true, "policy_off", 99, variant)
					candidate = l01QualityReproRun(t, protocol, true, "on", 99, variant)
					if second == time.Now().Unix() {
						stableSecond = true
						break
					}
				}
				require.True(t, stableSecond)
				require.Equal(t, baseline.wire, candidate.wire)
				require.Equal(t, baseline.result.Usage, candidate.result.Usage)
				require.Equal(t, baseline.code, candidate.code)
				require.Equal(t, baseline.header, candidate.header)
				require.Equal(t, baseline.client, candidate.client)
				candidate.trace.mu.Lock()
				data := candidate.trace.data
				candidate.trace.mu.Unlock()
				require.False(t, data.L01Applied)
				require.Equal(t, "on", data.L01Arm)
				require.Equal(t, tc.expected, data.L01Skip)
				require.Equal(t, l01QualityRotationFixture, gjson.GetBytes(candidate.body, "prompt_cache_key").String(), "earlier L01 skip must not suppress the existing quality rotation")
			})
		}
	}
}

func TestL01QualityEnforceWithoutRotationStillApplies(t *testing.T) {
	for _, protocol := range []string{"responses", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			s, c, _, account, upstream, body := l01QualityReproContext(t, protocol, true, "on", 99)
			q := qualityRequest(c.Request.Context())
			q.mu.Lock()
			q.state.Avoid = map[string]OpenAIQualityAvoid{"other": {Provider: "synthetic-other-provider"}}
			q.mu.Unlock()
			require.Equal(t, "enforce", s.qualityConfig().EffectiveMode())
			require.Empty(t, s.qualityRotation(c.Request.Context(), account), "no rotation for the actual serving provider")
			var result *OpenAIForwardResult
			var err error
			if protocol == "chat" {
				result, err = s.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
			} else {
				result, err = s.Forward(c.Request.Context(), c, account, body)
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 1)
			pck := gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String()
			require.True(t, cacheTraceUUID.MatchString(pck))
			require.Equal(t, pck, upstream.lastReq.Header.Get("session_id"))
			require.NotNil(t, result.CacheTrace)
			require.True(t, result.CacheTrace.data.L01Applied)
			require.Equal(t, "none", result.CacheTrace.data.L01Skip)
		})
	}
}

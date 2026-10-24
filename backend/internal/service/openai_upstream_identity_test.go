//go:build unit

package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestL01BodyInsertionPreservesOriginalSuffixAndPresence(t *testing.T) {
	const uuid = "a6b18d89-187f-4444-8adf-cbc444987efa"
	for _, tc := range []struct {
		name, body, want string
		present          bool
	}{
		{"ordinary", `{"input":[{"role":"user","content":"synthetic-private"}],"model":"gpt-5.4"}`, `{"prompt_cache_key":"` + uuid + `","input":[{"role":"user","content":"synthetic-private"}],"model":"gpt-5.4"}`, false},
		{"empty", `{}`, `{"prompt_cache_key":"` + uuid + `"}`, false},
		{"whitespace", " \n{\t }\n", " \n{\"prompt_cache_key\":\"" + uuid + "\"\t }\n", false},
		{"leading", " \t{ \n\"model\":\"gpt-5.4\"}", " \t{\"prompt_cache_key\":\"" + uuid + "\", \n\"model\":\"gpt-5.4\"}", false},
		{"client_empty", `{"prompt_cache_key":""}`, `{"prompt_cache_key":""}`, true},
		{"client_null", `{"prompt_cache_key":null}`, `{"prompt_cache_key":null}`, true},
		{"client_number", `{"prompt_cache_key":42}`, `{"prompt_cache_key":42}`, true},
		{"client_space", `{"prompt_cache_key":"  "}`, `{"prompt_cache_key":"  "}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(tc.body)
			backup := bytes.Clone(original)
			result := fillOpenAIUpstreamIdentityBody(original, tc.present, uuid)
			require.Equal(t, tc.want, string(result))
			require.Equal(t, backup, original)
			require.True(t, gjson.ValidBytes(result))
			again := fillOpenAIUpstreamIdentityBody(result, true, uuid)
			require.Equal(t, result, again)
			require.Equal(t, &result[0], &again[0])
			if tc.present {
				require.Equal(t, &original[0], &result[0])
			}
		})
	}
}

func TestL01RequestViewPresenceIsIndependentOfValue(t *testing.T) {
	for _, tc := range []struct {
		name, field    string
		present, empty bool
	}{
		{"absent", "", false, false}, {"empty", `,"prompt_cache_key":""`, true, true},
		{"null", `,"prompt_cache_key":null`, true, false}, {"space", `,"prompt_cache_key":" "`, true, false},
		{"number", `,"prompt_cache_key":42`, true, false}, {"object", `,"prompt_cache_key":{}`, true, false},
		{"escaped", `,"prompt\u005fcache_key":""`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"input":[{"role":"user","content":"synthetic"}],"model":"gpt-5.4"` + tc.field + `}`)
			view := newOpenAIRequestView(body)
			require.Equal(t, tc.present, view.HasPromptCacheKey)
			require.Equal(t, tc.empty, view.PromptCacheKeyEmpty)
		})
	}
}

func TestL01PolicySnapshotAndAttemptCohort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(true, true))
	first := "v2:0123456789abcdef"
	c.Set(openAICacheSessionGinKey, first)
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	d := prepareOpenAIUpstreamIdentity(c, account)
	require.True(t, d.eligible())
	require.Equal(t, first, d.RoutingSession)
	require.EqualValues(t, 7, d.PolicyRevision)
	// The request snapshot remains the identical pointer after its cohort is
	// fixed. A later publisher's snapshot is irrelevant to this request.
	captured := CachePolicyFromGin(c)
	replacement := l01ChatTestPolicy(false, false)
	require.NotSame(t, captured, replacement)
	require.Same(t, captured, CaptureCachePolicy(c, nil))
	d = prepareOpenAIUpstreamIdentity(c, account)
	require.Equal(t, "on", d.Arm, "cohort is fixed after the first nonempty routing session")
	c.Set(openAICacheSessionGinKey, "v2:fedcba9876543210")
	account.ID = 10
	next := prepareOpenAIUpstreamIdentity(c, account)
	require.Equal(t, d.Arm, next.Arm, "account and serving scope do not rebucket an existing request")
	require.NotEqual(t, generateSessionUUID("openai-upstream-session:v1:"+d.RoutingSession), generateSessionUUID("openai-upstream-session:v1:"+next.RoutingSession), "serving scope changes the generated identity")
	rememberOpenAIUpstreamIdentityWire(c, account, "synthetic-derived-uuid")
	account.Type = AccountTypeOAuth
	skipped := prepareOpenAIUpstreamIdentity(c, account)
	require.False(t, skipped.eligible())
	req := httptest.NewRequest(http.MethodPost, "https://example.test/v1/responses", nil)
	applyOpenAIUpstreamIdentityHeaders(c, account, req)
	require.Empty(t, req.Header.Get("session_id"), "an earlier eligible attempt must not leak into OAuth")
}

func TestL01SessionHeaderPreservesExplicitPresence(t *testing.T) {
	for _, value := range []string{"", "synthetic-client"} {
		req := httptest.NewRequest(http.MethodPost, "https://example.test/v1/responses", nil)
		req.Header.Set("session_id", value)
		applyUpstreamSessionIdentity(req, nil, "synthetic-derived")
		require.Equal(t, value, req.Header.Get("session_id"))
	}
}

func TestL01DisabledPreparationDoesNotAllocate(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(false, false))
	a := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.Zero(t, testing.AllocsPerRun(1000, func() {
		d := prepareOpenAIUpstreamIdentity(c, a)
		if d.eligible() {
			panic("disabled L01 preparation")
		}
		applyOpenAIUpstreamIdentityHeaders(c, a, nil)
	}))
	require.NotContains(t, c.Keys, openAIUpstreamIdentityCohortGinKey)
}

func TestL01CohortMatchesExistingBucket(t *testing.T) {
	for _, size := range []int{0, 1, 16, 19, 32, 33, 64, 65, 128, 1024} {
		seed := strings.Repeat("s", size)
		require.Equal(t, sha256SumCacheCohort(seed), openAIUpstreamIdentityCohort(seed))
		if size <= 64 {
			require.Zero(t, testing.AllocsPerRun(100, func() { _ = openAIUpstreamIdentityCohort(seed) }))
		}
	}
}

func TestL01UnsupportedIngressPrecedesAccountAndSession(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			t.Run(kind+"/enabled="+strconv.FormatBool(enabled), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(enabled, true))
				d := prepareOpenAIUpstreamIdentity(c, &Account{ID: 9, Platform: PlatformOpenAI, Type: kind})
				require.False(t, d.eligible())
				require.Equal(t, "none", d.Arm)
				want := "policy_off"
				if enabled {
					want = "unsupported_ingress"
				}
				require.Equal(t, want, d.Skip)
				_, fixed := cachePolicyL01CohortFromGin(c)
				require.False(t, fixed)
			})
		}
	}
}

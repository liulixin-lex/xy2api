//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func l01ChatTestPolicy(enabled, chat bool) *CachePolicySnapshot {
	p := &CachePolicySnapshot{master: enabled, upstream: CacheUpstreamIdentityPolicy{
		Mode: "uuid_fill", RolloutPercent: 100, ChatCompatUUID: chat, active: enabled, revision: 7,
		ForwardSessionHeaders: "whitelist",
	}}
	p.initCohortViews()
	return p
}

type l01ChatForwardFixture struct {
	service  *OpenAIGatewayService
	context  *gin.Context
	upstream *httpUpstreamRecorder
	account  *Account
	trace    *cacheTrace
	response *httptest.ResponseRecorder
}

func newL01ChatForwardFixture(t *testing.T, body string, policy *CachePolicySnapshot, headers http.Header, oauth bool) l01ChatForwardFixture {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		c.Request.Header[key] = append([]string(nil), values...)
	}
	c.Set("api_key", &APIKey{ID: 99})
	c.Set(openAICacheSessionGinKey, "v2:0123456789abcdef")
	c.Set(cachePolicySnapshotGinKey, policy)
	trace := &cacheTrace{policy: policy}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), cacheTraceKey{}, trace))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"synthetic stop"}}`)),
	}}
	account := &Account{
		ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "synthetic-test-key"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	if oauth {
		account.Type = AccountTypeOAuth
		account.Credentials = map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}
	}
	return l01ChatForwardFixture{&OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}, c, upstream, account, trace, rec}
}

func (f l01ChatForwardFixture) forward(t *testing.T, body string) {
	t.Helper()
	seed := f.service.ExtractSessionID(f.context, []byte(body))
	_, err := f.service.ForwardAsChatCompletions(f.context.Request.Context(), f.context, f.account, []byte(body), seed, "")
	require.Error(t, err, "synthetic upstream error stops after capturing the actual wire request")
	require.Len(t, f.upstream.requests, 1)
}

func TestL01ChatForwardWireAndExplicitFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const ordinary = `{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic-chat-prompt"}],"stream":false`
	tests := []struct {
		name, body           string
		headers              http.Header
		chat, oauth, applied bool
	}{
		{name: "absent", body: ordinary + `}`, chat: true, applied: true},
		{name: "empty", body: ordinary + `,"prompt_cache_key":""}`, chat: true},
		{name: "null", body: ordinary + `,"prompt_cache_key":null}`, chat: true},
		{name: "whitespace", body: ordinary + `,"prompt_cache_key":"  "}`, chat: true},
		{name: "client", body: ordinary + `,"prompt_cache_key":"synthetic-client-key"}`, chat: true},
		{name: "escaped_empty", body: ordinary + `,"prompt\u005fcache_key":""}`, chat: true},
		{name: "casefold_empty", body: ordinary + `,"PROMPT_CACHE_KEY":""}`, chat: true},
		{name: "duplicate_string_null", body: ordinary + `,"prompt_cache_key":"client-first","prompt_cache_key":null}`, chat: true},
		{name: "chat_off", body: ordinary + `}`, chat: false},
		{name: "oauth", body: ordinary + `}`, chat: true, oauth: true},
		{name: "no_compat_model", body: `{"model":"gpt-4o","messages":[{"role":"user","content":"synthetic"}],"stream":false}`, chat: true},
		{name: "responses_shape", body: `{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic"}],"stream":false}`, chat: true},
	}
	for _, header := range explicitOpenAIHeaderSessionNames {
		tests = append(tests, struct {
			name, body           string
			headers              http.Header
			chat, oauth, applied bool
		}{name: "header_" + header, body: ordinary + `}`, headers: http.Header{http.CanonicalHeaderKey(header): {"synthetic-session"}}, chat: true})
		tests = append(tests, struct {
			name, body           string
			headers              http.Header
			chat, oauth, applied bool
		}{name: "empty_header_" + header, body: ordinary + `}`, headers: http.Header{http.CanonicalHeaderKey(header): {""}}, chat: true})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseline := newL01ChatForwardFixture(t, tc.body, l01ChatTestPolicy(false, tc.chat), tc.headers, tc.oauth)
			candidate := newL01ChatForwardFixture(t, tc.body, l01ChatTestPolicy(true, tc.chat), tc.headers, tc.oauth)
			baseline.forward(t, tc.body)
			candidate.forward(t, tc.body)
			require.Equal(t, tc.applied, candidate.trace.data.L01Applied)
			switch tc.name {
			case "absent":
				require.Equal(t, "none", candidate.trace.data.L01Skip)
			case "no_compat_model", "responses_shape":
				require.Equal(t, "no_compat_seed", candidate.trace.data.L01Skip)
			case "empty", "escaped_empty", "casefold_empty":
				require.Equal(t, "client_empty", candidate.trace.data.L01Skip)
			case "client", "null", "whitespace", "duplicate_string_null":
				require.Equal(t, "client_pck", candidate.trace.data.L01Skip)
			case "chat_off":
				require.Equal(t, "chat_compat_off", candidate.trace.data.L01Skip)
			}
			require.Equal(t, baseline.response.Code, candidate.response.Code)
			require.Equal(t, baseline.response.Header(), candidate.response.Header())
			require.Equal(t, baseline.response.Body.String(), candidate.response.Body.String(), "the L-01 identity remains upstream-only")
			require.NotContains(t, candidate.response.Body.String(), "synthetic-chat-prompt")
			if !tc.applied {
				require.Equal(t, baseline.upstream.lastBody, candidate.upstream.lastBody)
				require.Equal(t, baseline.upstream.lastReq.Header, candidate.upstream.lastReq.Header)
				require.NotEqual(t, "gw_uuid", candidate.trace.data.PCKClassOut)
				require.NotEqual(t, "eq_pck", candidate.trace.data.SessOutRelation)
				return
			}
			oldPCK := gjson.GetBytes(baseline.upstream.lastBody, "prompt_cache_key").String()
			gotPCK := gjson.GetBytes(candidate.upstream.lastBody, "prompt_cache_key").String()
			require.True(t, cacheTraceUUID.MatchString(gotPCK))
			require.Equal(t, generateSessionUUID(oldPCK), gotPCK, "reuse the existing tenant-isolated compat seed")
			require.Equal(t, baseline.upstream.lastReq.Header.Get("session_id"), candidate.upstream.lastReq.Header.Get("session_id"), "header bytes remain the existing UUID")
			require.Equal(t, gotPCK, candidate.upstream.lastReq.Header.Get("session_id"))
			require.Equal(t, "gw_uuid", candidate.trace.data.PCKClassOut)
			require.Equal(t, "eq_pck", candidate.trace.data.SessOutRelation)
		})
	}
}

func TestL01ChatRecursiveContinuationRetainsOriginalSeed(t *testing.T) {
	const body = `{"model":"gpt-5.4","messages":[{"role":"system","content":"synthetic"},{"role":"user","content":"synthetic"}],"stream":false}`
	first := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(true, true), nil, false)
	first.forward(t, body)
	baseline := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(false, true), nil, false)
	baseline.forward(t, body)
	// Match the exact private continuation arguments used by task recovery:
	// the existing API-key seed is already tenant-isolated, not a UUID.
	seed := gjson.GetBytes(baseline.upstream.lastBody, "prompt_cache_key").String()
	require.False(t, cacheTraceUUID.MatchString(seed))
	continuation := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(true, true), nil, false)
	_, err := continuation.service.forwardAsChatCompletions(context.Background(), continuation.context, continuation.account, []byte(body), seed, "", true)
	require.Error(t, err)
	require.Len(t, continuation.upstream.requests, 1)
	require.Equal(t, first.upstream.lastBody, continuation.upstream.lastBody)
	require.Equal(t, first.upstream.lastReq.Header.Get("session_id"), continuation.upstream.lastReq.Header.Get("session_id"))
	require.Equal(t, generateSessionUUID(seed), continuation.upstream.lastReq.Header.Get("session_id"))
	require.NotEqual(t, generateSessionUUID(generateSessionUUID(seed)), continuation.upstream.lastReq.Header.Get("session_id"))
	require.Equal(t, "gw_uuid", continuation.trace.data.PCKClassOut)
	require.Equal(t, "eq_pck", continuation.trace.data.SessOutRelation)
}

func TestL01ChatPreservesUsageAndClientResponse(t *testing.T) {
	const payload = `data: {"type":"response.created","response":{"id":"resp-synthetic","model":"gpt-5.4","created_at":123}}

data: {"type":"response.output_text.delta","delta":"synthetic answer","output_index":0,"content_index":0}

data: {"type":"response.completed","response":{"id":"resp-synthetic","model":"gpt-5.4","created_at":123,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic answer"}]}],"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024},"output_tokens":7,"total_tokens":2055}}}

data: [DONE]

`
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "stream"}[streaming], func(t *testing.T) {
			stream := "false"
			if streaming {
				stream = "true"
			}
			body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic-prompt"}],"stream":` + stream + `}`
			var results []*OpenAIForwardResult
			var fixtures []l01ChatForwardFixture
			for _, enabled := range []bool{false, true} {
				fixture := newL01ChatForwardFixture(t, body, l01ChatTestPolicy(enabled, true), nil, false)
				fixture.upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"synthetic-response-request"}}, Body: io.NopCloser(strings.NewReader(payload))}
				result, err := fixture.service.ForwardAsChatCompletions(fixture.context.Request.Context(), fixture.context, fixture.account, []byte(body), "", "")
				require.NoError(t, err)
				require.NotNil(t, result)
				results = append(results, result)
				fixtures = append(fixtures, fixture)
			}
			require.Equal(t, results[0].Usage, results[1].Usage)
			require.Equal(t, OpenAIUsage{InputTokens: 2048, OutputTokens: 7, CacheReadInputTokens: 1024, CacheReadSource: results[0].Usage.CacheReadSource}, results[1].Usage)
			require.Equal(t, results[0].Model, results[1].Model)
			require.Equal(t, results[0].BillingModel, results[1].BillingModel)
			require.Equal(t, results[0].UpstreamModel, results[1].UpstreamModel)
			require.Equal(t, results[0].UpstreamEndpoint, results[1].UpstreamEndpoint)
			require.Equal(t, results[0].ServiceTier, results[1].ServiceTier)
			require.Equal(t, fixtures[0].response.Code, fixtures[1].response.Code)
			require.Equal(t, fixtures[0].response.Header(), fixtures[1].response.Header())
			require.Equal(t, fixtures[0].response.Body.String(), fixtures[1].response.Body.String())
			uuid := gjson.GetBytes(fixtures[1].upstream.lastBody, "prompt_cache_key").String()
			require.NotEmpty(t, uuid)
			require.NotContains(t, fixtures[1].response.Body.String(), uuid)
		})
	}
}

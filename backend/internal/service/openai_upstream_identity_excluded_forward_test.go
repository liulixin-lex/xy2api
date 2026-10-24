//go:build unit

package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These cases stop at the same synthetic upstream rejection after the real
// production entry resolves the transport and captures the outbound request.
// Exclusion must preserve every wire byte and the unchanged client error.
func TestL01ExcludedChatForwardMatchesDisabledWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body, skip, endpoint string
		raw, compat                bool
	}{
		{
			name: "raw_chat_unsupported_ingress", raw: true, compat: true,
			body: `{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic-excluded"}],"stream":false}`,
			skip: "unsupported_ingress", endpoint: "/v1/chat/completions",
		},
		{
			name: "gpt4o_no_compat_seed", compat: true,
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":"synthetic-excluded"}],"stream":false}`,
			skip: "no_compat_seed", endpoint: "/v1/responses",
		},
		{
			name: "gpt4o_no_seed_before_compat_switch", compat: false,
			body: `{"model":"gpt-4o","messages":[{"role":"user","content":"synthetic-excluded"}],"stream":false}`,
			skip: "no_compat_seed", endpoint: "/v1/responses",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline := newL01ChatForwardFixture(t, tc.body, l01ChatTestPolicy(false, tc.compat), nil, false)
			candidate := newL01ChatForwardFixture(t, tc.body, l01ChatTestPolicy(true, tc.compat), nil, false)
			for _, fixture := range []l01ChatForwardFixture{baseline, candidate} {
				fixture.account.Extra["openai_responses_supported"] = !tc.raw
				fixture.forward(t, tc.body)
				require.Equal(t, tc.endpoint, fixture.upstream.lastReq.URL.Path, "the intended production transport must really execute")
			}
			require.Equal(t, cacheTraceWireSnapshot(t, baseline.upstream.lastReq, baseline.upstream.lastBody), cacheTraceWireSnapshot(t, candidate.upstream.lastReq, candidate.upstream.lastBody))
			require.Equal(t, baseline.response.Code, candidate.response.Code)
			require.Equal(t, baseline.response.Header(), candidate.response.Header())
			require.Equal(t, baseline.response.Body.String(), candidate.response.Body.String())
			require.False(t, candidate.trace.data.L01Applied)
			require.Equal(t, tc.skip, candidate.trace.data.L01Skip)
			if tc.raw {
				require.Equal(t, "none", candidate.trace.data.L01Arm)
			} else {
				require.Equal(t, "on", candidate.trace.data.L01Arm)
			}
			require.NotEqual(t, "gw_uuid", candidate.trace.data.PCKClassOut)
			require.NotEqual(t, "eq_pck", candidate.trace.data.SessOutRelation)
		})
	}
}

func TestL01ChatResponsesFallbackRawClearsFinalTrace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const body = `{"model":"gpt-5.4","messages":[{"role":"user","content":"synthetic-retry-body"}],"stream":false}`
	const rawReply = `{"id":"cc-synthetic-retry","object":"chat.completion","created":123,"model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2000,"prompt_tokens_details":{"cached_tokens":1024},"completion_tokens":1,"total_tokens":2001}}`
	type outcome struct {
		wire, client string
		status       int
		headers      http.Header
		usage        OpenAIUsage
		billing      any
	}
	var baseline outcome
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			logs := t3SettlementCapture(t)
			f := cacheTraceNewContractFixture(t, "l01-raw-retry", "chat")
			f.body = []byte(body)
			f.c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(f.body))
			f.c.Request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			responseContext, _ := gin.CreateTestContext(recorder)
			f.c.Writer = responseContext.Writer
			snapshot := *f.s.cachePolicies.Snapshot()
			snapshot.master = enabled
			snapshot.upstream = l01ChatTestPolicy(enabled, true).upstream
			snapshot.initCohortViews()
			f.c.Set(cachePolicySnapshotGinKey, &snapshot)
			// Unknown Responses support takes the production fallback path;
			// known false would exercise direct raw routing instead.
			f.account.Extra = nil
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIRejectedFieldTestResponse(http.StatusNotFound, `{"error":{"type":"invalid_request_error","message":"synthetic unsupported endpoint"}}`),
				newOpenAIRejectedFieldTestResponse(http.StatusOK, rawReply),
			}}
			upstream.responses[1].Header.Set("X-Request-Id", f.id)
			f.s.httpUpstream = upstream
			require.NotEmpty(t, f.s.GenerateSessionHash(f.c, f.body))
			result, err := f.s.ForwardAsChatCompletions(f.c.Request.Context(), f.c, f.account, f.body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path)
			require.Equal(t, "/v1/chat/completions", upstream.requests[1].URL.Path)
			if enabled {
				firstPCK := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
				require.True(t, cacheTraceUUID.MatchString(firstPCK), "positive control: the failed attempt applied L-01")
				require.Equal(t, firstPCK, upstream.requests[0].Header.Get("session_id"))
			}
			require.False(t, gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").Exists())
			require.Empty(t, upstream.requests[1].Header.Get("session_id"))
			require.NotNil(t, result.CacheTrace)
			awaitCacheTrace(t, f.s, f.c, result)
			t3SettlementWorkerDrained(t, f.s)
			require.Equal(t, 2000, result.Usage.InputTokens)
			require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
			f.settle(t, result) // real RecordUsage, including exact billing-control assertions
			traces := t3SettlementEvents(t, logs, "cache_trace")
			usages := t3SettlementEvents(t, logs, "openai_cache_usage")
			require.Len(t, traces, 1)
			require.Len(t, usages, 1)
			t3SettlementAssertUsageLink(t, traces[0], usages[0], result, f.account)
			require.Equal(t, false, traces[0]["l01_applied"])
			require.Equal(t, "none", traces[0]["l01_arm"])
			expectedSkip := "policy_off"
			if enabled {
				expectedSkip = "unsupported_ingress"
			}
			require.Equal(t, expectedSkip, traces[0]["l01_skip"])
			require.NotEqual(t, "gw_uuid", traces[0]["pck_class_out"])
			require.NotEqual(t, "eq_pck", traces[0]["sess_out_relation"])
			current := outcome{wire: cacheTraceWireSnapshot(t, upstream.lastReq, upstream.lastBody),
				client: recorder.Body.String(), status: recorder.Code, headers: recorder.Header().Clone(),
				usage: result.Usage, billing: cacheTraceContractBillingSnapshot(t, f.rows, f.billing, nil)}
			if !enabled {
				baseline = current
			} else {
				require.Equal(t, baseline, current, "the successful raw attempt and settlement remain equivalent")
			}
		})
	}
}

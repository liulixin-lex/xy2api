//go:build unit

package service

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type l01PassthroughResult struct {
	wire, clientBody string
	clientStatus     int
	clientHeaders    http.Header
	forward          any
	billing          any
}

// Use real account predicates and the public Forward/RecordUsage paths. OAuth
// and setup-token accounts also use Codex protocol, but not_apikey precedes
// codex_protocol and passthrough in the contract. The current Account type has
// no API-key/Codex combination; do not fake one with a label-only fixture.
func TestL01PassthroughForwardExclusionAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
		for _, stream := range []bool{false, true} {
			for _, clientPCK := range []bool{false, true} {
				name := fmt.Sprintf("%s/stream=%t/client_pck=%t", accountType, stream, clientPCK)
				t.Run(name, func(t *testing.T) {
					var baseline l01PassthroughResult
					for _, enabled := range []bool{false, true} {
						t.Run(fmt.Sprintf("l01=%t", enabled), func(t *testing.T) {
							actual := l01PassthroughForwardRun(t, accountType, stream, clientPCK, enabled)
							if !enabled {
								baseline = actual
								return
							}
							require.Equal(t, baseline, actual, "excluded forwarding, parsed usage, billing arguments/calls and client output must remain identical")
						})
					}
				})
			}
		}
	}
}

func l01PassthroughForwardRun(t *testing.T, accountType string, stream, clientPCK, enabled bool) l01PassthroughResult {
	t.Helper()
	f := cacheTraceNewContractFixture(t, "l01-passthrough", "responses")
	state := l01DisabledWireState{mode: "off", observer: true}
	if enabled {
		state.master, state.mode, state.percent = true, "uuid_fill", 100
	}
	l01WireConfigure(t, state)(f.s)
	f.account.Type = accountType
	f.account.Extra["openai_passthrough"] = true
	if accountType != AccountTypeAPIKey {
		f.account.Credentials = map[string]any{"access_token": "l01-excluded-private-token", "chatgpt_account_id": "l01-excluded-private-account"}
	}
	require.True(t, f.account.IsOpenAIPassthroughEnabled())
	require.Equal(t, accountType != AccountTypeAPIKey, f.account.UsesOpenAICodexProtocol())
	require.Equal(t, accountType == AccountTypeAPIKey, f.account.IsOpenAIApiKey())
	pck := ""
	if clientPCK {
		pck = `,"prompt_cache_key":"l01-excluded-private-pck"`
	}
	f.body = []byte(fmt.Sprintf(`{"model":"gpt-5.4","instructions":"l01-excluded-private-instructions","input":[{"role":"user","content":"l01-excluded-private-body"}],"stream":%t%s}`, stream, pck))
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(f.body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "codex_cli_rs/l01-excluded-private-ua")
	c.Request.Header.Set("X-Codex-Window-Id", "l01-excluded-private-window:4")
	c.Set("api_key", f.api)
	f.c = c
	logs := t3SettlementCapture(t)
	result := f.forward(t, "responses", stream)
	require.NotNil(t, result.CacheTrace, "use a real enabled observer and successful upstream attempt")
	awaitCacheTrace(t, f.s, f.c, result)
	t3SettlementWorkerDrained(t, f.s)
	wantSkip := "policy_off"
	if enabled {
		wantSkip = "passthrough"
		if accountType != AccountTypeAPIKey {
			wantSkip = "not_apikey"
		}
	}
	require.Equal(t, "none", result.CacheTrace.data.L01Arm)
	require.Equal(t, wantSkip, result.CacheTrace.data.L01Skip)
	require.False(t, result.CacheTrace.data.L01Applied)
	_, cohortFixed := cachePolicyL01CohortFromGin(f.c)
	require.False(t, cohortFixed, "passthrough exclusion must not run cohort preparation")
	upstream, ok := f.s.httpUpstream.(*httpUpstreamRecorder)
	require.True(t, ok)
	require.Len(t, upstream.requests, 1, "observation must not add upstream attempts")
	wire := cacheTraceWireSnapshot(t, upstream.requests[0], upstream.bodies[0])
	// Normalize only wall durations; every parsed token/model/endpoint field is
	// compared across switches and the real settlement call is kept intact.
	result.Duration = time.Second
	if result.FirstTokenMs != nil {
		first := 17
		result.FirstTokenMs = &first
	}
	forward := cacheBillingJSON(t, result)
	f.settle(t, result)
	traces := t3SettlementEvents(t, logs, "cache_trace")
	usage := t3SettlementEvents(t, logs, "openai_cache_usage")
	require.Len(t, traces, 1)
	require.Len(t, usage, 1)
	payload := t3SettlementPayload(t, traces[0], false, result.CacheTrace.policy.Revision())
	require.Equal(t, "none", payload["l01_arm"])
	require.Equal(t, wantSkip, payload["l01_skip"])
	require.Equal(t, false, payload["l01_applied"])
	require.NotEqual(t, "gw_uuid", payload["pck_class_out"])
	require.NotEqual(t, "eq_pck", payload["sess_out_relation"])
	t3SettlementAssertUsageLink(t, payload, usage[0], result, f.account)
	for _, marker := range []string{"l01-excluded-private", "ctc-private"} {
		require.NotContains(t, logs.String(), marker)
	}
	for key := range response.Header() {
		require.NotContains(t, strings.ToLower(key), "l01")
	}
	require.NotContains(t, response.Body.String(), "l01_")
	return l01PassthroughResult{
		wire: wire, clientBody: response.Body.String(), clientStatus: response.Code,
		clientHeaders: response.Header().Clone(), forward: forward,
		billing: cacheTraceContractBillingSnapshot(t, f.rows, f.billing, nil),
	}
}

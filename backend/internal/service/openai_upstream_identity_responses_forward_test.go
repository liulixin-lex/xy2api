//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	appLogger "github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const l01ResponsesSuccess = `{"id":"resp-l01-synthetic","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024},"output_tokens":7}}`

type l01ResponsesForwardFixture struct {
	service  *OpenAIGatewayService
	context  *gin.Context
	upstream *httpUpstreamRecorder
	account  *Account
	trace    *cacheTrace
	response *httptest.ResponseRecorder
}

func newL01ResponsesForwardFixture(t *testing.T, body string, policy *CachePolicySnapshot, keyID, groupID int64) l01ResponsesForwardFixture {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &APIKey{ID: keyID, GroupID: &groupID})
	c.Set(cachePolicySnapshotGinKey, policy)
	trace := &cacheTrace{policy: policy}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), cacheTraceKey{}, trace))
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(http.StatusOK, l01ResponsesSuccess)}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-l01-routing-secret"}}
	service := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	account := newOpenAIRejectedFieldTestAccount()
	account.ID = 41
	return l01ResponsesForwardFixture{service, c, upstream, account, trace, rec}
}

func (f l01ResponsesForwardFixture) forward(t *testing.T, body string) *OpenAIForwardResult {
	t.Helper()
	require.NotEmpty(t, f.service.GenerateSessionHash(f.context, []byte(body)), "the fixture must use a real scoped routing identity")
	result, err := f.service.Forward(f.context.Request.Context(), f.context, f.account, []byte(body))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, f.upstream.requests)
	return result
}

func requireL01ResponsesWireIdentity(t *testing.T, f l01ResponsesForwardFixture, expected string) {
	t.Helper()
	for i, body := range f.upstream.bodies {
		require.Equal(t, expected, gjson.GetBytes(body, "prompt_cache_key").String(), "attempt %d body", i)
		require.Equal(t, expected, f.upstream.requests[i].Header.Get("session_id"), "attempt %d header", i)
	}
}

func TestL01ResponsesForwardExplicitPCKMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const prefix = `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"synthetic-prefix"}],"stream":false`
	for _, tc := range []struct {
		name, suffix string
		applied      bool
	}{
		{"absent", `}`, true},
		{"empty", `,"prompt_cache_key":""}`, false},
		{"null", `,"prompt_cache_key":null}`, false},
		{"whitespace", `,"prompt_cache_key":"   "}`, false},
		{"client", `,"prompt_cache_key":"synthetic-client-value"}`, false},
		{"client_uuid", `,"prompt_cache_key":"d626e6ee-f555-449f-b5fa-dcaa38baa57b"}`, false},
		{"number", `,"prompt_cache_key":42}`, false},
		{"bool", `,"prompt_cache_key":false}`, false},
		{"array", `,"prompt_cache_key":["synthetic"]}`, false},
		{"object", `,"prompt_cache_key":{"synthetic":1}}`, false},
		{"duplicate_empty_value", `,"prompt_cache_key":"","prompt_cache_key":"synthetic-last"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := prefix + tc.suffix
			baseline := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(false, true), 99, 10)
			candidate := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
			baseResult := baseline.forward(t, body)
			result := candidate.forward(t, body)
			require.Equal(t, baseResult.Usage, result.Usage)
			require.Equal(t, baseline.response.Code, candidate.response.Code)
			require.Equal(t, baseline.response.Header(), candidate.response.Header())
			require.Equal(t, baseline.response.Body.String(), candidate.response.Body.String())
			require.Equal(t, tc.applied, candidate.trace.data.L01Applied)
			if !tc.applied {
				expectedSkip := "client_pck"
				if tc.name == "empty" || tc.name == "duplicate_empty_value" {
					expectedSkip = "client_empty" // original first gjson value is an empty string
				}
				require.Equal(t, expectedSkip, candidate.trace.data.L01Skip)
				require.Equal(t, baseline.upstream.lastBody, candidate.upstream.lastBody, "explicit pck must retain baseline wire bytes")
				require.Equal(t, baseline.upstream.lastReq.Header, candidate.upstream.lastReq.Header)
				require.NotEqual(t, "gw_uuid", candidate.trace.data.PCKClassOut)
				return
			}
			routing, ok := candidate.context.Get(openAICacheSessionGinKey)
			require.True(t, ok)
			session, ok := routing.(string)
			require.True(t, ok)
			uuid := generateSessionUUID("openai-upstream-session:v1:" + session)
			requireL01ResponsesWireIdentity(t, candidate, uuid)
			require.Equal(t, "gw_uuid", candidate.trace.data.PCKClassOut)
			require.Equal(t, "eq_pck", candidate.trace.data.SessOutRelation)
			require.NotContains(t, candidate.response.Body.String(), uuid)
			require.NotContains(t, candidate.response.Header().Get("session_id"), uuid)
		})
	}
}

func TestL01ResponsesForwardServingScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const body = `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"synthetic-stable-history"}],"stream":false}`
	first := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
	first.forward(t, body)
	firstUUID := gjson.GetBytes(first.upstream.lastBody, "prompt_cache_key").String()
	for _, tc := range []struct {
		name                      string
		keyID, groupID, accountID int64
		model, host               string
		matchesFirst              bool
	}{
		{"same_scope_new_account_host", 99, 10, 42, "gpt-5.4", "https://second.example", true},
		{"other_serving_group", 99, 11, 41, "gpt-5.4", "https://first.example", false},
		{"other_api_key", 100, 10, 41, "gpt-5.4", "https://first.example", false},
		{"other_model", 99, 10, 41, "gpt-5.5", "https://first.example", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scopedBody := strings.Replace(body, "gpt-5.4", tc.model, 1)
			f := newL01ResponsesForwardFixture(t, scopedBody, l01ChatTestPolicy(true, true), tc.keyID, tc.groupID)
			f.account.ID = tc.accountID
			f.account.Credentials["base_url"] = tc.host
			f.forward(t, scopedBody)
			uuid := gjson.GetBytes(f.upstream.lastBody, "prompt_cache_key").String()
			require.NotEmpty(t, uuid)
			requireL01ResponsesWireIdentity(t, f, uuid)
			if tc.matchesFirst {
				require.Equal(t, firstUUID, uuid)
			} else {
				require.NotEqual(t, firstUUID, uuid)
			}
		})
	}
}

func TestL01ResponsesForwardCohortSurvivesAccountAndGroupChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const body = `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"synthetic-cohort"}],"stream":false}`
	policy := l01ChatTestPolicy(true, true)
	policy.upstream.RolloutPercent = 50
	policy.initCohortViews()
	f := newL01ResponsesForwardFixture(t, body, policy, 99, 10)
	keyValue, _ := f.context.Get("api_key")
	key, ok := keyValue.(*APIKey)
	require.True(t, ok)
	var treatmentGroup, controlGroup int64
	var treatmentRouting, controlRouting string
	for group := int64(1); group <= 256 && (treatmentGroup == 0 || controlGroup == 0); group++ {
		key.GroupID = &group
		routing := f.service.GenerateSessionHash(f.context, []byte(body))
		if openAIUpstreamIdentityCohort(routing) < 50 {
			treatmentGroup, treatmentRouting = group, routing
		} else {
			controlGroup, controlRouting = group, routing
		}
	}
	require.NotZero(t, treatmentGroup)
	require.NotZero(t, controlGroup)
	key.GroupID = &treatmentGroup
	require.Equal(t, treatmentRouting, f.service.GenerateSessionHash(f.context, []byte(body)))
	_, err := f.service.Forward(f.context.Request.Context(), f.context, f.account, []byte(body))
	require.NoError(t, err)
	require.Equal(t, "on", f.trace.data.L01Arm)
	firstUUID := gjson.GetBytes(f.upstream.bodies[0], "prompt_cache_key").String()
	// A different serving group has the opposite independent cohort, but this
	// logical request keeps its first arm while using the new scoped UUID.
	key.GroupID = &controlGroup
	f.account.ID++
	require.Equal(t, controlRouting, f.service.GenerateSessionHash(f.context, []byte(body)))
	f.upstream.resp = newOpenAIRejectedFieldTestResponse(http.StatusOK, l01ResponsesSuccess)
	_, err = f.service.Forward(f.context.Request.Context(), f.context, f.account, []byte(body))
	require.NoError(t, err)
	require.Len(t, f.upstream.bodies, 2)
	require.Equal(t, "on", f.trace.data.L01Arm)
	require.True(t, f.trace.data.L01Applied)
	secondUUID := gjson.GetBytes(f.upstream.bodies[1], "prompt_cache_key").String()
	require.NotEqual(t, firstUUID, secondUUID)
	require.Equal(t, generateSessionUUID("openai-upstream-session:v1:"+controlRouting), secondUUID)
	require.Equal(t, secondUUID, f.upstream.requests[1].Header.Get("session_id"))
	independent := newL01ResponsesForwardFixture(t, body, policy, 99, controlGroup)
	independent.forward(t, body)
	require.Equal(t, "off", independent.trace.data.L01Arm)
	require.Equal(t, "arm_off", independent.trace.data.L01Skip)
	require.False(t, independent.trace.data.L01Applied)
	require.False(t, gjson.GetBytes(independent.upstream.lastBody, "prompt_cache_key").Exists())
}

func TestL01ResponsesForwardRebuildsKeepOneIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"invalid_encrypted", "rejected_field", "web_search", "compact"} {
		t.Run(kind, func(t *testing.T) {
			withResilienceSettings(t, nil)
			resetWebSearchDeclarations(t)
			body := `{"model":"gpt-5.5","stream":false,"input":[{"type":"message","role":"user","content":"synthetic-rebuild"}]}`
			var firstResponse *http.Response
			switch kind {
			case "invalid_encrypted":
				body = `{"model":"gpt-5.5","stream":false,"input":[{"type":"reasoning","encrypted_content":"synthetic-stale","summary":[{"type":"summary_text","text":"synthetic-summary"}]},{"type":"message","role":"user","content":"synthetic-rebuild"}]}`
				firstResponse = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"bad encrypted content"}}`)
			case "rejected_field":
				body = `{"model":"gpt-5.5","stream":false,"truncation":"auto","input":[{"type":"message","role":"user","content":"synthetic-rebuild"}]}`
				firstResponse = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: 'truncation'.","param":"truncation"}}`)
			case "web_search":
				body = openAIWebSearchHistoryCompactionBody
				firstResponse = webSearchRejectionHTTP()
			case "compact":
				body = `{"model":"gpt-5.5","stream":false,"input":[{"type":"message","role":"user","content":"synthetic-rebuild"},{"type":"compaction_trigger"}]}`
				firstResponse = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"context_length_exceeded","message":"context window exceeded"}}`)
			}
			f := newL01ResponsesForwardFixture(t, body, l01ChatTestPolicy(true, true), 99, 10)
			f.upstream.resp = nil
			f.upstream.responses = []*http.Response{firstResponse, newOpenAIRejectedFieldTestResponse(http.StatusOK, l01ResponsesSuccess)}
			if kind == "compact" {
				f.service.cfg.Gateway.OpenAICompactModel = "gpt-5.4"
				MarkOpenAINativeCompactionV2(f.context)
			}
			if kind == "web_search" {
				req := newWebSearchReplayRequest(t)
				req.c.Set("api_key", &APIKey{ID: 99, GroupID: new(int64)})
				req.c.Set(cachePolicySnapshotGinKey, l01ChatTestPolicy(true, true))
				f.context, f.response = req.c, req.rec
				f.trace = &cacheTrace{policy: CachePolicyFromGin(f.context)}
				f.context.Request = f.context.Request.WithContext(context.WithValue(f.context.Request.Context(), cacheTraceKey{}, f.trace))
				f.upstream.responses[1] = webSearchSuccessStream("resp-l01-rebuild")
				f.service = newOpenAIImageGenerationControlTestService(f.upstream)
				f.account = webSearchReplayAccount(false)
			}
			f.forward(t, body)
			require.Len(t, f.upstream.bodies, 2, "the production %s retry must actually run", kind)
			uuid := gjson.GetBytes(f.upstream.bodies[0], "prompt_cache_key").String()
			require.True(t, cacheTraceUUID.MatchString(uuid))
			requireL01ResponsesWireIdentity(t, f, uuid)
			require.True(t, f.trace.data.L01Applied)
			switch kind {
			case "invalid_encrypted":
				require.True(t, gjson.GetBytes(f.upstream.bodies[0], "input.0.encrypted_content").Exists())
				require.False(t, gjson.GetBytes(f.upstream.bodies[1], "input.0.encrypted_content").Exists())
			case "rejected_field":
				require.True(t, gjson.GetBytes(f.upstream.bodies[0], "truncation").Exists())
				require.False(t, gjson.GetBytes(f.upstream.bodies[1], "truncation").Exists())
			case "web_search":
				requireWebSearchNotDeclared(t, f.upstream.bodies[0])
				requireWebSearchDeclared(t, f.upstream.bodies[1])
			case "compact":
				require.Equal(t, "gpt-5.5", gjson.GetBytes(f.upstream.bodies[0], "model").String())
				require.Equal(t, "gpt-5.4", gjson.GetBytes(f.upstream.bodies[1], "model").String())
				require.True(t, HasCompactionTriggerInInput(f.upstream.bodies[1]))
			}
			// The UUID is transport metadata and never enters the user response.
			require.NotContains(t, f.response.Body.String(), uuid, "%s response", kind)
		})
	}
}

// Isolate the real slog -> zap bridge, whose initialization changes global
// logger state. Forward and RecordUsage produce both events; no emit helper
// or manually constructed event substitutes for the production request path.
func TestL01ResponsesForwardProductionLoggerPrivacyAndBilling(t *testing.T) {
	const childEnv = "L01_RESPONSES_LOGGER_CHILD"
	if path := os.Getenv(childEnv); path != "" {
		require.NoError(t, appLogger.Init(appLogger.InitOptions{Level: "info", Format: "json",
			ServiceName: "l01-responses-forward-test", Environment: "test", StacktraceLevel: "none",
			Output: appLogger.OutputOptions{ToFile: true, FilePath: path}, Sampling: appLogger.SamplingOptions{Enabled: false}}))
		t.Cleanup(appLogger.Sync)
		var referenceUsage OpenAIUsage
		var referenceBilling any
		var referenceHeader http.Header
		var referenceBody string
		var injectedUUID string
		for _, enabled := range []bool{false, true} {
			f := cacheTraceNewContractFixture(t, "l01", "responses")
			f.body = []byte(`{"input":[{"role":"user","content":"ctc-private-l01-body"}],"stream":false,"model":"gpt-5.4"}`)
			f.c.Request.Header.Del("Session_id")
			f.c.Request.Header.Set("X-L01-Private", "ctc-private-l01-header")
			f.c.Request.Header.Set("User-Agent", "curl/ctc-private-l01-UA")
			snapshot := *f.s.cachePolicies.Snapshot()
			snapshot.master = enabled
			snapshot.upstream = l01ChatTestPolicy(enabled, true).upstream
			snapshot.initCohortViews()
			f.c.Set(cachePolicySnapshotGinKey, &snapshot)
			recorder := httptest.NewRecorder()
			responseContext, _ := gin.CreateTestContext(recorder)
			f.c.Writer = responseContext.Writer
			result := f.forward(t, "responses", false)
			awaitCacheTrace(t, f.s, f.c, result)
			t3SettlementWorkerDrained(t, f.s)
			upstream, ok := f.s.httpUpstream.(*httpUpstreamRecorder)
			require.True(t, ok)
			if enabled {
				injectedUUID = gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String()
				require.True(t, cacheTraceUUID.MatchString(injectedUUID))
				require.Equal(t, injectedUUID, upstream.lastReq.Header.Get("session_id"))
				require.True(t, result.CacheTrace.data.L01Applied)
				require.Equal(t, "gw_uuid", result.CacheTrace.data.PCKClassOut)
				require.Equal(t, "eq_pck", result.CacheTrace.data.SessOutRelation)
				require.Equal(t, referenceUsage, result.Usage)
				require.Equal(t, referenceHeader, recorder.Header())
				require.Equal(t, referenceBody, recorder.Body.String())
				require.NotContains(t, recorder.Body.String(), injectedUUID)
				require.NotContains(t, recorder.Header(), "Session_id")
			} else {
				referenceUsage = result.Usage
				referenceHeader, referenceBody = recorder.Header().Clone(), recorder.Body.String()
			}
			require.NotContains(t, recorder.Body.String(), "ctc-private")
			f.settle(t, result)
			billing := cacheTraceContractBillingSnapshot(t, f.rows, f.billing, nil)
			if enabled {
				require.Equal(t, referenceBilling, billing, "same usage yields identical billing arguments, records, calls and errors")
			} else {
				referenceBilling = billing
			}
		}
		appLogger.Sync()
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "ctc-private")
		require.NotContains(t, string(raw), injectedUUID)
		var traces []map[string]any
		var usages []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			var event map[string]any
			require.NoError(t, json.Unmarshal(line, &event))
			switch event["msg"] {
			case "cache_trace":
				traces = append(traces, event)
			case "openai_cache_usage":
				usages = append(usages, event)
			}
		}
		require.Len(t, traces, 2)
		require.Len(t, usages, 2)
		require.Equal(t, true, traces[1]["l01_applied"])
		require.Equal(t, "gw_uuid", traces[1]["pck_class_out"])
		require.Equal(t, "eq_pck", traces[1]["sess_out_relation"])
		for i := range traces {
			for _, key := range []string{"session_digest", "request_id_hmac"} {
				require.Equal(t, usages[i][key], traces[i][key])
			}
			require.Equal(t, usages[i]["input_tokens"], traces[i]["P"])
			require.Equal(t, usages[i]["cache_read_tokens"], traces[i]["read"])
		}
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "l01-responses.jsonl")
	cmd := exec.Command(executable, "-test.run=^TestL01ResponsesForwardProductionLoggerPrivacyAndBilling$", "-test.count=1", "-test.timeout=3m")
	cmd.Env = append(os.Environ(), childEnv+"="+path)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "real logger Forward/settlement test: %s", output)
}

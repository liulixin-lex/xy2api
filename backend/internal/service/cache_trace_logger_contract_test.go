package service

// This contract test uses the production slog -> zap bridge in an isolated
// test subprocess. Artifact locations are optional environment variables; no
// developer workspace or private analysis path belongs in repository tests.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	appLogger "github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const cacheTraceContractChildEnv = "CACHE_TRACE_CONTRACT_CHILD"
const cacheTraceContractPathEnv = "CACHE_TRACE_CONTRACT_LOG_PATH"
const cacheTraceContractManifestEnv = "CACHE_TRACE_CONTRACT_MANIFEST_PATH"

func TestCacheTraceProductionLoggerContract(t *testing.T) {
	if os.Getenv(cacheTraceContractChildEnv) == "1" {
		cacheTraceRunProductionLoggerContract(t)
		return
	}
	path := os.Getenv(cacheTraceContractPathEnv)
	if path == "" {
		path = filepath.Join(t.TempDir(), "actual-events.jsonl")
	}
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "the evidence file must be new, never append to an earlier run")
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestCacheTraceProductionLoggerContract$", "-test.count=1", "-test.timeout=4m")
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, cacheTraceContractChildEnv+"=") || strings.HasPrefix(entry, cacheTraceContractPathEnv+"=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, cacheTraceContractChildEnv+"=1", cacheTraceContractPathEnv+"="+path)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "isolated production logger test failed: %s", output)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Positive(t, info.Size())
}

type cacheTraceContractFixture struct {
	s              *OpenAIGatewayService
	c              *gin.Context
	api            *APIKey
	user           *User
	account        *Account
	rows           *openAIRecordUsageLogRepoStub
	billing        *openAIRecordUsageBillingRepoStub
	body           []byte
	id             string
	pricingModel   string // only for the model-less no-session service fixture
	routingSession string // compatibility fixture for a previously selected legacy digest
}

func cacheTraceNewContractFixture(t *testing.T, name, protocol string) *cacheTraceContractFixture {
	t.Helper()
	rows := &openAIRecordUsageLogRepoStub{inserted: true}
	billing := &openAIRecordUsageBillingRepoStub{}
	s := newOpenAIRecordUsageServiceWithBillingRepoForTest(rows, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	s.cfg.JWT.Secret = "ctc-private-signing-secret"
	s.cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	traceTestConfig(s)
	api := openAIRecordUsageAPIKeyWithGroup(s, 1000, false)
	groupID := int64(1)
	api.GroupID = &groupID
	api.Group.Platform = PlatformOpenAI
	api.Group.RateMultiplier = 1
	user := &User{ID: 2000}
	api.User = user
	path, field := "/v1/responses", "input"
	if protocol == "chat" {
		path, field = "/v1/chat/completions", "messages"
	}
	// Put model and pck after input, as real Codex/OAuth bodies do. All marker
	// text is synthetic and must be absent from both target log event kinds.
	body := []byte(fmt.Sprintf(`{"%s":[{"role":"user","content":"ctc-private-body-%s"}],"instructions":"ctc-private-instructions","client_metadata":{"thread_id":"ctc-private-thread"},"reasoning":{"context":"all_turns"},"include":["reasoning.encrypted_content"],"store":false,"stream":false,"prompt_cache_key":"ctc-private-pck-%s","model":"gpt-5.4"}`, field, name, name))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Session_id", "ctc-private-session-"+name)
	c.Request.Header.Set("User-Agent", "CoDeX_DeSkToP/ctc-private-UA-version (ctc-private-UA-suffix)")
	c.Request.Header.Set("X-Openai-Subagent", "ctc-private-subagent")
	c.Request.Header.Set("X-Codex-Parent-Thread-Id", "ctc-private-parent")
	c.Request.Header.Set("X-Codex-Window-Id", "ctc-private-window:23")
	c.Set("api_key", api)
	account := &Account{ID: 3000, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "ctc-private-api-key"},
		Extra:       map[string]any{"openai_responses_supported": true}}
	return &cacheTraceContractFixture{s: s, c: c, api: api, user: user, account: account, rows: rows, billing: billing, body: body, id: "ctc-private-request-" + name}
}

func (f *cacheTraceContractFixture) forward(t *testing.T, protocol string, stream bool) *OpenAIForwardResult {
	t.Helper()
	return f.forwardReply(t, protocol, stream, traceForwardSSE)
}

func (f *cacheTraceContractFixture) forwardReply(t *testing.T, protocol string, stream bool, reply string) *OpenAIForwardResult {
	t.Helper()
	if stream {
		f.body = bytes.Replace(f.body, []byte(`"stream":false`), []byte(`"stream":true`), 1)
	}
	response := traceForwardOK()
	response.Body = io.NopCloser(strings.NewReader(reply))
	response.Header.Set("X-Request-Id", f.id)
	f.s.httpUpstream = &httpUpstreamRecorder{resp: response}
	f.s.GenerateSessionHash(f.c, f.body)
	if f.routingSession != "" {
		f.c.Set(openAICacheSessionGinKey, f.routingSession)
		f.s.beginCacheTrace(f.c, f.body)
		f.s.captureCacheDiagnostic(f.c, f.body)
	}
	var result *OpenAIForwardResult
	var err error
	if protocol == "chat" {
		result, err = f.s.ForwardAsChatCompletions(f.c.Request.Context(), f.c, f.account, f.body, "", "")
	} else {
		result, err = f.s.Forward(f.c.Request.Context(), f.c, f.account, f.body)
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.CacheDiagnostic)
	return result
}

func (f *cacheTraceContractFixture) captured(t *testing.T) *http.Request {
	t.Helper()
	f.s.GenerateSessionHash(f.c, f.body)
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", bytes.NewReader(f.body))
	require.NoError(t, err)
	req.Header.Set("Session_id", "ctc-private-outbound-session")
	f.s.captureCacheTraceRequest(f.c, req, f.account, "responses")
	f.s.captureCacheDiagnostic(f.c, f.body)
	require.NotNil(t, cacheTraceFromGin(f.c))
	return req
}

func (f *cacheTraceContractFixture) finish(t *testing.T, usage *OpenAIUsage) *OpenAIForwardResult {
	t.Helper()
	result := &OpenAIForwardResult{RequestID: f.id, Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: *usage, Duration: time.Second}
	f.s.finishCacheDiagnostic(f.c, f.body, result)
	require.NotNil(t, result.CacheDiagnostic)
	return result
}

func cacheTraceContractBillingSnapshot(t *testing.T, rows *openAIRecordUsageLogRepoStub, billing *openAIRecordUsageBillingRepoStub, err error) any {
	t.Helper()
	if rows.lastLog != nil {
		rows.lastLog.CreatedAt = time.Time{}
	}
	if billing.lastCmd != nil && billing.lastCmd.Usage != nil {
		billing.lastCmd.Usage.CreatedAt = time.Time{}
	}
	return cacheBillingJSON(t, map[string]any{"error": cacheBillingErr(err), "row_calls": rows.calls,
		"billing_calls": billing.calls, "usage": rows.lastLog, "command": billing.lastCmd})
}

// Use the real billing entry once. The no-observer control uses identical
// parsed usage and RecordUsage arguments; only observation sidecars differ.
func (f *cacheTraceContractFixture) settle(t *testing.T, result *OpenAIForwardResult) string {
	t.Helper()
	return f.settleContext(t, context.Background(), result)
}

func (f *cacheTraceContractFixture) settleContext(t *testing.T, ctx context.Context, result *OpenAIForwardResult) string {
	t.Helper()
	requestHMAC := f.s.cacheUsageRequestDigest(result)
	require.Regexp(t, `^[0-9a-f]{64}$`, requestHMAC)
	result.RequestID = "ctc-private-mutated-after-snapshot-" + f.id
	result.Duration = time.Second
	input := &OpenAIRecordUsageInput{Result: result, APIKey: f.api, User: f.user, Account: f.account,
		OriginalModel: f.pricingModel, PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
	controlRows := &openAIRecordUsageLogRepoStub{inserted: true}
	controlBilling := &openAIRecordUsageBillingRepoStub{}
	control := newOpenAIRecordUsageServiceWithBillingRepoForTest(controlRows, controlBilling, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	control.resolver = NewModelPricingResolver(nil, control.billingService)
	controlResult, controlInput := *result, *input
	controlResult.CacheTrace, controlResult.cacheTraceFailure, controlResult.CacheDiagnostic = nil, nil, nil
	controlInput.Result = &controlResult
	controlErr := control.RecordUsage(ctx, &controlInput)
	err := f.s.RecordUsage(ctx, input)
	require.NoError(t, err)
	require.Equal(t, 1, f.rows.calls)
	require.Equal(t, 1, f.billing.calls)
	require.Equal(t, cacheTraceContractBillingSnapshot(t, controlRows, controlBilling, controlErr), cacheTraceContractBillingSnapshot(t, f.rows, f.billing, err), "observer must not affect billing parameters, records, calls, or errors")
	// Retry the observation settlement hook, not billing; there must still be
	// exactly one trace and one legacy usage event for this request.
	f.s.settleCacheTrace(ctx, result, f.account)
	return requestHMAC
}

func cacheTraceRunProductionLoggerContract(t *testing.T) {
	path := os.Getenv(cacheTraceContractPathEnv)
	require.NotEmpty(t, path)
	require.NoError(t, appLogger.Init(appLogger.InitOptions{Level: "info", Format: "json",
		ServiceName: "cache-trace-contract-test", Environment: "test", Caller: true, StacktraceLevel: "none",
		Output: appLogger.OutputOptions{ToFile: true, FilePath: path}, Sampling: appLogger.SamplingOptions{Enabled: false}}))
	t.Cleanup(appLogger.Sync)
	manifest := make(map[string]string)
	noSessionUsage := make(map[string]OpenAIUsage)
	var blockHashes []uint64

	for _, tc := range []struct {
		name, protocol                          string
		stream, passthrough, raw, legacyRouting bool
	}{
		{name: "admitted_responses", protocol: "responses"},
		{name: "admitted_responses_stream", protocol: "responses", stream: true},
		{name: "admitted_chat_buffered", protocol: "chat"},
		{name: "admitted_chat_stream", protocol: "chat", stream: true},
		{name: "admitted_passthrough_buffered", protocol: "responses", passthrough: true},
		{name: "admitted_passthrough_stream", protocol: "responses", passthrough: true, stream: true},
		{name: "admitted_rawcc_buffered", protocol: "chat", raw: true},
		{name: "admitted_rawcc_stream", protocol: "chat", raw: true, stream: true},
		{name: "admitted_legacy_routing", protocol: "responses", legacyRouting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, tc.name, tc.protocol)
			f.account.Extra["openai_passthrough"] = tc.passthrough
			f.account.Extra["openai_responses_supported"] = !tc.raw
			if tc.legacyRouting {
				// Inject a previously selected legacy digest while retaining the
				// authenticated key required by both trace and usage diagnostics.
				// This covers logger compatibility, not a legacy routing decision.
				f.routingSession, _ = deriveOpenAISessionHashes(explicitOpenAIRequestSessionID(f.c, f.body))
				require.Regexp(t, `^[0-9a-f]{16}$`, f.routingSession)
				f.c.Request = f.c.Request.WithContext(resilience.WithRequestState(f.c.Request.Context(), resilience.NewRequestState()))
				f.s.GenerateSessionHash(f.c, f.body)
				noteCacheTraceRebind(f.c.Request.Context(), resilience.CategoryStall)
				NoteCacheTraceFailover(f.c.Request.Context(), 1)
			}
			reply := traceForwardSSE
			if tc.raw {
				reply = traceAllocationSSE(2, true)
			}
			result := f.forwardReply(t, tc.protocol, tc.stream, reply)
			require.NotNil(t, result.CacheTrace)
			awaitCacheTrace(t, f.s, f.c, result)
			require.True(t, result.CacheTrace.data.Prefix.Sampled)
			blockHashes = append(blockHashes, result.CacheTrace.chain.hashes...)
			blockHashes = append(blockHashes, result.CacheTrace.chain.final)
			manifest[tc.name] = f.settle(t, result)
		})
	}

	for _, name := range []string{"admitted_no_session", "budget_full_no_session", "observer_failed_no_session_panic", "observer_failed_no_session_error"} {
		t.Run(name, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, name, "responses")
			f.body = []byte(cacheTraceNoSessionBody)
			f.c.Request.Header.Del("Session_id")
			// This defensive service-layer fixture has no model/seed. Supply
			// pricing context through the existing billing input, never by
			// changing observed P/read, request identity, or session digest.
			f.pricingModel = "gpt-5.4"
			require.Empty(t, deriveOpenAIContentSessionSeed(f.body))
			require.Empty(t, f.s.GenerateSessionHash(f.c, f.body))
			require.NotNil(t, cacheTraceFromGin(f.c))
			var result *OpenAIForwardResult
			if strings.HasPrefix(name, "observer_failed_") {
				f.captured(t)
				trace := cacheTraceFromGin(f.c)
				usage, complete := f.s.parseSSEUsageObserving(traceForwardSSE, func([]byte) error {
					if strings.HasSuffix(name, "panic") {
						panic("ctc-private-no-session-observer-panic")
					}
					return errors.New("ctc-private-no-session-observer-error")
				}, trace)
				require.False(t, complete)
				require.True(t, trace.usageObservationFailed)
				result = f.finish(t, usage)
				require.Nil(t, result.CacheTrace)
				require.NotNil(t, result.cacheTraceFailure)
			} else {
				if name == "budget_full_no_session" {
					f.s.cacheTraceWorkerPool().budget = 1
				}
				result = f.forward(t, "responses", false)
				require.NotNil(t, result.CacheTrace)
				if name == "admitted_no_session" {
					awaitCacheTrace(t, f.s, f.c, result)
					t3SettlementWorkerDrained(t, f.s)
					require.True(t, result.CacheTrace.data.Prefix.Sampled)
					blockHashes = append(blockHashes, result.CacheTrace.chain.hashes...)
					blockHashes = append(blockHashes, result.CacheTrace.chain.final)
				}
			}
			require.Empty(t, result.CacheDiagnostic.Session)
			noSessionUsage[name] = result.Usage
			manifest[name] = f.settle(t, result)
			require.Empty(t, f.s.cacheTelemetry().sessions, "empty sessions never create timing/chain history")
		})
	}

	for _, name := range []string{"plugin_unknown_anchor", "chain_percent_unknown_anchor", "budget_full"} {
		t.Run(name, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, name, "responses")
			f.s.cacheTraceWorkerPool().budget = 1
			var result *OpenAIForwardResult
			if name == "plugin_unknown_anchor" {
				// The plugin process is not launched by this contract test. Its
				// real read-only routing predicate still selects the transport.
				f.s.pluginManager = &PluginManager{}
				f.s.pluginManager.route.Store(&pluginRoute{rolloutPercent: 100})
				f.account.Type = AccountTypeOAuth
				f.account.Credentials = map[string]any{"access_token": "ctc-private-oauth-token"}
				f.captured(t)
				result = f.finish(t, f.s.parseSSEUsageWithCacheTrace(f.c, traceForwardSSE))
			} else {
				if name == "chain_percent_unknown_anchor" {
					traceTestSetPolicy(f.s, map[string]any{"chain_percent": 0})
					f.s.cacheTraceWorkerPool().budget = 1
				}
				result = f.forward(t, "responses", false)
			}
			require.NotNil(t, result.CacheTrace)
			require.Nil(t, result.CacheTrace.async)
			manifest[name] = f.settle(t, result)
		})
	}

	for _, name := range []string{"expired", "not_ready"} {
		t.Run(name, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, name, "responses")
			f.captured(t)
			trace := cacheTraceFromGin(f.c)
			require.NotNil(t, trace.pending)
			state := trace.async
			var release func()
			if name == "expired" {
				// A deterministic expired queue item exercises the worker's real
				// expiration branch without sleeping for the queue timeout.
				trace.pending.queuedAt = time.Now().Add(-time.Hour)
			} else {
				entered, unblock := make(chan struct{}), make(chan struct{})
				var once sync.Once
				release = func() { once.Do(func() { close(unblock) }) }
				t.Cleanup(release)
				original := trace.pending.getBody
				trace.pending.getBody = func() (io.ReadCloser, error) {
					close(entered)
					<-unblock
					return original()
				}
				f.s.startCacheTraceRequest(f.c)
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("worker did not enter the controlled body reader")
				}
			}
			f.s.startCacheTraceRequest(f.c)
			if name == "expired" {
				require.Eventually(t, func() bool { return state.reason.Load() == cacheTraceExpired }, 5*time.Second, time.Millisecond)
			}
			usage := f.s.parseSSEUsageWithCacheTrace(f.c, traceForwardSSE)
			require.Equal(t, f.s.parseSSEUsageFromBody(traceForwardSSE), usage)
			result := f.finish(t, usage)
			manifest[name] = f.settle(t, result)
			if release != nil {
				release()
			}
			require.Eventually(t, func() bool { return f.s.cacheTraceWorkerPool().reserved.Load() == 0 }, 5*time.Second, time.Millisecond)
			f.s.settleCacheTrace(context.Background(), result, f.account)
			require.Equal(t, name, result.CacheTrace.data.NAReason, "late worker must not revise the frozen event")
		})
	}

	for _, transport := range []string{"http", "plugin"} {
		for _, fault := range []string{"panic", "error"} {
			name := "observer_failed_" + transport + "_" + fault
			t.Run(name, func(t *testing.T) {
				f := cacheTraceNewContractFixture(t, name, "responses")
				if transport == "plugin" {
					f.s.pluginManager = &PluginManager{}
					f.s.pluginManager.route.Store(&pluginRoute{rolloutPercent: 100})
					f.account.Type = AccountTypeOAuth
					f.account.Credentials = map[string]any{"access_token": "ctc-private-oauth-token"}
				}
				f.captured(t)
				trace := cacheTraceFromGin(f.c)
				calls := 0
				usage, complete := f.s.parseSSEUsageObserving(traceForwardSSE, func([]byte) error {
					calls++
					if calls == 2 {
						if fault == "panic" {
							panic("ctc-private-observer-panic")
						}
						return errors.New("ctc-private-observer-error")
					}
					return nil
				}, trace)
				require.False(t, complete)
				require.Equal(t, 2, calls)
				require.Equal(t, f.s.parseSSEUsageFromBody(traceForwardSSE), usage)
				// The production parser owns failure propagation, including the
				// original trace after callbacks are disabled. Never synthesize
				// the failure marker in this test.
				require.True(t, trace.usageObservationFailed)
				result := f.finish(t, usage)
				require.Nil(t, result.CacheTrace, "keep the pre-existing discarded-trace contract")
				require.NotNil(t, result.cacheTraceFailure)
				manifest[name] = f.settle(t, result)
				require.Nil(t, result.CacheTrace)
				require.Zero(t, f.s.cacheTraceWorkerPool().reserved.Load())
			})
		}
	}

	extraManifest, extraExpected, extraHashes := cacheTraceRunExtendedLoggerContract(t)
	blockHashes = append(blockHashes, extraHashes...)
	appLogger.Sync()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	traces, usages := make(map[string][]map[string]any), make(map[string][]map[string]any)
	traceWire := make(map[string][]byte)
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event))
		kind, _ := event["msg"].(string)
		if kind != "cache_trace" && kind != "openai_cache_usage" {
			continue
		}
		require.Equal(t, "INFO", event["level"])
		require.Equal(t, "slog", event["logger"])
		require.Equal(t, "cache-trace-contract-test", event["service"])
		require.Equal(t, "test", event["env"])
		require.NotEmpty(t, event["time"])
		for _, marker := range []string{"ctc-private-", "trace-private-", "synthetic-token", "CoDeX_DeSkToP/", "X-Codex-Window-Id"} {
			require.NotContains(t, string(line), marker)
		}
		key, ok := event["request_id_hmac"].(string)
		require.True(t, ok)
		if kind == "cache_trace" {
			traces[key] = append(traces[key], event)
			traceWire[key] = line
		} else {
			usages[key] = append(usages[key], event)
		}
	}
	require.Len(t, manifest, 22)
	require.Len(t, noSessionUsage, 4)
	require.Len(t, traces, len(manifest)+len(extraManifest))
	require.Len(t, usages, len(manifest)-len(noSessionUsage)+len(extraManifest))
	minimalKeys := strings.Fields("v platform loss_class na_reason request_id_hmac session_digest account_id group_id P read pol_rev ua_family")
	boolKeys := strings.Fields("pck_out_is_uuid thread_present chain_sampled blk_order_physical_eq_logical include_encrypted_reasoning store_false ingress_ws_bridge rsn_boundary_hit read_short_of_fork")
	for name, key := range manifest {
		require.Len(t, traces[key], 1, name)
		trace := traces[key][0]
		if expected, noSession := noSessionUsage[name]; noSession {
			require.Empty(t, usages[key], "legacy usage intentionally omits empty sessions")
			require.Equal(t, "", trace["session_digest"])
			require.Equal(t, float64(3000), trace["account_id"])
			require.Equal(t, float64(1), trace["group_id"])
			require.Equal(t, float64(expected.InputTokens), trace["P"])
			require.Equal(t, float64(expected.CacheReadInputTokens), trace["read"])
			if name == "admitted_no_session" {
				require.Equal(t, "no_session", trace["gap_src"])
				require.NotContains(t, trace, "gap_send_ms")
			}
		} else {
			require.Len(t, usages[key], 1, name)
			usage := usages[key][0]
			if name == "admitted_legacy_routing" {
				require.Regexp(t, `^[0-9a-f]{16}$`, trace["session_digest"])
				require.Equal(t, "failover_n", trace["route_reason"])
				require.Equal(t, "stall", trace["rebind_cause"])
			}
			for _, field := range strings.Fields("request_id_hmac session_digest account_id group_id") {
				require.Equal(t, usage[field], trace[field], name+":"+field)
			}
			require.Equal(t, usage["input_tokens"], trace["P"], name)
			require.Equal(t, usage["cache_read_tokens"], trace["read"], name)
		}
		require.Equal(t, "codex_desktop", trace["ua_family"])
		payload := make(map[string]any)
		for field, value := range trace {
			switch field {
			case "time", "level", "logger", "caller", "msg", "service", "env", "stacktrace":
			default:
				payload[field] = value
			}
		}
		minimal := strings.HasPrefix(name, "budget_full") || name == "expired" || name == "not_ready" || strings.HasPrefix(name, "observer_failed_")
		if minimal {
			require.Len(t, payload, 12, name)
			for _, field := range minimalKeys {
				require.Contains(t, payload, field, name)
			}
			reason := strings.TrimSuffix(name, "_no_session")
			if strings.HasPrefix(name, "observer_failed_") {
				reason = "observer_failed"
			}
			require.Equal(t, "n/a_sampled_out", trace["loss_class"])
			require.Equal(t, reason, trace["na_reason"])
		} else {
			require.Greater(t, len(payload), 12, name)
			for _, field := range boolKeys {
				_, ok := trace[field].(bool)
				require.True(t, ok, name+":"+field+" must be JSON bool")
			}
			require.Equal(t, float64(7), trace["codex_hdr"])
			require.Equal(t, false, trace["ingress_ws_bridge"])
			if strings.HasSuffix(name, "unknown_anchor") {
				require.Equal(t, "unknown", trace["conv_anchor"])
				require.Equal(t, false, trace["chain_sampled"])
				if strings.HasPrefix(name, "plugin") {
					require.Equal(t, "n/a_plugin", trace["loss_class"])
					require.NotContains(t, trace, "na_reason")
				} else {
					require.Equal(t, "n/a_sampled_out", trace["loss_class"])
					require.Equal(t, "chain_percent", trace["na_reason"])
				}
			} else {
				require.Equal(t, true, trace["chain_sampled"])
				require.Regexp(t, `^[0-9a-f]{8,64}$`, trace["conv_anchor"])
			}
		}
		for _, hash := range blockHashes {
			if hash == 0 {
				continue
			}
			require.NotContains(t, string(traceWire[key]), fmt.Sprintf("%016x", hash), "in-memory xxhash must never enter cache_trace")
			require.NotContains(t, string(traceWire[key]), strconv.FormatUint(hash, 10), "in-memory xxhash must never enter cache_trace")
		}
	}
	cacheTraceAssertExtendedLoggerContract(t, extraManifest, extraExpected, traces, usages, traceWire, blockHashes)
	for name, key := range extraManifest {
		require.NotContains(t, manifest, name)
		manifest[name] = key
	}
	if path := os.Getenv(cacheTraceContractManifestEnv); path != "" {
		encoded, err := json.MarshalIndent(manifest, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	}
}

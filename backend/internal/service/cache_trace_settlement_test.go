package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func t3SettlementCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func t3SettlementEvents(t *testing.T, logs *bytes.Buffer, kind string) []map[string]any {
	t.Helper()
	var matches []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event))
		name, ok := event["msg"].(string)
		require.True(t, ok, "structured event message is a string")
		if name == kind {
			matches = append(matches, event)
		}
	}
	return matches
}

func t3SettlementPayload(t *testing.T, event map[string]any, minimal bool) map[string]any {
	t.Helper()
	payload := make(map[string]any, len(event))
	for key, value := range event {
		switch key {
		case "level", "time", "logger", "caller", "msg", "service", "env", "stacktrace":
		default:
			payload[key] = value
		}
	}
	// Exact required/allowed sets from cache_trace_contract v1.7.8.2. This
	// shape assertion complements the external authoritative schema validator.
	fields := "v platform loss_class na_reason request_id_hmac session_digest account_id group_id P read pol_rev ua_family"
	if !minimal {
		fields = "v request_id_hmac session_digest platform protocol_in protocol_out group_id account_id transport ingress_ws_bridge pol_rev pck_class_in pck_class_out pck_out_is_uuid sess_hdr_in sess_hdr_out sess_out_relation id_source conv_anchor thread_present codex_hdr window_seq_delta ua_family gap_src chain_sampled blk_n bytes_out bytes_out_raw blk_order_physical_eq_logical fork_ref fork_kind fork_precision fork_blk fork_byte fork_blk_hi fork_byte_hi fork_raw_byte p_prev hist_age_s tail_kind reasoning_items_n old_reasoning_n first_old_reasoning_blk first_old_reasoning_byte first_old_reasoning_raw_byte include_encrypted_reasoning store_false reasoning_enc_n reasoning_ctx reasoning_ctx_eff model_gen rsn_boundary_hit read_short_of_fork P read tok_per_byte_milli usage_fields pcd resp_cache_ttl loss_class route_reason rebind_cause"
		if payload["loss_class"] == "n/a_sampled_out" {
			fields += " na_reason"
		}
		if payload["gap_src"] == "settled" {
			fields += " gap_send_ms"
		}
		for _, field := range strings.Fields("pck_out_is_uuid thread_present chain_sampled blk_order_physical_eq_logical include_encrypted_reasoning store_false ingress_ws_bridge rsn_boundary_hit read_short_of_fork") {
			_, ok := payload[field].(bool)
			require.True(t, ok, "%s must be a JSON boolean", field)
		}
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	require.ElementsMatch(t, strings.Fields(fields), keys)
	if minimal {
		require.Len(t, payload, 12)
	}
	require.Equal(t, "m0.o0.u0.a0.r0", payload["pol_rev"])
	require.Equal(t, "openai", payload["platform"])
	require.Equal(t, float64(1), payload["v"])
	require.Regexp(t, `^[0-9a-f]{12,64}$`, payload["request_id_hmac"])
	// Session identifiers retain the existing usage-log wire format.
	require.Regexp(t, `^(?:v2:)?[0-9a-f]{16}$`, payload["session_digest"])
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	t.Logf("T3_SETTLEMENT_SCHEMA_EVENT %s", encoded)
	return payload
}

func t3SettlementAssertUsageLink(t *testing.T, trace, usage map[string]any, result *OpenAIForwardResult, account *Account) {
	t.Helper()
	for _, field := range []string{"request_id_hmac", "session_digest", "account_id", "group_id"} {
		require.NotNil(t, usage[field])
		require.Equal(t, usage[field], trace[field], "same-settlement %s", field)
	}
	require.Equal(t, usage["input_tokens"], trace["P"])
	require.Equal(t, usage["cache_read_tokens"], trace["read"])
	require.Equal(t, float64(result.Usage.InputTokens), trace["P"])
	require.Equal(t, float64(result.Usage.CacheReadInputTokens), trace["read"])
	require.Equal(t, float64(account.ID), trace["account_id"])
	require.Equal(t, float64(result.CacheDiagnostic.GroupID), trace["group_id"])
}

func t3SettlementAssertPrivate(t *testing.T, logs *bytes.Buffer, chains ...cachePrefixChain) {
	t.Helper()
	for _, marker := range []string{"trace-private", "t3-private", "synthetic-token", "synthetic-account", "trace-test-secret", "rid-review", "resp_1", "private-observer"} {
		require.NotContains(t, logs.String(), marker)
	}
	for _, chain := range chains {
		for _, hash := range chain.hashes {
			require.NotContains(t, logs.String(), strconv.FormatUint(hash, 10), "block hashes never leave process")
			require.NotContains(t, logs.String(), fmt.Sprintf("%016x", hash), "block hashes never leave process")
		}
	}
	for _, event := range t3SettlementEvents(t, logs, "cache_trace") {
		for _, key := range []string{"hashes", "block_hash", "block_hashes", "prefix_hashes", "chain"} {
			require.NotContains(t, event, key)
		}
	}
}

// Drain only the fixture worker before manipulating request-local snapshots.
// Waiting on result.Load alone can return before worker-side publish completes.
func t3SettlementWorkerDrained(t *testing.T, s *OpenAIGatewayService) {
	t.Helper()
	pool := s.cacheTraceWorkerPool()
	require.Eventually(t, func() bool { return pool.reserved.Load() == 0 }, 5*time.Second, time.Millisecond)
}

func t3SettlementSuccessfulForward(t *testing.T, protocol, kind string, stream, eof bool) (*OpenAIGatewayService, *gin.Context, *OpenAIForwardResult) {
	t.Helper()
	s, c, result := traceSuccessfulForward(t, protocol, kind, stream, eof)
	t3SettlementWorkerDrained(t, s)
	return s, c, result
}

// Real Forward/Chat -> deferred finishCacheDiagnostic -> billed usage. Only
// the synthetic upstream SSE and optional byte-budget setup vary. Waiting for
// worker readiness is test-only; production settlement never waits.
func t3SettlementForwardReply(t *testing.T, protocol string, stream bool, reply string, budgetRejected bool) (*OpenAIGatewayService, *gin.Context, *OpenAIForwardResult, []byte) {
	t.Helper()
	field, path := "input", "/v1/responses"
	if protocol == "chat" {
		field, path = "messages", "/v1/chat/completions"
	}
	body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","%s":[{"role":"user","content":"t3-private-body"}],"prompt_cache_key":"t3-private-key","stream":%t}`, field, stream))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("session-id", "t3-private-session")
	group := int64(10)
	c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "trace-test-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	traceTestConfig(cfg)
	response := traceForwardOK()
	response.Header.Set("X-Request-Id", "t3-private-upstream-request")
	response.Body = io.NopCloser(strings.NewReader(reply))
	s := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{resp: response}}
	if budgetRejected {
		s.cacheTraceWorkerPool().budget = 1
	}
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": true}}
	s.GenerateSessionHash(c, body)
	var result *OpenAIForwardResult
	var err error
	if protocol == "chat" {
		result, err = s.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
	} else {
		result, err = s.Forward(c.Request.Context(), c, account, body)
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.CacheTrace)
	require.NotNil(t, result.CacheDiagnostic, "real Forward's deferred finishCacheDiagnostic ran")
	require.NotEmpty(t, result.CacheDiagnostic.Session)
	if !budgetRejected {
		awaitCacheTrace(t, s, c, result)
		t3SettlementWorkerDrained(t, s)
	}
	return s, c, result, body
}

func TestCacheTraceT3SettlementProductionForwardLinksAndOnce(t *testing.T) {
	for _, protocol := range []string{"responses", "chat"} {
		for _, kind := range []string{"apikey", "oauth"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", protocol, kind, stream), func(t *testing.T) {
					s, c, result := t3SettlementSuccessfulForward(t, protocol, kind, stream, false)
					require.NotNil(t, result.CacheDiagnostic, "Forward finished its diagnostic snapshot")
					trace := result.CacheTrace
					require.True(t, trace.data.UsagePromptObserved)
					require.True(t, trace.data.UsagePromptPresent)
					require.Equal(t, 2000, result.Usage.InputTokens)
					require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
					require.NotEmpty(t, trace.chain.hashes, "positive worker control")
					account := &Account{ID: 9, Platform: PlatformOpenAI}
					logs := t3SettlementCapture(t)
					s.observeCacheUsage(result, account, time.Now())
					s.settleCacheTrace(c.Request.Context(), result, account)
					first := logs.String()
					s.settleCacheTrace(c.Request.Context(), result, account)
					require.Equal(t, first, logs.String(), "second settlement produces no event")
					traces := t3SettlementEvents(t, logs, "cache_trace")
					usages := t3SettlementEvents(t, logs, "openai_cache_usage")
					require.Len(t, traces, 1)
					require.Len(t, usages, 1)
					payload := t3SettlementPayload(t, traces[0], false)
					require.Equal(t, "cold", payload["loss_class"])
					require.Equal(t, true, payload["chain_sampled"])
					require.NotEqual(t, float64(-1), payload["tok_per_byte_milli"])
					t3SettlementAssertUsageLink(t, traces[0], usages[0], result, account)
					telemetry := s.cacheTelemetry()
					telemetry.mu.Lock()
					owner := telemetry.sessions[result.CacheDiagnostic.Session].traceChains
					telemetry.mu.Unlock()
					require.NotNil(t, owner)
					require.Equal(t, 2000, owner.chains[0].prompt)
					require.NotEmpty(t, owner.chains[0].hashes)
					t3SettlementAssertPrivate(t, logs, trace.chain)
				})
			}
		}
	}
}

func TestCacheTraceT3SettlementUndeliveredEmitsNoRow(t *testing.T) {
	previous := *resilience.Current()
	resilience.Publish(resilience.Defaults())
	t.Cleanup(func() { resilience.Publish(previous) })
	for _, delivered := range []bool{false, true} {
		t.Run(strconv.FormatBool(delivered), func(t *testing.T) {
			s, _, result := t3SettlementSuccessfulForward(t, "responses", "apikey", false, false)
			trace := result.CacheTrace
			async := trace.async
			client, cancel := context.WithCancel(context.Background())
			state := resilience.NewRequestState()
			state.InitDelivery(client, time.Now().Add(-time.Second), func() int64 {
				if delivered {
					return 5
				}
				return 0
			})
			cancel()
			state.ResolveDisconnect()
			ctx := resilience.WithRequestState(context.Background(), state)
			account := &Account{ID: 9, Platform: PlatformOpenAI}
			logs := t3SettlementCapture(t)
			s.observeCacheUsage(result, account, time.Now())
			// The direct settlement gate is under test. The explicitly invoked
			// legacy observation above is a setup control, not an undelivered
			// production RecordUsage path; discard its one usage log here.
			require.Len(t, t3SettlementEvents(t, logs, "openai_cache_usage"), 1)
			logs.Reset()
			s.settleCacheTrace(ctx, result, account)
			s.settleCacheTrace(ctx, result, account)
			if !delivered {
				require.Empty(t, logs.String())
				require.False(t, trace.settled)
				require.Nil(t, async.settled.Load(), "undelivered must not enable late history publication")
				require.Nil(t, s.cacheTelemetry().sessions[result.CacheDiagnostic.Session].traceChains)
				return
			}
			traces := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, traces, 1, "partially delivered remains eligible")
			require.Equal(t, "cold", traces[0]["loss_class"])
			require.NotNil(t, async.settled.Load())
			t3SettlementAssertPrivate(t, logs, trace.chain)
		})
	}
}

func TestCacheTraceT3SettlementUsagePresenceAndHistory(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement, want string
		p, read                      int
		present, publish             bool
	}{
		{"missing-P", `"input_tokens":2000,`, "", "n/a_usage", 0, 1024, false, false},
		{"explicit-zero-P", `"input_tokens":2000`, `"input_tokens":0`, "below_min", 0, 0, true, true},
		{"missing-cache-read", `"input_tokens_details":{"cached_tokens":1024},`, "", "n/a_usage", 2000, 0, true, true},
		{"explicit-zero-cache-read", `"cached_tokens":1024`, `"cached_tokens":0`, "cold", 2000, 0, true, true},
	} {
		for _, protocol := range []string{"responses", "chat"} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				require.Equal(t, 1, strings.Count(traceForwardSSE, tc.old))
				reply := strings.Replace(traceForwardSSE, tc.old, tc.replacement, 1)
				if tc.name == "explicit-zero-P" {
					reply = strings.Replace(reply, `"cached_tokens":1024`, `"cached_tokens":0`, 1)
				}
				s, c, result, _ := t3SettlementForwardReply(t, protocol, false, reply, false)
				require.Equal(t, tc.p, result.Usage.InputTokens)
				require.Equal(t, tc.read, result.Usage.CacheReadInputTokens)
				require.Equal(t, tc.present, result.CacheTrace.data.UsagePromptPresent)
				require.True(t, result.CacheTrace.data.UsagePromptObserved)
				account := &Account{ID: 9, Platform: PlatformOpenAI}
				logs := t3SettlementCapture(t)
				s.observeCacheUsage(result, account, time.Now())
				s.settleCacheTrace(c.Request.Context(), result, account)
				traces := t3SettlementEvents(t, logs, "cache_trace")
				usages := t3SettlementEvents(t, logs, "openai_cache_usage")
				require.Len(t, traces, 1)
				require.Len(t, usages, 1)
				payload := t3SettlementPayload(t, traces[0], false)
				require.Equal(t, tc.want, payload["loss_class"])
				t3SettlementAssertUsageLink(t, traces[0], usages[0], result, account)
				owner := s.cacheTelemetry().sessions[result.CacheDiagnostic.Session].traceChains
				require.NotNil(t, owner)
				if tc.publish {
					require.NotEmpty(t, owner.chains[0].hashes)
					require.Equal(t, tc.p, owner.chains[0].prompt, "explicit P=0 is a real baseline")
				} else {
					require.Empty(t, owner.chains[0].hashes, "missing P cannot train an invented zero baseline")
					require.Equal(t, float64(-1), payload["tok_per_byte_milli"])
				}
				t3SettlementAssertPrivate(t, logs, result.CacheTrace.chain)
			})
		}
	}
}

// Uses real billed P/read and Forward metadata. Only the worker comparison
// snapshot is synthetic, so these assert settlement/classifier wiring rather
// than claim to recover a real upstream fork from the tiny fixture request.
func TestCacheTraceT3SettlementRangeEndpointWiring(t *testing.T) {
	for _, tc := range []struct {
		name, kind, precision, want string
		previous, lo, hi            int
	}{
		{"diverge-same", "diverge", "range", "prefix_changed", 4000, 200, 1000},
		{"diverge-ambiguous", "diverge", "range", "n/a_truncated", 1500, 200, 1500},
		{"tail-hi-no-fork", "tail_unproven", "range", "n/a_truncated", 4000, 1500, 2000},
		{"both-full", "diverge", "range", "full", 1280, 200, 1500},
		{"cur-prefix-no-fork", "cur_prefix", "exact", "under_read", 4000, 2000, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, result := t3SettlementSuccessfulForward(t, "responses", "apikey", false, false)
			trace := result.CacheTrace
			computed := *trace.async.result.Load()
			computed.prefix.BytesOut = result.Usage.InputTokens // density=1, synthetic logical position basis
			computed.prefix.ForkRef, computed.prefix.ForkKind, computed.prefix.ForkPrecision = "prev1", tc.kind, tc.precision
			computed.prefix.PreviousPrompt, computed.prefix.HistoryAgeSeconds = tc.previous, 7
			computed.prefix.ForkByte, computed.prefix.ForkByteHi = tc.lo, tc.hi
			computed.prefix.ForkBlock, computed.prefix.ForkBlockHi = 1, 3
			if tc.precision == "exact" {
				computed.prefix.ForkBlockHi = -1
			}
			computed.prefix.OldReasoningN, computed.prefix.FirstOldReasoningByte = 0, -1
			computed.prefix.historyAt = time.Now().Add(-7 * time.Second)
			injected := &cacheTraceAsync{}
			injected.result.Store(&computed)
			trace.async = injected
			account := &Account{ID: 9, Platform: PlatformOpenAI}
			logs := t3SettlementCapture(t)
			s.observeCacheUsage(result, account, time.Now())
			s.settleCacheTrace(c.Request.Context(), result, account)
			traces := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, traces, 1)
			payload := t3SettlementPayload(t, traces[0], false)
			require.Equal(t, tc.want, payload["loss_class"])
			require.Equal(t, float64(tc.previous), payload["p_prev"])
			historyAge, ok := payload["hist_age_s"].(float64)
			require.True(t, ok)
			require.GreaterOrEqual(t, historyAge, float64(7))
			require.Equal(t, tc.kind, payload["fork_kind"])
			require.Equal(t, float64(tc.lo), payload["fork_byte"])
			require.Equal(t, float64(tc.hi), payload["fork_byte_hi"])
			t3SettlementAssertPrivate(t, logs, computed.chain)
		})
	}
}

func TestCacheTraceT3SettlementMinimalShapesAndUsageLinks(t *testing.T) {
	for _, tc := range []struct{ reason, failure string }{
		{"budget_full", ""}, {"expired", ""}, {"not_ready", ""},
		{"observer_failed", "panic"}, {"observer_failed", "error"},
	} {
		t.Run(tc.reason+"/"+tc.failure, func(t *testing.T) {
			reason := tc.reason
			s, c, result, body := t3SettlementForwardReply(t, "responses", false, traceForwardSSE, reason == "budget_full")
			var chain cachePrefixChain
			if reason != "budget_full" {
				chain = result.CacheTrace.chain
			}
			switch reason {
			case "budget_full":
				require.True(t, cacheTraceFromGin(c).skipUsage, "real admission budget rejection")
				require.False(t, result.CacheTrace.data.UsagePromptObserved)
				require.Zero(t, result.CacheTrace.data.UsageFields)
			case "expired", "not_ready":
				// Deterministic settlement boundary: the real forwarding worker
				// completed already; substitute an isolated atomic handle in the
				// exact state an expired/not-ready worker would expose.
				state := &cacheTraceAsync{}
				if reason == "expired" {
					state.reason.Store(cacheTraceExpired)
				}
				result.CacheTrace.async = state
				result.CacheTrace.data.Prefix = cachePrefixUnavailable(len(body))
			case "observer_failed":
				// Exercise the real request-level recovery/error completion
				// path using its test callback seam, with the original trace
				// supplied. No manual assignment of the failed-observer flag.
				live := cacheTraceFromGin(c)
				calls := 0
				usage, complete := s.parseSSEUsageObserving(traceForwardSSE, func([]byte) error {
					calls++
					if tc.failure == "panic" {
						panic("private-observer-panic-marker")
					}
					return errors.New("private-observer-error-marker")
				}, live)
				require.False(t, complete)
				require.Equal(t, 1, calls, "observation stops once; billing parsing still completes")
				require.Equal(t, s.parseSSEUsageFromBody(traceForwardSSE), usage)
				require.True(t, live.usageObservationFailed, "parser completion marks the original trace after panic/error")
				// Refinish the same real Forward billing result after fault
				// injection, exactly as a forwarding caller does after parsing.
				result.Usage = *usage
				result.CacheTrace = nil
				s.finishCacheDiagnostic(c, body, result)
				require.Nil(t, result.CacheTrace, "existing failed-observer assertion remains")
				require.NotNil(t, result.cacheTraceFailure)
			}
			account := &Account{ID: 9, Platform: PlatformOpenAI}
			logs := t3SettlementCapture(t)
			s.observeCacheUsage(result, account, time.Now())
			s.settleCacheTrace(c.Request.Context(), result, account)
			first := logs.String()
			s.settleCacheTrace(c.Request.Context(), result, account)
			require.Equal(t, first, logs.String())
			traces := t3SettlementEvents(t, logs, "cache_trace")
			usages := t3SettlementEvents(t, logs, "openai_cache_usage")
			require.Len(t, traces, 1)
			require.Len(t, usages, 1)
			payload := t3SettlementPayload(t, traces[0], true)
			require.Equal(t, "n/a_sampled_out", payload["loss_class"])
			require.Equal(t, reason, payload["na_reason"])
			if reason == "budget_full" {
				require.True(t, result.CacheTrace.timing.sentAt.IsZero(), "rejection never acquired a send token")
				require.Nil(t, s.cacheTelemetry().sessions[result.CacheDiagnostic.Session].traceChains, "budget rejection cannot create timing/history state")
			}
			t3SettlementAssertUsageLink(t, traces[0], usages[0], result, account)
			t3SettlementAssertPrivate(t, logs, chain)
		})
	}
}

func TestCacheTraceT3SettlementLateWorkerCannotRewriteEvent(t *testing.T) {
	s, c, result := t3SettlementSuccessfulForward(t, "responses", "apikey", false, false)
	trace := result.CacheTrace
	completed := trace.async.result.Load()
	require.NotNil(t, completed)
	// A controlled atomic schedule, not a blocked production worker: Forward
	// and its real worker finished, then a fresh handle models not_ready at
	// settlement and publication of the immutable computed result afterwards.
	late := &cacheTraceAsync{}
	trace.async = late
	trace.data.Prefix = cachePrefixUnavailable(completed.prefix.BytesOutRaw)
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	logs := t3SettlementCapture(t)
	s.observeCacheUsage(result, account, time.Now())
	s.settleCacheTrace(c.Request.Context(), result, account)
	traces := t3SettlementEvents(t, logs, "cache_trace")
	require.Len(t, traces, 1)
	payload := t3SettlementPayload(t, traces[0], true)
	require.Equal(t, "not_ready", payload["na_reason"])
	first, frozen := logs.String(), trace.data
	require.Nil(t, trace.async, "settlement severs the event's async result handle")
	late.result.Store(completed)
	late.publish(s, result.CacheDiagnostic.Session)
	s.settleCacheTrace(c.Request.Context(), result, account)
	trace.emitCacheTrace()
	require.Equal(t, first, logs.String(), "late worker and repeated emit must not append or rewrite")
	require.Equal(t, frozen, trace.data)
	settled := late.settled.Load()
	require.NotNil(t, settled, "admitted not_ready preserves its delivered settlement")
	require.NotNil(t, settled.owner)
	require.Equal(t, result.Usage.InputTokens, settled.prompt)
	owner := s.cacheTelemetry().sessions[result.CacheDiagnostic.Session].traceChains
	require.Same(t, settled.owner, owner)
	require.Equal(t, completed.chain.hashes, owner.chains[0].hashes, "known P allows late history publication")
	require.Equal(t, result.Usage.InputTokens, owner.chains[0].prompt)
	closed := false
	for _, entry := range owner.recent {
		if entry.sequence == trace.timing.sequence {
			closed = !entry.settledAt.IsZero()
		}
	}
	require.True(t, closed, "admitted not_ready must close its timing record")
	t3SettlementAssertPrivate(t, logs, completed.chain)
}

func TestCacheTraceT3SettlementHistoryEpochSurvivesEviction(t *testing.T) {
	for _, mode := range []string{"same", "deleted", "recreated"} {
		t.Run(mode, func(t *testing.T) {
			s, c, result := t3SettlementSuccessfulForward(t, "responses", "apikey", false, false)
			state := result.CacheTrace.async
			computed := state.result.Load()
			account := &Account{ID: 9, Platform: PlatformOpenAI}
			logs := t3SettlementCapture(t)
			s.observeCacheUsage(result, account, time.Now())
			s.settleCacheTrace(c.Request.Context(), result, account)
			settled := state.settled.Load()
			require.NotNil(t, settled, "real settlement produced its publication owner")
			require.NotNil(t, settled.owner)
			first := logs.String()
			// Model another outstanding publication with the owner obtained
			// from real settlement. This synthetic atomic handle deliberately
			// has not published yet; it is not a second settlement event.
			late := &cacheTraceAsync{}
			late.settled.Store(settled)
			lateComputed := *computed
			lateComputed.chain.hashes = []uint64{0x53a9e1b7d4c2608f}
			late.result.Store(&lateComputed)
			telemetry := s.cacheTelemetry()
			session := result.CacheDiagnostic.Session
			var replacement *cacheTraceSessionState
			var replacementBefore cacheTraceSessionState
			telemetry.mu.Lock()
			switch mode {
			case "deleted":
				delete(telemetry.sessions, session)
			case "recreated":
				replacement = new(cacheTraceSessionState)
				replacement.chains[0] = cachePrefixChain{hashes: []uint64{0x624ea03d91f75bc8}, blocks: 3, prompt: 7777}
				replacementBefore = *replacement
				telemetry.sessions[session] = openAICacheHistory{account: 77, prefix: "replacement-prefix", at: time.Now(), traceChains: replacement}
			}
			telemetry.mu.Unlock()
			late.publish(s, session)
			telemetry.mu.Lock()
			history, exists := telemetry.sessions[session]
			telemetry.mu.Unlock()
			switch mode {
			case "same":
				require.True(t, exists)
				require.Same(t, settled.owner, history.traceChains)
				require.Equal(t, lateComputed.chain.hashes, history.traceChains.chains[0].hashes, "same-owner publication positive control")
			case "deleted":
				require.False(t, exists, "late publication must not resurrect an evicted session")
			case "recreated":
				require.True(t, exists)
				require.Same(t, replacement, history.traceChains)
				require.Equal(t, int64(77), history.account)
				require.Equal(t, "replacement-prefix", history.prefix)
				require.Equal(t, replacementBefore, *replacement, "old epoch cannot mutate replacement chains/timing/window")
			}
			s.settleCacheTrace(c.Request.Context(), result, account)
			require.Equal(t, first, logs.String())
			t3SettlementAssertPrivate(t, logs, computed.chain, lateComputed.chain)
		})
	}
}

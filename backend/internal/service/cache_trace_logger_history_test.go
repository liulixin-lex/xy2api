package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type cacheTraceLoggerExpectation struct {
	loss, kind, precision, gap string
	prompt, read, previous     int
	metadata, minimal          bool
}

// All history and timing come from successive real Forward -> RecordUsage
// calls on one service. Do not seed telemetry, rewrite timestamps, or construct
// event/chain snapshots: the strict CLI must see the product's actual output.
func cacheTraceRunExtendedLoggerContract(t *testing.T) (map[string]string, map[string]cacheTraceLoggerExpectation, []uint64) {
	t.Helper()
	manifest := make(map[string]string)
	expected := make(map[string]cacheTraceLoggerExpectation)
	var hashes []uint64
	t.Run("history_and_settled_gap", func(t *testing.T) {
		shared := cacheTraceNewContractFixture(t, "history", "responses")
		input := []map[string]string{
			{"role": "user", "content": "ctc-private-history-anchor"},
			{"role": "assistant", "content": strings.Repeat("ctc-private-history-prefix ", 256)},
			{"role": "user", "content": "ctc-private-history-question"},
		}
		for i, tc := range []struct {
			name   string
			stream bool
			want   cacheTraceLoggerExpectation
		}{
			{"history_cold", false, cacheTraceLoggerExpectation{loss: "cold", kind: "none", precision: "none", gap: "no_prev", prompt: 4000, read: 1024, previous: -1}},
			{"history_full", true, cacheTraceLoggerExpectation{loss: "full", kind: "append", precision: "none", gap: "settled", prompt: 5000, read: 4000, previous: 4000}},
			{"history_under_read", false, cacheTraceLoggerExpectation{loss: "under_read", kind: "append", precision: "none", gap: "settled", prompt: 6000, read: 1024, previous: 5000}},
			{"history_prefix_changed", true, cacheTraceLoggerExpectation{loss: "prefix_changed", kind: "diverge", precision: "exact", gap: "settled", prompt: 6000, read: 1024, previous: 6000}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := cacheTraceNewContractFixture(t, "history", "responses")
				f.s, f.rows, f.billing = shared.s, shared.rows, shared.billing
				*f.rows = openAIRecordUsageLogRepoStub{inserted: true}
				*f.billing = openAIRecordUsageBillingRepoStub{}
				f.id = "ctc-private-request-" + tc.name
				if i == 1 || i == 2 {
					input = append(input, map[string]string{"role": "assistant", "content": "ctc-private-history-answer"},
						map[string]string{"role": "user", "content": fmt.Sprintf("ctc-private-history-followup-%d", i)})
				}
				if i == 3 {
					// Preserve the first-user anchor; change the early assistant
					// block so this is a same-conversation prefix divergence.
					input[1]["content"] = strings.Repeat("ctc-private-history-changed ", 256)
				}
				var body map[string]any
				require.NoError(t, json.Unmarshal(f.body, &body))
				body["input"] = input
				var err error
				f.body, err = json.Marshal(body)
				require.NoError(t, err)
				f.c.Request.Body = io.NopCloser(bytes.NewReader(f.body))
				f.c.Request.Header.Set("X-Codex-Window-Id", fmt.Sprintf("ctc-private-history-window:%d", 23+i))
				result := f.forwardReply(t, "responses", tc.stream, cacheTraceLoggerUsageReply(tc.want.prompt, tc.want.read))
				upstream, ok := f.s.httpUpstream.(*httpUpstreamRecorder)
				require.True(t, ok)
				require.Len(t, upstream.requests, 1)
				require.NotNil(t, result.CacheTrace)
				awaitCacheTrace(t, f.s, f.c, result)
				t3SettlementWorkerDrained(t, f.s)
				require.True(t, result.CacheTrace.data.Prefix.Sampled)
				hashes = append(hashes, result.CacheTrace.chain.hashes...)
				hashes = append(hashes, result.CacheTrace.chain.final)
				manifest[tc.name] = f.settle(t, result)
				expected[tc.name] = tc.want
				require.Equal(t, tc.want.loss, result.CacheTrace.data.LossClass)
				require.Equal(t, tc.want.previous, result.CacheTrace.data.Prefix.PreviousPrompt)
				require.Equal(t, tc.want.gap, result.CacheTrace.data.GapSource)
			})
		}
	})

	t.Run("metadata_only", func(t *testing.T) {
		f := cacheTraceNewContractFixture(t, "metadata_only", "responses")
		// The metadata frame has no usage slot; the terminal frame has no
		// usage either. P/read remain billing's zeroes, never test-filled.
		metadata := "event: response.metadata\ndata: " + `{"type":"response.metadata","response":{"reasoning":{"context":"all_turns"},"prompt_cache_diagnostics":"tools_changed","prompt_cache_options":{"ttl":"12h"}}}` + "\n\n"
		usage := `,"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":1024},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2001}`
		require.Equal(t, 1, strings.Count(traceForwardSSE, usage))
		result := f.forwardReply(t, "responses", true, metadata+strings.Replace(traceForwardSSE, usage, "", 1))
		require.NotNil(t, result.CacheTrace)
		awaitCacheTrace(t, f.s, f.c, result)
		t3SettlementWorkerDrained(t, f.s)
		require.Zero(t, result.Usage.InputTokens)
		require.Zero(t, result.Usage.CacheReadInputTokens)
		manifest["metadata_only"] = f.settle(t, result)
		expected["metadata_only"] = cacheTraceLoggerExpectation{loss: "n/a_usage", kind: "none", precision: "none", gap: "no_prev", previous: -1, metadata: true}
		hashes = append(hashes, result.CacheTrace.chain.hashes...)
		hashes = append(hashes, result.CacheTrace.chain.final)
	})

	t.Run("undelivered", func(t *testing.T) {
		settings := *resilience.Current()
		resilience.Publish(resilience.Defaults())
		t.Cleanup(func() { resilience.Publish(settings) })
		resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
		t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
		f := cacheTraceNewContractFixture(t, "undelivered", "responses")
		client, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		state := resilience.NewRequestState()
		state.InitDelivery(client, time.Now(), func() int64 { return 0 })
		state.SetAPIKey(f.api.ID)
		state.SetStage(resilience.StagePreOutput)
		ctx := resilience.WithRequestState(context.Background(), state)
		f.c.Request = f.c.Request.WithContext(ctx)
		result := f.forward(t, "responses", false)
		upstream, ok := f.s.httpUpstream.(*httpUpstreamRecorder)
		require.True(t, ok)
		require.Len(t, upstream.requests, 1, "an actual upstream attempt is required")
		require.NotNil(t, result.CacheTrace)
		awaitCacheTrace(t, f.s, f.c, result)
		t3SettlementWorkerDrained(t, f.s)
		cancel()
		disconnected, delivered, abuse := state.ResolveDisconnect()
		require.True(t, disconnected)
		require.False(t, delivered)
		require.True(t, abuse)
		manifest["undelivered"] = f.settleContext(t, ctx, result)
		expected["undelivered"] = cacheTraceLoggerExpectation{loss: "n/a_sampled_out", prompt: 2000, read: 1024, minimal: true}
		require.NotNil(t, f.rows.lastLog.BillingMode)
		require.Equal(t, UsageBillingModeUndeliveredAbuse, *f.rows.lastLog.BillingMode)
		_, _, billed := state.UndeliveredUsage()
		require.True(t, billed)
	})
	require.Len(t, manifest, 6)
	return manifest, expected, hashes
}

func cacheTraceLoggerUsageReply(prompt, read int) string {
	reply := strings.Replace(traceForwardSSE, `"input_tokens":2000`, fmt.Sprintf(`"input_tokens":%d`, prompt), 1)
	reply = strings.Replace(reply, `"cached_tokens":1024`, fmt.Sprintf(`"cached_tokens":%d`, read), 1)
	return strings.Replace(reply, `"total_tokens":2001`, fmt.Sprintf(`"total_tokens":%d`, prompt+1), 1)
}

func cacheTraceAssertExtendedLoggerContract(t *testing.T, manifest map[string]string, expected map[string]cacheTraceLoggerExpectation,
	traces, usages map[string][]map[string]any, wire map[string][]byte, hashes []uint64) {
	t.Helper()
	minimalKeys := strings.Fields("v platform loss_class na_reason request_id_hmac session_digest account_id group_id P read pol_rev ua_family")
	for name, key := range manifest {
		t.Run("actual_log_"+name, func(t *testing.T) {
			require.Len(t, traces[key], 1)
			require.Len(t, usages[key], 1)
			trace, usage, want := traces[key][0], usages[key][0], expected[name]
			for _, field := range strings.Fields("request_id_hmac session_digest account_id group_id") {
				require.Equal(t, usage[field], trace[field], field)
			}
			require.Equal(t, usage["input_tokens"], trace["P"])
			require.Equal(t, usage["cache_read_tokens"], trace["read"])
			require.Equal(t, float64(want.prompt), trace["P"])
			require.Equal(t, float64(want.read), trace["read"])
			require.Equal(t, want.loss, trace["loss_class"])
			require.Equal(t, "codex_desktop", trace["ua_family"])
			payload := make(map[string]any)
			for field, value := range trace {
				switch field {
				case "time", "level", "logger", "caller", "msg", "service", "env", "stacktrace":
				default:
					payload[field] = value
				}
			}
			if want.minimal {
				require.Len(t, payload, 12)
				for _, field := range minimalKeys {
					require.Contains(t, payload, field)
				}
				require.Equal(t, "undelivered", trace["na_reason"])
			} else {
				require.Greater(t, len(payload), 12)
				for _, field := range strings.Fields("pck_out_is_uuid thread_present chain_sampled blk_order_physical_eq_logical include_encrypted_reasoning store_false ingress_ws_bridge rsn_boundary_hit read_short_of_fork") {
					_, ok := trace[field].(bool)
					require.True(t, ok, field+" must be JSON bool")
				}
				require.Equal(t, true, trace["chain_sampled"])
				require.Regexp(t, `^[0-9a-f]{8,64}$`, trace["conv_anchor"])
				require.Equal(t, want.kind, trace["fork_kind"])
				require.Equal(t, want.precision, trace["fork_precision"])
				require.Equal(t, float64(want.previous), trace["p_prev"])
				require.Equal(t, want.gap, trace["gap_src"])
				if want.previous >= 0 {
					age, ok := trace["hist_age_s"].(float64)
					require.True(t, ok)
					require.GreaterOrEqual(t, age, float64(0))
					require.Equal(t, "prev1", trace["fork_ref"])
					require.Equal(t, float64(1), trace["window_seq_delta"])
				}
				if want.gap == "settled" {
					gap, ok := trace["gap_send_ms"].(float64)
					require.True(t, ok)
					require.GreaterOrEqual(t, gap, float64(0))
				} else {
					require.NotContains(t, trace, "gap_send_ms")
				}
				if want.precision == "exact" {
					for _, field := range strings.Fields("fork_blk fork_byte fork_raw_byte") {
						position, ok := trace[field].(float64)
						require.True(t, ok, field)
						require.GreaterOrEqual(t, position, float64(0), field)
					}
				}
				if want.metadata {
					require.Equal(t, "tools_changed", trace["pcd"])
					require.Equal(t, "12h", trace["resp_cache_ttl"])
					require.Equal(t, "all_turns", trace["reasoning_ctx_eff"])
					require.Equal(t, float64(32), trace["usage_fields"])
				}
			}
			for _, hash := range hashes {
				if hash != 0 {
					require.NotContains(t, string(wire[key]), fmt.Sprintf("%016x", hash))
					require.NotContains(t, string(wire[key]), strconv.FormatUint(hash, 10))
				}
			}
		})
	}
}

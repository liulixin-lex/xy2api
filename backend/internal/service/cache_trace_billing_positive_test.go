//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cacheTraceBillingPositiveLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *cacheTraceBillingPositiveLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}
func (w *cacheTraceBillingPositiveLog) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

func cacheTraceBillingPositiveEvents(t *testing.T, raw []byte, kind string) []map[string]any {
	t.Helper()
	events := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event))
		if event["msg"] == kind {
			events = append(events, event)
		}
	}
	return events
}

func cacheTraceBillingPositiveMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	require.True(t, ok, "billing snapshot must be a JSON object")
	return result
}

func cacheTraceBillingPositiveNumber(t *testing.T, value any) float64 {
	t.Helper()
	result, ok := value.(float64)
	require.True(t, ok, "billing token count must be a JSON number")
	require.Equal(t, float64(int(result)), result)
	return result
}

func cacheTraceBillingPositiveString(t *testing.T, value any) string {
	t.Helper()
	result, ok := value.(string)
	require.True(t, ok, "billing read source must be a JSON string")
	return result
}

type cacheTraceBillingPositiveCase struct {
	Name            string `json:"name"`
	HasResult       bool   `json:"has_result"`
	TraceRows       int    `json:"trace_rows"`
	UsageRows       int    `json:"usage_rows"`
	Prompt          int    `json:"P"`
	Read            int    `json:"read"`
	Shape           string `json:"shape"`
	Reason          string `json:"reason,omitempty"`
	UsageFields     int    `json:"usage_fields,omitempty"`
	ReadBitsChecked bool   `json:"read_bits_checked"`
}

type cacheTraceBillingPositiveReport struct {
	TestFailed              bool                            `json:"test_failed"`
	Cases                   int                             `json:"cases"`
	Settled                 int                             `json:"settled"`
	NoResult                int                             `json:"no_result"`
	TraceRows               int                             `json:"trace_rows"`
	UsageRows               int                             `json:"usage_rows"`
	FullRows                int                             `json:"full_rows"`
	FullReadBitRows         int                             `json:"full_read_bit_rows"`
	MinimalReasons          map[string]int                  `json:"minimal_reasons"`
	NormalValidShapes       map[string]string               `json:"normal_valid_shapes"`
	NormalZeroUsageControls int                             `json:"normal_zero_usage_controls"`
	Rows                    []cacheTraceBillingPositiveCase `json:"rows"`
}

// Positive companion for the unchanged baseline-compatible 110-case corpus.
// It invokes cacheBillingRun(enabled=true), whose own production Forward and
// RecordUsage produce these events. No direct settle/emit helper is called and
// no observation fields or config are changed after that fixture is created.
func TestCacheTraceBillingCorpusEnabledPositive(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	defer gin.SetMode(previousMode)
	rawGolden, err := os.ReadFile("testdata/cache_trace_billing_golden.json")
	require.NoError(t, err)
	var golden map[string]map[string]any
	require.NoError(t, json.Unmarshal(rawGolden, &golden))
	require.Len(t, golden, 110)
	report := cacheTraceBillingPositiveReport{MinimalReasons: map[string]int{}, NormalValidShapes: map[string]string{}}
	t.Cleanup(func() {
		report.TestFailed = t.Failed()
		encoded, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		if path := os.Getenv("CACHE_TRACE_BILLING_POSITIVE_REPORT"); path != "" {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			require.NoError(t, err)
			_, err = file.Write(encoded)
			require.NoError(t, err)
			require.NoError(t, file.Close())
		}
		t.Logf("CACHE_TRACE_BILLING_POSITIVE cases=%d settled=%d no_result=%d full=%d read_bits=%d minimal=%v normal_shapes=%d", report.Cases, report.Settled, report.NoResult, report.FullRows, report.FullReadBitRows, report.MinimalReasons, len(report.NormalValidShapes))
	})
	for _, protocol := range []string{"responses", "chat", "rawcc"} {
		for _, kind := range []string{"apikey", "oauth"} {
			if protocol == "rawcc" && kind == "oauth" {
				continue
			}
			for _, stream := range []bool{false, true} {
				for _, tc := range cacheBillingCorpus(protocol == "rawcc") {
					name := fmt.Sprintf("%s/%s/stream=%t/%s", protocol, kind, stream, tc.name)
					t.Run(name, func(t *testing.T) {
						var logs cacheTraceBillingPositiveLog
						old := slog.Default()
						slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
						defer slog.SetDefault(old)
						actual := cacheBillingRun(t, tc, protocol, kind, stream, true)
						require.Equal(t, golden[name]["settlement_calls"], actual["settlement_calls"], "existing corpus result/settlement presence remains the reference")
						events := logs.snapshot()
						traces := cacheTraceBillingPositiveEvents(t, events, "cache_trace")
						usages := cacheTraceBillingPositiveEvents(t, events, "openai_cache_usage")
						row := cacheTraceBillingPositiveCase{Name: name, TraceRows: len(traces), UsageRows: len(usages), Shape: "no_result"}
						report.Cases++
						report.TraceRows += len(traces)
						report.UsageRows += len(usages)
						if actual["settlement_calls"] == float64(0) {
							require.Nil(t, actual["usage"])
							require.Nil(t, actual["forward_result"])
							require.Empty(t, traces, "nil Forward result cannot emit an observation settlement")
							require.Empty(t, usages, "nil Forward result cannot emit a usage settlement")
							report.NoResult++
							report.Rows = append(report.Rows, row)
							return
						}
						require.Equal(t, float64(1), actual["settlement_calls"])
						require.NotNil(t, actual["forward_result"])
						require.NotNil(t, actual["usage"])
						require.Len(t, traces, 1, "enabled production RecordUsage must emit exactly one cache_trace; a disabled observer fails this assertion")
						require.Len(t, usages, 1, "same production RecordUsage must emit one linkable usage event")
						trace, usageEvent := traces[0], usages[0]
						usage := cacheTraceBillingPositiveMap(t, actual["usage"])
						row.HasResult = true
						row.Prompt = int(cacheTraceBillingPositiveNumber(t, usage["InputTokens"]))
						row.Read = int(cacheTraceBillingPositiveNumber(t, usage["CacheReadInputTokens"]))
						report.Settled++
						for _, field := range []string{"request_id_hmac", "session_digest", "account_id", "group_id"} {
							require.NotNil(t, trace[field])
							require.Equal(t, usageEvent[field], trace[field], "same-settlement link %s", field)
						}
						require.Regexp(t, `^[0-9a-f]{12,64}$`, trace["request_id_hmac"])
						require.Regexp(t, `^(?:v2:)?[0-9a-f]{16}$`, trace["session_digest"])
						require.Equal(t, usage["InputTokens"], trace["P"])
						require.Equal(t, usage["CacheReadInputTokens"], trace["read"])
						require.Equal(t, usageEvent["input_tokens"], trace["P"])
						require.Equal(t, usageEvent["cache_read_tokens"], trace["read"])
						require.Equal(t, usage["CacheReadSource"], usageEvent["read_source"])
						stored := cacheTraceBillingPositiveMap(t, actual["usage_log"])
						require.Equal(t, stored["AccountID"], trace["account_id"])
						require.Equal(t, stored["GroupID"], trace["group_id"])
						require.Equal(t, stored["CacheReadTokens"], trace["read"])
						if fields, ok := trace["usage_fields"]; ok {
							row.Shape = "full"
							report.FullRows++
							bits, ok := fields.(float64)
							require.True(t, ok)
							require.Equal(t, float64(int(bits)), bits)
							row.UsageFields = int(bits)
							source := cacheTraceBillingPositiveString(t, usage["CacheReadSource"])
							switch source {
							case "input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens":
								require.NotZero(t, row.UsageFields&1)
							case "cache_read_input_tokens":
								require.NotZero(t, row.UsageFields&4)
							case "":
								require.Zero(t, row.UsageFields&15)
								require.Equal(t, "n/a_usage", trace["loss_class"])
							default:
								t.Fatalf("unexpected corpus read-source enum %q", source)
							}
							if source != "" {
								row.ReadBitsChecked = true
								report.FullReadBitRows++
								require.NotEqual(t, "n/a_usage", trace["loss_class"])
							}
						} else {
							row.Shape = "minimal"
							require.Equal(t, "n/a_sampled_out", trace["loss_class"])
							reason, ok := trace["na_reason"].(string)
							require.True(t, ok)
							require.Contains(t, []string{"budget_full", "not_ready"}, reason, "fault-free corpus allows only declared budget/readiness minimal events")
							row.Reason = reason
							report.MinimalReasons[reason]++
							for _, field := range []string{"route_reason", "rebind_cause", "fork_byte", "bytes_out", "usage_fields"} {
								require.NotContains(t, trace, field, "minimal schema cannot smuggle unavailable observations")
							}
						}
						for _, event := range []map[string]any{trace, usageEvent} {
							encoded, err := json.Marshal(event)
							require.NoError(t, err)
							for _, marker := range []string{"private-input-marker", "private-output-marker", "private-session-marker", "synthetic-corpus-secret", "synthetic-token", "synthetic-account", "rid-corpus", "resp-corpus", "cc-corpus"} {
								require.NotContains(t, string(encoded), marker)
							}
						}
						if tc.name == "normal" {
							shape := fmt.Sprintf("%s/%s/stream=%t", protocol, kind, stream)
							if protocol == "rawcc" && !stream {
								require.Zero(t, row.Prompt)
								require.Zero(t, row.Read)
								report.NormalZeroUsageControls++
							} else {
								require.Equal(t, 4096, row.Prompt)
								require.Equal(t, 1024, row.Read)
								report.NormalValidShapes[shape] = row.Shape + ":" + row.Reason
							}
						}
						report.Rows = append(report.Rows, row)
					})
				}
			}
		}
	}
	require.Equal(t, 110, report.Cases)
	require.Len(t, report.Rows, 110)
	require.Equal(t, 85, report.Settled)
	require.Equal(t, 25, report.NoResult)
	require.Equal(t, report.Settled, report.TraceRows)
	require.Equal(t, report.Settled, report.UsageRows)
	require.Len(t, report.NormalValidShapes, 9, "all normal production protocol/account/stream forms need positive events")
	require.Equal(t, 1, report.NormalZeroUsageControls, "rawcc buffered receiving SSE is the retained preexisting zero-usage control")
	require.Equal(t, report.Settled, report.FullRows+report.MinimalReasons["budget_full"]+report.MinimalReasons["not_ready"])

}

// Read-bit readiness is a separate deterministic gate. The original 110-case
// fixture retains its immediate settlement timing, including legitimate minimal
// not_ready rows. Here only the test waits for the real worker before RecordUsage.
func TestCacheTraceBillingReadBitsReadyNormalShapes(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	defer gin.SetMode(previousMode)
	rows := []cacheTraceBillingPositiveCase{}
	t.Cleanup(func() {
		report := map[string]any{"test_failed": t.Failed(), "cases": len(rows), "worker_waited": true, "rows": rows}
		encoded, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		if path := os.Getenv("CACHE_TRACE_BILLING_READY_REPORT"); path != "" {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			require.NoError(t, err)
			_, err = file.Write(encoded)
			require.NoError(t, err)
			require.NoError(t, file.Close())
		}
		t.Logf("CACHE_TRACE_BILLING_READY normal_shapes=%d", len(rows))
	})
	for _, protocol := range []string{"responses", "chat", "rawcc"} {
		for _, kind := range []string{"apikey", "oauth"} {
			for _, stream := range []bool{false, true} {
				if protocol == "rawcc" && (kind == "oauth" || !stream) {
					continue
				}
				name := fmt.Sprintf("%s/%s/stream=%t", protocol, kind, stream)
				t.Run(name, func(t *testing.T) {
					fixtureProtocol := protocol
					if protocol == "rawcc" {
						fixtureProtocol = "chat"
					}
					f := cacheTraceNewContractFixture(t, name, fixtureProtocol)
					if kind == "oauth" {
						f.account.Type = AccountTypeOAuth
						f.account.Credentials = map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}
					}
					f.account.Extra["openai_responses_supported"] = protocol != "rawcc"
					if stream {
						f.body = bytes.Replace(f.body, []byte(`"stream":false`), []byte(`"stream":true`), 1)
					}
					normal := cacheBillingCorpus(protocol == "rawcc")[0]
					require.Equal(t, "normal", normal.name)
					response := traceForwardOK()
					response.Header.Set("X-Request-Id", f.id)
					response.Body = io.NopCloser(strings.NewReader(normal.wire))
					f.s.httpUpstream = &httpUpstreamRecorder{resp: response}
					f.s.GenerateSessionHash(f.c, f.body)
					var result *OpenAIForwardResult
					var err error
					if protocol == "responses" {
						result, err = f.s.Forward(f.c.Request.Context(), f.c, f.account, f.body)
					} else {
						result, err = f.s.ForwardAsChatCompletions(f.c.Request.Context(), f.c, f.account, f.body, "", "")
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.NotNil(t, result.CacheDiagnostic)
					require.NotNil(t, result.CacheTrace)
					awaitCacheTrace(t, f.s, f.c, result)
					t3SettlementWorkerDrained(t, f.s)
					require.True(t, result.CacheTrace.data.Prefix.Sampled)
					require.Equal(t, 4096, result.Usage.InputTokens)
					require.Equal(t, 1024, result.Usage.CacheReadInputTokens)
					expectedSource := "input_tokens_details.cached_tokens"
					if protocol == "rawcc" {
						expectedSource = "prompt_tokens_details.cached_tokens"
					}
					require.Equal(t, expectedSource, result.Usage.CacheReadSource)
					var logs cacheTraceBillingPositiveLog
					old := slog.Default()
					slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
					defer slog.SetDefault(old)
					input := &OpenAIRecordUsageInput{Result: result, APIKey: f.api, User: f.user, Account: f.account,
						PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
					require.NoError(t, f.s.RecordUsage(context.Background(), input))
					traces := cacheTraceBillingPositiveEvents(t, logs.snapshot(), "cache_trace")
					usages := cacheTraceBillingPositiveEvents(t, logs.snapshot(), "openai_cache_usage")
					require.Len(t, traces, 1)
					require.Len(t, usages, 1)
					payload := t3SettlementPayload(t, traces[0], false, CachePolicyFromGin(f.c).Revision())
					t3SettlementAssertUsageLink(t, traces[0], usages[0], result, f.account)
					require.NotContains(t, payload, "na_reason")
					require.NotEqual(t, "n/a_usage", payload["loss_class"])
					bits := int(cacheTraceBillingPositiveNumber(t, payload["usage_fields"]))
					require.Equal(t, 1, bits&15, "ready normal paths must observe cached_tokens read bit")
					require.Equal(t, expectedSource, usages[0]["read_source"])
					require.Equal(t, 1, f.rows.calls)
					require.Equal(t, 1, f.billing.calls)
					require.NotNil(t, f.rows.lastLog)
					require.EqualValues(t, 1024, f.rows.lastLog.CacheReadTokens)
					for _, event := range []map[string]any{traces[0], usages[0]} {
						encoded, err := json.Marshal(event)
						require.NoError(t, err)
						for _, marker := range []string{"ctc-private", "private-input-marker", "private-output-marker", "synthetic-token", "synthetic-account", "rid-review", "resp-corpus", "cc-corpus"} {
							require.NotContains(t, string(encoded), marker)
						}
					}
					rows = append(rows, cacheTraceBillingPositiveCase{Name: name, HasResult: true, TraceRows: 1, UsageRows: 1,
						Prompt: 4096, Read: 1024, Shape: "full", UsageFields: bits, ReadBitsChecked: true})
				})
			}
		}
	}
	require.Len(t, rows, 9)
}

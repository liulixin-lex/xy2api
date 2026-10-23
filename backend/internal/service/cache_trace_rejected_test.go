package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func rejectedTraceFixture(t testing.TB, size int) (*OpenAIGatewayService, *gin.Context, *http.Request) {
	t.Helper()
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "private-rejected-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	traceTestConfig(cfg)
	s := &OpenAIGatewayService{cfg: cfg}
	c := cacheTestContext(1, 10)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	session := s.cacheIdentityDigest("openai-session", "private-rejected-session")
	c.Set(openAICacheSessionGinKey, session)
	body := []byte(`{"input":"` + strings.Repeat("x", size) + `","prompt_cache_key":"private-rejected-pck"}`)
	s.beginCacheTrace(c, body)
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Session_id", "private-rejected-header")
	return s, c, req
}

func TestCacheTraceRejectedSchemaAndLegacyLinks(t *testing.T) {
	for _, reason := range []string{"budget_full", "expired", "not_ready"} {
		t.Run(reason, func(t *testing.T) {
			s, c, req := rejectedTraceFixture(t, 1024)
			pool := s.cacheTraceWorkerPool()
			pool.budget = 1 // ensure no worker starts or retains the marker-bearing body
			s.captureCacheTraceRequest(c, req, &Account{ID: 9, Platform: PlatformOpenAI}, "responses")
			live := cacheTraceFromGin(c)
			require.Nil(t, live.pending)
			require.Nil(t, live.async.result.Load())
			require.True(t, live.skipUsage)
			switch reason {
			case "expired":
				live.async.reason.Store(cacheTraceExpired)
			case "not_ready":
				live.async.reason.Store(0)
			}
			body := "data: " + `{"type":"response.completed","response":{"usage":{"input_tokens":4321,"input_tokens_details":{"cached_tokens":1024},"output_tokens":2}}}` + "\n\n"
			// Rejected buffered requests use exactly the existing billing traversal.
			usage := s.parseSSEUsageWithCacheTrace(c, body)
			require.Equal(t, s.parseSSEUsageFromBody(body), usage)
			observeCacheTraceUsageString(c, `{"usage":{"cache_read_input_tokens":9}}`)
			observeCacheTraceUsage(c, []byte(`{"usage":{"cache_read_input_tokens":9}}`))
			require.Zero(t, live.data.UsageFields, "no per-frame observation on rejection")
			result := &OpenAIForwardResult{RequestID: "private-rejected-request", Usage: *usage, CacheDiagnostic: &OpenAICacheDiagnostic{Session: live.sessionKey, GroupID: 10}}
			s.finishCacheTrace(c, result)
			expected := s.cacheIdentityDigest("openai-request", result.RequestID)
			require.Equal(t, expected, result.CacheTrace.data.RequestIDHMAC)
			// Changing the input after snapshot proves the usage events reuse the
			// already computed HMAC, rather than hashing the short ID a second time.
			result.RequestID = "private-rejected-must-not-rehash"
			var out bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
			s.observeCacheUsage(result, &Account{ID: 9, Platform: PlatformOpenAI}, time.Now())
			s.logCacheSettlement(result, &UsageLog{InputTokens: usage.InputTokens})
			result.CacheTrace.emitCacheTrace()
			slog.SetDefault(old)
			events := map[string]map[string]any{}
			for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
				var event map[string]any
				require.NoError(t, json.Unmarshal(line, &event))
				name, ok := event["msg"].(string)
				require.True(t, ok, "structured log event must have a string message")
				events[name] = event
			}
			event := events["cache_trace"]
			require.NotNil(t, event)
			require.Equal(t, "n/a_sampled_out", event["loss_class"])
			require.Equal(t, reason, event["na_reason"])
			require.Equal(t, float64(4321), event["P"])
			require.Equal(t, float64(1024), event["read"])
			for _, kind := range []string{"openai_cache_usage", "openai_cache_settled_usage"} {
				for _, key := range []string{"session_digest", "request_id_hmac"} {
					require.Equal(t, event[key], events[kind][key])
				}
			}
			// A's strict schema required set and the compact event's complete whitelist.
			// The exact same JSON is also replayed through A's actual schema validator
			// in the external evidence harness; no private path is embedded in Go tests.
			allowed := map[string]bool{}
			for _, key := range strings.Fields("time level msg v platform loss_class na_reason request_id_hmac session_digest account_id group_id P read") {
				allowed[key] = true
			}
			for key := range event {
				require.True(t, allowed[key], "unexpected event field")
			}
			for _, key := range strings.Fields("v platform account_id group_id P read request_id_hmac session_digest") {
				require.Contains(t, event, key)
			}
			require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), event["request_id_hmac"])
			require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), event["session_digest"])
			require.Equal(t, float64(1), event["v"])
			require.Equal(t, "openai", event["platform"])
			for _, marker := range []string{"private-rejected", "must-not-rehash"} {
				require.NotContains(t, out.String(), marker)
			}
			encoded, err := json.Marshal(event)
			require.NoError(t, err)
			t.Logf("GAP_SCHEMA_EVENT %s", encoded)
		})
	}
}

func TestCacheTraceReservationOwnershipAndRetry(t *testing.T) {
	s, c, req := rejectedTraceFixture(t, 1024)
	pool := s.cacheTraceWorkerPool()
	account := &Account{ID: 9, Platform: PlatformOpenAI}
	for i := 0; i < 3; i++ {
		s.captureCacheTraceRequest(c, req, account, "responses")
		tr := cacheTraceFromGin(c)
		require.NotNil(t, tr.pending)
		want := int64(len(tr.ingress)) + 2*req.ContentLength + 1024
		require.Equal(t, want, pool.reserved.Load(), "replacement releases the unsent reservation")
		require.Zero(t, pool.bytes)
	}
	resetCacheTraceAttempt(c)
	require.Zero(t, pool.reserved.Load())
	s.captureCacheTraceRequest(c, req, account, "responses")
	s.finishCacheTrace(c, nil) // failure before send must release the reservation
	require.Zero(t, pool.reserved.Load())
	s.captureCacheTraceRequest(c, req, account, "responses")
	s.startCacheTraceRequest(c)
	require.Eventually(t, func() bool { return pool.reserved.Load() == 0 }, time.Second, time.Millisecond)
}

func TestCacheTraceReservationConcurrentBudget(t *testing.T) {
	p := &cacheTraceWorkers{budget: 4096}
	var wg sync.WaitGroup
	held := make(chan int64, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cost, ok := p.reserve(100, 100); ok {
				held <- cost
			}
		}()
	}
	wg.Wait()
	close(held)
	require.LessOrEqual(t, p.reserved.Load(), p.budget)
	var sum int64
	for n := range held {
		sum += n
	}
	require.Equal(t, sum, p.reserved.Load())
	require.Positive(t, sum)
	p.reserved.Add(-sum)
	require.Zero(t, p.reserved.Load())
}

func TestCacheTraceRejectedCaptureAllocationsFlat(t *testing.T) {
	for _, size := range []int{1, 2, 5, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, c, req := rejectedTraceFixture(t, size<<20)
			p := s.cacheTraceWorkerPool()
			p.budget = 1
			account := &Account{ID: 9, Platform: PlatformOpenAI}
			allocations := testing.AllocsPerRun(1000, func() { s.captureCacheTraceRequest(c, req, account, "responses") })
			require.Equal(t, float64(1), allocations, "fixed-size rejection state only; no job or body work")
			require.Nil(t, cacheTraceFromGin(c).pending)
			require.Zero(t, p.reserved.Load())
		})
	}
}

func BenchmarkCacheTraceRejectedCapture(b *testing.B) {
	for _, size := range []int{1, 2, 5, 16} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s, c, req := rejectedTraceFixture(b, size<<20)
			p := s.cacheTraceWorkerPool()
			p.budget = 1
			account := &Account{ID: 9, Platform: PlatformOpenAI}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.captureCacheTraceRequest(c, req, account, "responses")
			}
		})
	}
}

// Reserving before constructing an enabled job must preserve the zero-work
// default path, including a malicious GetBody that would panic if invoked.
func TestCacheTraceRejectedNeverReadsBody(t *testing.T) {
	s, c, req := rejectedTraceFixture(t, 1024)
	s.cacheTraceWorkerPool().budget = 1
	req.GetBody = func() (io.ReadCloser, error) { panic("private-rejected-body-reader") }
	s.captureCacheTraceRequest(c, req, &Account{ID: 9, Platform: PlatformOpenAI}, "responses")
	s.startCacheTraceRequest(c)
	require.False(t, cacheTraceObserving(c))
	require.Nil(t, cacheTraceFrom(context.Background()))
}

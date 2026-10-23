package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Review's Codex-shaped fixture: type-first tool items, escaped arguments and
// tool outputs, and encrypted reasoning. No production content is retained.
func cacheTraceCodexBody(turns int) []byte {
	enc := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("r", 1500)))
	out := strings.Repeat(`line \"quoted\" path\\to\\file\n`, 90)
	items := []string{`{"type":"message","role":"developer","content":[{"type":"input_text","text":"` + strings.Repeat("p", 3000) + `"}]}`}
	for i := 0; i < turns; i++ {
		items = append(items,
			fmt.Sprintf(`{"type":"message","role":"user","content":[{"type":"input_text","text":"step %d"}]}`, i),
			fmt.Sprintf(`{"type":"reasoning","id":"rs_%d","summary":[],"encrypted_content":"%s"}`, i, enc),
			fmt.Sprintf(`{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"rg -n foo src/%d\"]}","call_id":"call_%d"}`, i, i),
			fmt.Sprintf(`{"type":"function_call_output","call_id":"call_%d","output":"%s"}`, i, out))
	}
	tools := `[{"type":"function","name":"shell","description":"` + strings.Repeat("d", 2000) + `","parameters":{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"}}}}}]`
	return []byte(`{"model":"gpt-5.4","instructions":"` + strings.Repeat("i", 8000) + `","tools":` + tools + `,"reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"store":false,"stream":true,"input":[` + strings.Join(items, ",") + `]}`)
}

func BenchmarkCacheTraceCodexWorker(b *testing.B) {
	for _, size := range []int{1, 2, 5} {
		body := cacheTraceCodexBody(size * 1024 * 1024 / 5250)
		b.Run(fmt.Sprintf("%dMB_bytes_%d", size, len(body)), func(b *testing.B) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-bench-secret"}}
			cfg.Gateway.CacheTrace.ChainPercent = 100
			svc := &OpenAIGatewayService{cfg: cfg}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				svc.computeCacheTrace(body, body, 1, false, "synthetic-session", "responses", "http")
			}
		})
	}
}

// The worker is drained outside the measured interval so this measures admitted
// requests, not a quick budget-rejection path. Includes trace construction,
// source metadata, admission and the nonblocking forwarding snapshot.
func TestCacheTraceAsyncCodexPathP99(t *testing.T) {
	if testing.Short() {
		t.Skip("latency/throughput measurement")
	}
	for _, size := range []int{1, 2, 5} {
		body := cacheTraceCodexBody(size * 1024 * 1024 / 5250)
		t.Run(fmt.Sprintf("%dMB", size), func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-bench-secret"}}
			traceTestConfig(cfg)
			cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
			cfg.Gateway.CacheTrace.AsyncBytesPerSecond = 64 << 30 // admitted-hook microbenchmark; d3 uses production default
			svc := &OpenAIGatewayService{cfg: cfg}
			c := cacheTestContext(1, 10)
			c.Request, _ = http.NewRequest(http.MethodPost, "http://fixture/v1/responses", bytes.NewReader(body))
			c.Set(openAICacheSessionGinKey, "synthetic-session")
			base := c.Request
			req, _ := http.NewRequest(http.MethodPost, "http://fixture/v1/responses", bytes.NewReader(body))
			account := &Account{ID: 1, Platform: PlatformOpenAI}
			disabled := &OpenAIGatewayService{cfg: &config.Config{}}
			const samples = 200
			deltas := make([]time.Duration, 0, samples)
			var workerTime time.Duration
			for i := 0; i < samples; i++ {
				c.Request = base
				before := time.Now()
				disabled.beginCacheTrace(c, body)
				disabled.captureCacheTraceRequest(c, req, account, "responses")
				disabled.startCacheTraceRequest(c)
				disabled.finishCacheTrace(c, &OpenAIForwardResult{})
				baseline := time.Since(before)
				before = time.Now()
				svc.beginCacheTrace(c, body)
				svc.captureCacheTraceRequest(c, req, account, "responses")
				svc.startCacheTraceRequest(c)
				result := &OpenAIForwardResult{}
				svc.finishCacheTrace(c, result)
				deltas = append(deltas, time.Since(before)-baseline)
				state := result.CacheTrace.async
				deadline := time.Now().Add(5 * time.Second)
				for state.result.Load() == nil && time.Now().Before(deadline) {
					time.Sleep(100 * time.Microsecond)
				}
				require.NotNil(t, state.result.Load(), "admitted job must complete")
				workerTime += time.Since(before)
			}
			sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
			p99 := deltas[samples*99/100]
			t.Logf("bytes=%d admitted=%d dropped=0 p50=%s p99=%s worker_wall_avg=%s throughput=%.2f_MB/s", len(body), samples, deltas[samples/2], p99, workerTime/samples, float64(len(body)*samples)/workerTime.Seconds()/1e6)
			// Timing is reported as evidence, not a CI timing assertion under -race.
			// The acceptance measurement is repeated without instrumentation at low load.
		})
	}
}

//go:build unit

package service

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
	"unsafe"

	"github.com/liulixin-lex/xy2api/internal/config"
)

// Each request is a new session. Prepare input and map capacity outside the
// timed region so this isolates allocations caused by legacy history insertion
// while cache_trace is off. This fixture also compiles on the pure baseline.
func BenchmarkCacheTraceDisabledNewSessionHistory(b *testing.B) {
	b.Logf("history_value_bytes=%d", unsafe.Sizeof(openAICacheHistory{}))
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	b.Cleanup(func() { slog.SetDefault(old) })
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-history-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	svc.cacheTelemetry().sessions = make(map[string]openAICacheHistory, b.N)
	results := make([]OpenAIForwardResult, b.N)
	for i := range results {
		results[i] = OpenAIForwardResult{RequestID: fmt.Sprint(i), CacheDiagnostic: &OpenAICacheDiagnostic{Session: fmt.Sprintf("synthetic-session-%d", i)}, Usage: OpenAIUsage{InputTokens: 2048}}
	}
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range results {
		svc.observeCacheUsage(&results[i], account, now)
	}
}

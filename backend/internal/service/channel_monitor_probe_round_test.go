//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
	"github.com/stretchr/testify/require"
)

// TestRunMonitorProbeRound_PingRunsInParallel HEAD ping 与模型请求同时发出，不再串行先等 ping；
// 每个模型的请求都带本监控的签名头。
func TestRunMonitorProbeRound_PingRunsInParallel(t *testing.T) {
	const delay = 600 * time.Millisecond
	up, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, r *http.Request, body map[string]any, _ int) {
		time.Sleep(delay)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeProbeSSE(w, probeAnswerEvents(probeProtocolChat, probeTestAnswer(body))...)
	})
	m := &ChannelMonitor{
		ID: 7, Provider: MonitorProviderOpenAI, Endpoint: endpoint, APIKey: "key",
		PrimaryModel: "gpt-4o", ExtraModels: []string{"gpt-4.1"},
	}
	limits := fastProbeLimits()
	limits.DegradedAfter = 5 * time.Second

	start := time.Now()
	results := runMonitorProbeRound(context.Background(), m, limits)
	elapsed := time.Since(start)

	require.Less(t, elapsed, 2*delay-100*time.Millisecond, "ping and model requests must overlap")
	require.Len(t, results, 2)
	for i, model := range []string{"gpt-4o", "gpt-4.1"} {
		require.Equal(t, model, results[i].Model)
		require.Equal(t, MonitorStatusOperational, results[i].Status, results[i].Message)
		require.NotNil(t, results[i].PingLatencyMs)
		require.GreaterOrEqual(t, *results[i].PingLatencyMs, int(delay/time.Millisecond))
	}
	posts := 0
	for _, seen := range up.requests() {
		if seen.Path == "/v1/chat/completions" {
			posts++
			id, ok := monitorprobe.Verify(seen.Header.Get(monitorprobe.HeaderName), time.Now())
			require.True(t, ok)
			require.Equal(t, int64(7), id)
		}
	}
	require.Equal(t, 2, posts)
}

// TestRunMonitorProbeRound_SharedBudget 多个模型并发，但都在同一个预算内结束。
func TestRunMonitorProbeRound_SharedBudget(t *testing.T) {
	_, endpoint := newProbeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		<-r.Context().Done()
	})
	m := &ChannelMonitor{
		ID: 8, Provider: MonitorProviderOpenAI, Endpoint: endpoint, APIKey: "key",
		PrimaryModel: "gpt-4o", ExtraModels: []string{"gpt-4.1", "gpt-4.1-mini"},
	}
	limits := fastProbeLimits()
	limits.Budget = 300 * time.Millisecond
	start := time.Now()
	results := runMonitorProbeRound(context.Background(), m, limits)
	require.Less(t, time.Since(start), 2*time.Second)
	for _, r := range results {
		require.Equal(t, MonitorStatusError, r.Status)
		require.Contains(t, r.Message, "timeout after 300ms")
	}
}

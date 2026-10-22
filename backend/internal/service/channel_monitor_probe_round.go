package service

import (
	"context"
	"sync"
)

// runMonitorProbeRound 一个监控的一轮探测：对 endpoint 源站的 HEAD ping 与各模型请求同时发出
// （不再串行先等 ping），模型之间并发，共用一个预算；所有模型记录同一个 ping 延迟。
// 每个模型的请求都用本监控的 ID 签名（monitorprobe.HeaderName）。
func runMonitorProbeRound(ctx context.Context, m *ChannelMonitor, limits probeLimits) []*CheckResult {
	models := append([]string{m.PrimaryModel}, m.ExtraModels...)
	results := make([]*CheckResult, len(models))
	opts := &CheckOptions{
		MonitorID:        m.ID,
		APIMode:          m.APIMode,
		ExtraHeaders:     m.ExtraHeaders,
		BodyOverrideMode: m.BodyOverrideMode,
		BodyOverride:     m.BodyOverride,
	}

	roundCtx, cancel := context.WithTimeout(ctx, limits.Budget)
	defer cancel()

	var pingMs *int
	var wg sync.WaitGroup
	wg.Add(1 + len(models))
	go func() {
		defer wg.Done()
		pingMs = pingEndpointOrigin(ctx, m.Endpoint)
	}()
	for i, model := range models {
		go func() {
			defer wg.Done()
			results[i] = runProbe(roundCtx, m.Provider, m.Endpoint, m.APIKey, model, opts, limits)
		}()
	}
	wg.Wait()

	for _, r := range results {
		r.PingLatencyMs = pingMs
	}
	return results
}

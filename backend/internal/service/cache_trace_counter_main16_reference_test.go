//go:build unit

package service

import (
	"sort"
	"time"
)

// Frozen reference from main 16a776e347: cache_trace_counters.go.
// Only function names changed. This is the default-behavior regression oracle.
func (c *CacheTraceCounters) main16CounterPreviousHour(now time.Time) []CacheTraceHour {
	out := []CacheTraceHour{}
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	target := now.UTC().Truncate(time.Hour).Add(-time.Hour)
	for _, hour := range c.hours {
		if hour == nil || !hour.at.Equal(target) {
			continue
		}
		for key, value := range hour.values {
			out = append(out, CacheTraceHour{BucketStart: hour.at, ProcessID: c.processID, Platform: "openai", Transport: key.transport, RelayFamily: key.relay, PCKClassOut: key.pck, LossClass: key.loss, ModelGen: key.model, ForkKind: key.fork, Requests: value.requests, PromptTokens: value.prompt, ReadTokens: value.read, OpportunityTokens: value.opportunity})
		}
	}
	return out
}

func (c *CacheTraceCounters) main16CounterSnapshot(now time.Time, q CachePanelQuery, policy *CachePolicySnapshot) map[string]any {
	start := now.Add(-cachePanelDuration(q.Window))
	effective := start
	if c.started.After(effective) {
		effective = c.started
	}
	result := map[string]any{
		"policy_version": policy.NorthstarPolicy().PolicyVersion, "as_of": now, "generated_at": now,
		"window": CachePanelWindow{Start: start, End: now}, "effective_start": effective, "process_started_at": c.started,
		"trace_enabled": policy.TraceEnabled(), "trace_policy_rev": policy.ObservabilityRevision(), "schema_version": 1,
		"platforms_covered": []string{"openai"}, "scope_notes": []string{"messages_bridge_excluded"},
		"coverage":     map[string]any{"business_requests": int64(0), "business_prompt_tokens": int64(0), "traced_requests": int64(0), "traced_prompt_tokens": int64(0), "coverage_ratio": nil, "category_known_ratio": nil, "targets": map[string]float64{"coverage_ratio": 0.95, "category_known_ratio": 0.99}},
		"loss_classes": []any{}, "pck_in": []any{}, "pck_out": []any{}, "pck_out_is_uuid_ratio": nil,
		"session_headers": []any{}, "sess_out_relation": []any{}, "route_reasons": []any{}, "rebind_causes": []any{}, "model_isolated_switch_rate": nil, "host_read_ewma": []any{},
	}
	if !policy.TraceEnabled() || q.Platform != "all" && q.Platform != "openai" {
		return result
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	total := cacheCounterMinute{dimensions: map[string]map[string]cacheCounterValue{}}
	for _, b := range c.minutes {
		if b == nil || b.at.Before(start.Truncate(time.Minute)) || b.at.After(now) {
			continue
		}
		total.business.add(b.business)
		total.trace.add(b.trace)
		total.known += b.known
		total.uuid += b.uuid
		total.pairs += b.pairs
		total.switches += b.switches
		for i := range total.headersIn {
			total.headersIn[i] += b.headersIn[i]
			total.headersOut[i] += b.headersOut[i]
		}
		for dimension, values := range b.dimensions {
			for key, value := range values {
				total.add(dimension, key, value)
			}
		}
	}
	result["coverage"] = map[string]any{"business_requests": total.business.requests, "business_prompt_tokens": total.business.prompt, "traced_requests": total.trace.requests, "traced_prompt_tokens": total.trace.prompt,
		"coverage_ratio": northstarRatio(float64(total.trace.prompt), float64(total.business.prompt)), "category_known_ratio": northstarRatio(float64(total.known), float64(total.trace.requests)),
		"targets": map[string]float64{"coverage_ratio": 0.95, "category_known_ratio": 0.99}}
	for _, dim := range []struct{ source, target, key string }{{"loss", "loss_classes", "class"}, {"pck_in", "pck_in", "class"}, {"pck_out", "pck_out", "class"}, {"relation", "sess_out_relation", "relation"}, {"route", "route_reasons", "reason"}, {"rebind", "rebind_causes", "cause"}} {
		values := map[string]cacheCounterValue{}
		for key, value := range total.dimensions[dim.source] {
			if key == "" {
				key = "unknown"
			}
			if value.requests < 10 {
				if dim.source == "loss" && len(key) >= 4 && key[:4] == "n/a_" {
					key = "n/a_other"
				} else {
					key = "other"
				}
			}
			previous := values[key]
			previous.add(value)
			values[key] = previous
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := []any{}
		for _, key := range keys {
			value := values[key]
			item := map[string]any{dim.key: key, "requests": value.requests}
			if dim.source != "rebind" {
				item["prompt_tokens"] = value.prompt
			}
			if dim.source != "rebind" && dim.source != "relation" {
				item["read_tokens"] = value.read
			}
			if dim.source == "loss" {
				excluded := len(key) >= 4 && key[:4] == "n/a_"
				item["excluded"] = excluded
				item["opportunity_tokens"] = nil
				if !excluded {
					item["opportunity_tokens"] = value.opportunity
				}
			}
			if dim.source == "pck_out" {
				item["relay_family"] = "unknown"
				item["under_read_tokens"] = value.opportunity
			}
			items = append(items, item)
		}
		result[dim.target] = items
	}
	result["pck_out_is_uuid_ratio"] = northstarRatio(float64(total.uuid), float64(total.trace.requests))
	result["model_isolated_switch_rate"] = northstarRatio(float64(total.switches), float64(total.pairs))
	headers := []any{}
	for i := range total.headersIn {
		if total.headersIn[i] == 0 && total.headersOut[i] == 0 {
			continue
		}
		name := "metadata.user_id"
		if i < len(cacheTraceHeaders) {
			name = cacheTraceHeaders[i]
		}
		headers = append(headers, map[string]any{"header": name, "in_ratio": northstarRatio(float64(total.headersIn[i]), float64(total.trace.requests)), "out_ratio": northstarRatio(float64(total.headersOut[i]), float64(total.trace.requests))})
	}
	result["session_headers"] = headers
	hosts := []any{}
	names := make([]string, 0, len(c.hosts))
	for name := range c.hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := c.hosts[name]
		if h.samples < 10 || now.Sub(h.at) > 30*time.Minute {
			continue
		}
		hosts = append(hosts, map[string]any{"host": name, "read_share": northstarRatio(h.read, h.prompt), "samples": h.samples, "half_life_s": 1800})
	}
	result["host_read_ewma"] = hosts
	return result
}

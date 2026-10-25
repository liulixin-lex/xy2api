package service

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type cacheCounterValue struct{ requests, prompt, read, opportunity int64 }

func (v *cacheCounterValue) add(other cacheCounterValue) {
	v.requests += other.requests
	v.prompt += other.prompt
	v.read += other.read
	v.opportunity += other.opportunity
}

type cacheCounterMinute struct {
	at                           time.Time
	business, trace              cacheCounterValue
	known, uuid, pairs, switches int64
	headersIn, headersOut        [15]int64
	dimensions                   map[string]map[string]cacheCounterValue
}
type cacheCounterHourKey struct{ transport, relay, pck, loss, model, fork string }
type cacheCounterHour struct {
	at     time.Time
	values map[cacheCounterHourKey]cacheCounterValue
}
type cacheCounterHost struct {
	at           time.Time
	prompt, read float64
	samples      int64
}
type cacheCounterSessionKey struct{ session, model string }
type cacheCounterSession struct {
	at      time.Time
	account int64
}
type CacheTraceCounters struct {
	mu           sync.Mutex
	started      time.Time
	processID    string
	minutes      [1441]*cacheCounterMinute
	hours        [48]*cacheCounterHour
	hosts        map[string]cacheCounterHost
	sessions     map[cacheCounterSessionKey]cacheCounterSession
	sessionOrder [2048]cacheCounterSessionKey
	nextSession  int
	providers    [2]*CacheTraceCounters
}

func NewCacheTraceCounters() *CacheTraceCounters {
	return &CacheTraceCounters{started: cachePolicyProcessStartedAt, processID: uuid.NewString(), hosts: map[string]cacheCounterHost{}, sessions: map[cacheCounterSessionKey]cacheCounterSession{}}
}
func (c *CacheTraceCounters) minute(now time.Time) *cacheCounterMinute {
	at := now.UTC().Truncate(time.Minute)
	index := (at.Unix() / 60) % int64(len(c.minutes))
	if index < 0 {
		index += int64(len(c.minutes))
	}
	b := c.minutes[index]
	if b == nil || !b.at.Equal(at) {
		b = &cacheCounterMinute{at: at, dimensions: map[string]map[string]cacheCounterValue{}}
		c.minutes[index] = b
	}
	return b
}
func (b *cacheCounterMinute) add(dimension, key string, value cacheCounterValue) {
	values := b.dimensions[dimension]
	if values == nil {
		values = map[string]cacheCounterValue{}
		b.dimensions[dimension] = values
	}
	if _, ok := values[key]; !ok && len(values) >= 128 {
		key = "other"
	}
	previous := values[key]
	previous.add(value)
	values[key] = previous
}
func (c *CacheTraceCounters) RecordBusiness(now time.Time, prompt, read int64, host, session, model string, account int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.minute(now)
	b.business.add(cacheCounterValue{requests: 1, prompt: prompt, read: read})
	if host != "" {
		if _, ok := c.hosts[host]; !ok && len(c.hosts) >= 1024 {
			host = "other"
		}
		h := c.hosts[host]
		decay := math.Exp(-math.Ln2 * math.Max(0, now.Sub(h.at).Seconds()) / 1800)
		h.prompt = h.prompt*decay + float64(prompt)
		h.read = h.read*decay + float64(read)
		h.at = now
		h.samples++
		c.hosts[host] = h
	}
	if session != "" {
		key := cacheCounterSessionKey{session, model}
		previous, exists := c.sessions[key]
		if exists && now.Sub(previous.at) <= time.Hour && now.After(previous.at) {
			b.pairs++
			if previous.account != account {
				b.switches++
			}
		}
		if !exists {
			delete(c.sessions, c.sessionOrder[c.nextSession])
			c.sessionOrder[c.nextSession] = key
			c.nextSession = (c.nextSession + 1) % len(c.sessionOrder)
		}
		c.sessions[key] = cacheCounterSession{at: now, account: account}
	}
}

// CacheTraceCounterSample contains only aggregation facts from a settled
// emitted event. Request identifiers and session digests do not enter it.
type CacheTraceCounterSample struct {
	PromptTokens, ReadTokens, PreviousPrompt int
	LossClass, Transport, ModelGen, ForkKind string
	PCKClassIn, PCKClassOut, SessOutRelation string
	RouteReason, RebindCause                 string
	SessHdrIn, SessHdrOut                    uint16
	PCKOutIsUUID                             bool
}

func (c *CacheTraceCounters) RecordTrace(now time.Time, d cacheTraceData) {
	c.recordTraceSample(now, CacheTraceCounterSample{
		PromptTokens: d.PromptTokens, ReadTokens: d.ReadTokens, PreviousPrompt: d.Prefix.PreviousPrompt,
		LossClass: d.LossClass, Transport: d.Transport, ModelGen: d.Prefix.ModelGen, ForkKind: d.Prefix.ForkKind,
		PCKClassIn: d.PCKClassIn, PCKClassOut: d.PCKClassOut, SessOutRelation: d.SessOutRelation,
		RouteReason: d.RouteReason, RebindCause: d.RebindCause, SessHdrIn: d.SessHdrIn, SessHdrOut: d.SessHdrOut,
		PCKOutIsUUID: d.PCKOutIsUUID,
	})
}

func (c *CacheTraceCounters) recordTraceSample(now time.Time, d CacheTraceCounterSample) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	value := cacheCounterValue{requests: 1, prompt: int64(d.PromptTokens), read: int64(d.ReadTokens)}
	excluded := len(d.LossClass) >= 4 && d.LossClass[:4] == "n/a_"
	if !excluded && d.LossClass != "cold" && d.LossClass != "below_min" {
		value.opportunity = int64(max(0, min(d.PreviousPrompt, d.PromptTokens)-d.ReadTokens))
	}
	b := c.minute(now)
	b.trace.add(value)
	b.add("loss", d.LossClass, value)
	b.add("pck_in", d.PCKClassIn, value)
	pckValue := value
	if d.LossClass != "under_read" {
		pckValue.opportunity = 0
	}
	b.add("pck_out", d.PCKClassOut, pckValue)
	b.add("relation", d.SessOutRelation, value)
	b.add("route", d.RouteReason, value)
	if d.RebindCause != "" && d.RebindCause != "none" {
		b.add("rebind", d.RebindCause, cacheCounterValue{requests: 1})
	}
	if d.PCKOutIsUUID {
		b.uuid++
	}
	if d.PCKClassIn != "" && d.PCKClassIn != "unknown" && d.PCKClassOut != "" && d.PCKClassOut != "unknown" && d.SessHdrIn&(1<<15) == 0 && d.SessHdrOut&(1<<15) == 0 {
		b.known++
	}
	for i := range b.headersIn {
		if d.SessHdrIn&(1<<i) != 0 {
			b.headersIn[i]++
		}
		if d.SessHdrOut&(1<<i) != 0 {
			b.headersOut[i]++
		}
	}
	at := now.UTC().Truncate(time.Hour)
	index := (at.Unix() / 3600) % int64(len(c.hours))
	if index < 0 {
		index += int64(len(c.hours))
	}
	hour := c.hours[index]
	if hour == nil || !hour.at.Equal(at) {
		hour = &cacheCounterHour{at: at, values: map[cacheCounterHourKey]cacheCounterValue{}}
		c.hours[index] = hour
	}
	key := cacheCounterHourKey{d.Transport, "unknown", d.PCKClassOut, d.LossClass, d.ModelGen, d.ForkKind}
	if _, ok := hour.values[key]; !ok && len(hour.values) >= 2048 {
		key = cacheCounterHourKey{"other", "unknown", "other", "other", "other", "other"}
	}
	old := hour.values[key]
	old.add(value)
	hour.values[key] = old
}

func (c *CacheTraceCounters) previousPlatformHour(now time.Time, platform string) []CacheTraceHour {
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
			out = append(out, CacheTraceHour{BucketStart: hour.at, ProcessID: c.processID, Platform: platform, Transport: key.transport, RelayFamily: key.relay, PCKClassOut: key.pck, LossClass: key.loss, ModelGen: key.model, ForkKind: key.fork, Requests: value.requests, PromptTokens: value.prompt, ReadTokens: value.read, OpportunityTokens: value.opportunity})
		}
	}
	return out
}

func (c *CacheTraceCounters) Snapshot(now time.Time, q CachePanelQuery, policy *CachePolicySnapshot) map[string]any {
	start := now.Add(-cachePanelDuration(q.Window))
	effective := start
	if c.started.After(effective) {
		effective = c.started
	}
	covered, notes := cacheCounterScope(policy, q.Platform)
	result := map[string]any{
		"policy_version": policy.NorthstarPolicy().PolicyVersion, "as_of": now, "generated_at": now,
		"window": CachePanelWindow{Start: start, End: now}, "effective_start": effective, "process_started_at": c.started,
		"trace_enabled": policy.TraceEnabled(), "trace_policy_rev": policy.ObservabilityRevision(), "schema_version": 1,
		"platforms_covered": covered, "scope_notes": notes,
		"coverage":     map[string]any{"business_requests": int64(0), "business_prompt_tokens": int64(0), "traced_requests": int64(0), "traced_prompt_tokens": int64(0), "coverage_ratio": nil, "category_known_ratio": nil, "targets": map[string]float64{"coverage_ratio": 0.95, "category_known_ratio": 0.99}},
		"loss_classes": []any{}, "pck_in": []any{}, "pck_out": []any{}, "pck_out_is_uuid_ratio": nil,
		"session_headers": []any{}, "sess_out_relation": []any{}, "route_reasons": []any{}, "rebind_causes": []any{}, "model_isolated_switch_rate": nil, "host_read_ewma": []any{},
	}
	if !policy.TraceEnabled() {
		return result
	}
	selected := covered
	if q.Platform != "all" {
		// An explicit query can still inspect its recorded window after the
		// platform is removed from the current observation policy.
		selected = []string{q.Platform}
	}
	total, openAI, hostsByName := c.selectedSnapshot(now, start, selected)
	result["coverage"] = map[string]any{"business_requests": total.business.requests, "business_prompt_tokens": total.business.prompt, "traced_requests": total.trace.requests, "traced_prompt_tokens": total.trace.prompt,
		"coverage_ratio": northstarRatio(float64(total.trace.prompt), float64(total.business.prompt)), "category_known_ratio": northstarRatio(float64(openAI.known), float64(openAI.trace.requests)),
		"targets": map[string]float64{"coverage_ratio": 0.95, "category_known_ratio": 0.99}}
	for _, dim := range []struct{ source, target, key string }{{"loss", "loss_classes", "class"}, {"pck_in", "pck_in", "class"}, {"pck_out", "pck_out", "class"}, {"relation", "sess_out_relation", "relation"}, {"route", "route_reasons", "reason"}, {"rebind", "rebind_causes", "cause"}} {
		values := map[string]cacheCounterValue{}
		basis := &total
		if dim.source == "pck_in" || dim.source == "pck_out" || dim.source == "relation" {
			basis = &openAI
		}
		for key, value := range basis.dimensions[dim.source] {
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
	result["pck_out_is_uuid_ratio"] = northstarRatio(float64(openAI.uuid), float64(openAI.trace.requests))
	result["model_isolated_switch_rate"] = northstarRatio(float64(total.switches), float64(total.pairs))
	headers := []any{}
	for i := range openAI.headersIn {
		if openAI.headersIn[i] == 0 && openAI.headersOut[i] == 0 {
			continue
		}
		name := "metadata.user_id"
		if i < len(cacheTraceHeaders) {
			name = cacheTraceHeaders[i]
		}
		headers = append(headers, map[string]any{"header": name, "in_ratio": northstarRatio(float64(openAI.headersIn[i]), float64(openAI.trace.requests)), "out_ratio": northstarRatio(float64(openAI.headersOut[i]), float64(openAI.trace.requests))})
	}
	result["session_headers"] = headers
	hosts := []any{}
	names := make([]string, 0, len(hostsByName))
	for name := range hostsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := hostsByName[name]
		if h.samples < 10 || now.Sub(h.at) > 30*time.Minute {
			continue
		}
		hosts = append(hosts, map[string]any{"host": name, "read_share": northstarRatio(h.read, h.prompt), "samples": h.samples, "half_life_s": 1800})
	}
	result["host_read_ewma"] = hosts
	return result
}

package service

import (
	"context"
	"time"
)

type CachePolicyEffective struct {
	State         string   `json:"state"`
	Active        *bool    `json:"active"`
	Blockers      []string `json:"blockers"`
	Warnings      []string `json:"warnings"`
	ClippedFields []string `json:"clipped_fields"`
}
type CachePolicyLoaded struct {
	Rev      int64     `json:"rev"`
	LoadedAt time.Time `json:"loaded_at"`
}
type CachePolicyView struct {
	Key           string               `json:"key"`
	SettingKey    string               `json:"setting_key"`
	Available     bool                 `json:"available"`
	Risk          string               `json:"risk"`
	GatedByMaster bool                 `json:"gated_by_master"`
	Stored        *CachePolicyEnvelope `json:"stored"`
	Loaded        *CachePolicyLoaded   `json:"loaded"`
	Effective     CachePolicyEffective `json:"effective"`
	Defaults      map[string]any       `json:"defaults"`
	Schema        []map[string]any     `json:"schema"`
}
type CachePolicyList struct {
	GeneratedAt      time.Time `json:"generated_at"`
	ProcessStartedAt time.Time `json:"process_started_at"`
	BuildVersion     string    `json:"build_version"`
	RefreshIntervalS int       `json:"refresh_interval_s"`
	Static           struct {
		KillSwitch bool            `json:"kill_switch"`
		Caps       CachePolicyCaps `json:"caps"`
	} `json:"static"`
	Existing struct {
		Diagnostics  bool `json:"openai_cache_diagnostics_enabled"`
		AwareRouting bool `json:"openai_cache_aware_routing_enabled"`
		Rollout      int  `json:"openai_cache_rollout_percent"`
		Claude       struct {
			Configured   bool   `json:"configured"`
			GroupCount   int    `json:"group_count"`
			SettingsLink string `json:"settings_link"`
		} `json:"claude_cache_fallback"`
	} `json:"existing"`
	Policies []CachePolicyView `json:"policies"`
}

func policyRisk(i int) string {
	switch i {
	case 1, 2:
		return "low"
	case 5:
		return "highest"
	default:
		return "high"
	}
}
func (s *CachePolicyService) effective(i int, stored, loaded cachePolicyEntry) CachePolicyEffective {
	return s.effectiveAt(i, stored, loaded, s.Snapshot())
}

func (s *CachePolicyService) effectiveAt(i int, stored, loaded cachePolicyEntry, p *CachePolicySnapshot) CachePolicyEffective {
	return cachePolicyStaleEffective(i, p, s.policyEffectiveAt(i, stored, loaded, p))
}

// A stale-bounded snapshot blocks every master-gated behavior key available in
// this build. stale_snapshot follows kill_switch and parse_error and precedes
// all other blockers; killed and parse_error keep their state.
func cachePolicyStaleEffective(i int, p *CachePolicySnapshot, e CachePolicyEffective) CachePolicyEffective {
	if i < 3 || e.Active == nil || !p.behaviorStale() {
		return e
	}
	inactive := false
	e.Active = &inactive
	at := 0
	for at < len(e.Blockers) && (e.Blockers[at] == "kill_switch" || e.Blockers[at] == "parse_error") {
		at++
	}
	blockers := make([]string, 0, len(e.Blockers)+1)
	blockers = append(append(blockers, e.Blockers[:at]...), "stale_snapshot")
	e.Blockers = append(blockers, e.Blockers[at:]...)
	if e.State != "killed" && e.State != "parse_error" {
		e.State = "blocked"
	}
	// Reported only for on or masked_by_master, neither of which remains.
	warnings := make([]string, 0, len(e.Warnings))
	for _, warning := range e.Warnings {
		if warning != "anthropic_observability_missing" {
			warnings = append(warnings, warning)
		}
	}
	e.Warnings = warnings
	return e
}

func (s *CachePolicyService) policyEffectiveAt(i int, stored, loaded cachePolicyEntry, p *CachePolicySnapshot) CachePolicyEffective {
	active := false
	e := CachePolicyEffective{Active: &active, Blockers: []string{}, Warnings: []string{}, ClippedFields: []string{}}
	if !s.cachePolicySupported(i) {
		e.State = "not_in_build"
		e.Active = nil
		return e
	}
	if i == 3 {
		return s.upstreamIdentityEffective(stored, loaded, p)
	}
	if i == 4 || i == 5 {
		return s.m4Effective(i, stored, loaded, p)
	}
	enabled, _ := loaded.envelope.Spec["enabled"].(bool)
	if i == 2 {
		enabled, _ = loaded.envelope.Spec["rollup_enabled"].(bool)
	}
	active = enabled && !loaded.parseError && !s.kill && (i != 1 || s.diagnostics)
	if i == 1 {
		if policyInt(loaded.envelope.Spec["byte_budget_mib"]) > s.caps.MaxByteBudgetMiB {
			e.ClippedFields = append(e.ClippedFields, "byte_budget_mib")
		}
		if policyInt(loaded.envelope.Spec["rate_mib_per_s"]) > s.caps.MaxRateMiBPerS {
			e.ClippedFields = append(e.ClippedFields, "rate_mib_per_s")
		}
	}
	if s.kill {
		e.Blockers = append(e.Blockers, "kill_switch")
	}
	if loaded.parseError || stored.parseError {
		e.Blockers = append(e.Blockers, "parse_error")
	}
	if i == 1 && !s.diagnostics {
		e.Blockers = append(e.Blockers, "diagnostics_disabled")
	}
	switch {
	case s.kill:
		e.State = "killed"
	case loaded.parseError || stored.parseError:
		e.State = "parse_error"
	case i == 1 && !s.diagnostics:
		e.State = "blocked"
	case loaded.envelope.Rev < stored.envelope.Rev:
		e.State = "pending_reload"
	case active:
		e.State = "on"
	default:
		e.State = "off"
	}
	return e
}
func (s *CachePolicyService) schema(i int) []map[string]any {
	if !s.cachePolicySupported(i) {
		return []map[string]any{}
	}
	if i == 2 {
		return []map[string]any{
			{"field": "rollup_enabled", "type": "bool"},
			{"field": "backfill_days", "type": "int", "min": 0, "max": 30},
			{"field": "policy_version", "type": "string"},
			{"field": "excluded_group_ids", "type": "id_list", "ref": "group"},
			{"field": "exclude_media", "type": "bool"},
			{"field": "model_thresholds", "type": "threshold_rules", "options": []int{512, 1024, 2048, 4096, 6144}},
			{"field": "unknown_threshold", "type": "enum", "options": []string{"aux_only"}},
		}
	}
	if i == 3 {
		return upstreamIdentitySchema()
	}
	if i == 4 || i == 5 {
		return s.m4Schema(i)
	}
	fields := []map[string]any{{"field": "enabled", "type": "bool"}}
	if i == 1 {
		fields = append(fields,
			map[string]any{"field": "sample_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
			map[string]any{"field": "chain_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
			map[string]any{"field": "byte_budget_mib", "type": "int", "min": 8, "max": s.caps.MaxByteBudgetMiB, "unit": "MiB", "cap_source": "gateway.cache_trace.max_byte_budget_mib"},
			map[string]any{"field": "rate_mib_per_s", "type": "int", "min": 1, "max": s.caps.MaxRateMiBPerS, "unit": "MiB/s", "cap_source": "gateway.cache_trace.max_rate_mib_per_s"})
	}
	if i == 1 && s.providerTraceAvailable {
		fields = append(fields,
			map[string]any{"field": "platforms", "type": "enum_list", "options": []string{"openai", "anthropic", "gemini", "messages_bridge"}},
			map[string]any{"field": "ingress_chain_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"})
	}
	if i == 1 && s.northstarAvailable {
		fields = append(fields, map[string]any{"field": "persist_trace_hourly", "type": "bool"})
	}
	return fields
}
func (s *CachePolicyService) view(i int, stored cachePolicyEntry, p *CachePolicySnapshot) CachePolicyView {
	v := CachePolicyView{Key: cachePolicyKeys[i], SettingKey: cachePolicySettings[i], Risk: policyRisk(i), GatedByMaster: i >= 3, Schema: s.schema(i)}
	if !s.cachePolicySupported(i) {
		v.Effective = s.effective(i, stored, stored)
		return v
	}
	v.Available = true
	// Never expose the snapshot's mutable maps to admin callers.
	copyEnvelope := stored.envelope
	if stored.envelope.UpdatedBy != nil {
		actor := *stored.envelope.UpdatedBy
		copyEnvelope.UpdatedBy = &actor
	}
	if stored.envelope.UpdatedAt != nil {
		at := *stored.envelope.UpdatedAt
		copyEnvelope.UpdatedAt = &at
	}
	if stored.envelope.Reason != nil {
		reason := *stored.envelope.Reason
		copyEnvelope.Reason = &reason
	}

	copyEnvelope.Spec = make(map[string]any, len(stored.envelope.Spec))
	for k, value := range stored.envelope.Spec {
		copyEnvelope.Spec[k] = cloneCachePolicyValue(value)
	}
	v.Stored = &copyEnvelope
	loaded := p.entries[i]
	v.Loaded = &CachePolicyLoaded{loaded.envelope.Rev, loaded.loadedAt}
	v.Defaults = s.defaults(i)
	v.Effective = s.effective(i, stored, loaded)
	return v
}
func (s *CachePolicyService) Get(ctx context.Context, key string) (CachePolicyView, error) {
	i := policyIndex(key)
	if !s.cachePolicySupported(i) {
		return CachePolicyView{}, PolicyNotInBuild(key)
	}
	raw, err := s.repo.GetMultiple(ctx, []string{cachePolicySettings[i]})
	if err != nil {
		return CachePolicyView{}, err
	}
	view := s.view(i, s.parse(i, raw[cachePolicySettings[i]]), s.Snapshot())
	if i == 3 {
		s.populateCachePolicyAccountOptions(ctx, &view)
	}
	if i == 4 || i == 5 {
		s.populateCachePolicyGroupOptions(ctx, &view)
	}
	return view, nil
}
func (s *CachePolicyService) List(ctx context.Context, version string) (CachePolicyList, error) {
	keys := append([]string{}, cachePolicySettings[:]...)
	keys = append(keys, SettingKeyClaudeCacheFallbackPolicy)
	raw, err := s.repo.GetMultiple(ctx, keys)
	if err != nil {
		return CachePolicyList{}, err
	}
	p := s.Snapshot()
	list := CachePolicyList{GeneratedAt: s.now().UTC(), ProcessStartedAt: s.started, BuildVersion: version, RefreshIntervalS: 60, Policies: make([]CachePolicyView, 0, 6)}
	list.Static.KillSwitch = s.kill
	list.Static.Caps = s.caps
	list.Existing.Diagnostics = s.diagnostics
	list.Existing.AwareRouting = s.aware
	list.Existing.Rollout = s.rollout
	claude := readClaudeCacheFallbackPolicy([]byte(raw[SettingKeyClaudeCacheFallbackPolicy]))
	list.Existing.Claude.Configured = raw[SettingKeyClaudeCacheFallbackPolicy] != ""
	list.Existing.Claude.GroupCount = len(claude.GroupIDs)
	list.Existing.Claude.SettingsLink = "/admin/settings?tab=gateway"
	for i := range cachePolicyKeys {
		e := cachePolicyEntry{}
		if s.cachePolicySupported(i) {
			e = s.parse(i, raw[cachePolicySettings[i]])
		}
		list.Policies = append(list.Policies, s.view(i, e, p))
	}
	s.populateCachePolicyAccountOptions(ctx, &list.Policies[3])
	if s.m4Available {
		s.populateCachePolicyGroupOptions(ctx, &list.Policies[4], &list.Policies[5])
	}
	return list, nil
}

func cloneCachePolicyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = cloneCachePolicyValue(item)
		}
		return out
	case []int64:
		return append([]int64{}, v...)
	case []CacheNorthstarThresholdRule:
		return append([]CacheNorthstarThresholdRule{}, v...)
	default:
		return value
	}
}

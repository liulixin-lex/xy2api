package service

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
)

// M4 only adds administrator policy storage. No request path consumes these
// entries until the separately reviewed identity/routing implementations land.
type CachePolicyGroupLookup interface {
	ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, *bool) ([]Group, *pagination.PaginationResult, error)
}

func WithCacheM4Policies(groups CachePolicyGroupLookup) CachePolicyOption {
	return func(s *CachePolicyService) {
		s.m4Available = true
		s.groups = groups
	}
}

// CacheM4BuildCapabilities describes implemented behavior, independently of
// administrator storage/API support. Zero values keep T4.0 storage-only.
type CacheM4BuildCapabilities struct {
	L04 bool
	L03 bool
	L07 bool
	// L09A is the prev_host affinity (T3.1a). prev_host+quality is L-10 and
	// has no capability yet.
	L09A bool
}

func WithCacheM4BuildCapabilities(capabilities CacheM4BuildCapabilities) CachePolicyOption {
	// Metadata behavior derives its candidate identity from L-04.
	capabilities.L03 = capabilities.L03 && capabilities.L04
	return func(s *CachePolicyService) { s.m4Behaviors = capabilities }
}

// This is a build capability, not a runtime setting or request-time probe.
func WithCacheAnthropicObservability() CachePolicyOption {
	return func(s *CachePolicyService) { s.anthropicObservabilityAvailable = true }
}

func m4PolicyDefaults(index int) map[string]any {
	if index == 4 {
		return map[string]any{
			"anchor_group_ids": []int64{}, "gemini_anchor": false,
			"metadata_group_ids": []int64{}, "metadata_account_ids": []int64{}, "rollout_percent": 0,
			"metadata_rollout_percent": 0, "max_inflight_per_anchor": 2,
		}
	}
	busy := map[string]any{"enabled": false, "group_ids": []int64{}, "rollout_percent": 0}
	cacheL07AddDefaults(busy)
	return map[string]any{"keep_sticky_on_busy": busy, "affinity_mode": cacheAffinityScore, "affinity_rollout_percent": 0}
}

func m4PolicyField(spec map[string]any, field string) any {
	parent, child, nested := strings.Cut(field, ".")
	if !nested {
		return spec[field]
	}
	object, _ := spec[parent].(map[string]any)
	return object[child]
}

func setM4PolicyField(spec map[string]any, field string, value any) {
	parent, child, nested := strings.Cut(field, ".")
	if !nested {
		spec[field] = value
		return
	}
	object, _ := spec[parent].(map[string]any)
	object[child] = value
}

func mergeM4Policy(index int, before map[string]any, raw json.RawMessage, caps cacheL07Caps, writing bool) (map[string]any, []CachePolicyDiff, []CachePolicyFieldError) {
	out := make(map[string]any, len(before))
	for field, value := range before {
		out[field] = cloneCachePolicyValue(value)
	}
	diff := []CachePolicyDiff{}
	errs := []CachePolicyFieldError{}
	var patch map[string]json.RawMessage
	if json.Unmarshal(raw, &patch) != nil || patch == nil {
		return out, diff, append(errs, CachePolicyFieldError{Field: "", Code: "malformed_body"})
	}
	leaves := make(map[string]json.RawMessage, len(patch))
	topFields := make([]string, 0, len(patch))
	for field := range patch {
		topFields = append(topFields, field)
	}
	sort.Strings(topFields)
	for _, field := range topFields {
		value := patch[field]
		// Dot paths describe schema/diff/errors, not an alternate wire shape.
		// Reject them so a nested object and a dotted key cannot compete.
		if strings.Contains(field, ".") {
			errs = append(errs, CachePolicyFieldError{Field: field, Code: "unknown_field"})
			continue
		}
		if index == 5 && field == "keep_sticky_on_busy" {
			var nested map[string]json.RawMessage
			if json.Unmarshal(value, &nested) != nil || nested == nil {
				errs = append(errs, CachePolicyFieldError{Field: field, Code: "malformed_body"})
				continue
			}
			for child, item := range nested {
				leaves[field+"."+child] = item
			}
		} else {
			leaves[field] = value
		}
	}
	names := make([]string, 0, len(leaves))
	for field := range leaves {
		names = append(names, field)
	}
	sort.Strings(names)
	defaults := m4PolicyDefaults(index)
	for _, field := range names {
		value := leaves[field]
		def := m4PolicyField(defaults, field)
		code := ""
		var next any
		if def == nil {
			code = "unknown_field"
			if field == "normalize_client_pck" {
				code = "forbidden_field"
			}
		} else if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			code = "malformed_body"
		} else {
			switch def.(type) {
			case bool:
				var v bool
				if json.Unmarshal(value, &v) != nil {
					code = "malformed_body"
				}
				next = v
			case int:
				var v int
				if json.Unmarshal(value, &v) != nil {
					code = "malformed_body"
				} else if fieldErr := cacheM4IntError(index, field, v, caps, writing); fieldErr != nil {
					errs = append(errs, *fieldErr)
					continue
				}
				next = v
			case string:
				var v string
				if json.Unmarshal(value, &v) != nil {
					code = "malformed_body"
				} else if v != cacheAffinityScore && v != cacheAffinityPrevHost {
					// prev_host+quality needs L-10; prev_host is checked
					// against the build capability with the other changes.
					code = "unsupported_in_build"
				}
				next = v
			case []int64:
				var ids []int64
				if json.Unmarshal(value, &ids) != nil {
					code = "malformed_body"
				} else {
					seen := make(map[int64]bool, len(ids))
					for _, id := range ids {
						if id <= 0 {
							code = "unknown_group"
							if field == "metadata_account_ids" {
								code = "unknown_account"
							}
							break
						}
						if seen[id] {
							code = "duplicate"
							break
						}
						seen[id] = true
					}
				}
				next = ids
			default:
				code = "unknown_field"
			}
		}
		if code != "" {
			errs = append(errs, CachePolicyFieldError{Field: field, Code: code})
			continue
		}
		setM4PolicyField(out, field, next)
		if old := m4PolicyField(before, field); !reflect.DeepEqual(old, next) {
			diff = append(diff, CachePolicyDiff{field, cloneCachePolicyValue(old), cloneCachePolicyValue(next)})
		}
	}
	if index == 4 {
		if policyInt(out["metadata_rollout_percent"]) > policyInt(out["rollout_percent"]) {
			errs = append(errs, CachePolicyFieldError{Field: "metadata_rollout_percent", Code: "exceeds_rollout_percent", Limit: map[string]int{"max": policyInt(out["rollout_percent"])}})
		}
		anchors, _ := out["anchor_group_ids"].([]int64)
		metadata, _ := out["metadata_group_ids"].([]int64)
		members := make(map[int64]bool, len(anchors))
		for _, id := range anchors {
			members[id] = true
		}
		for _, id := range metadata {
			if !members[id] {
				errs = append(errs, CachePolicyFieldError{Field: "metadata_group_ids", Code: "not_subset_of_anchor_groups"})
				break
			}
		}
	}
	// A leaf that fails parsing or its own value limit never participates in
	// direction or confirmation. Reference and relational errors (for example
	// not_subset_of_anchor_groups) keep the field in the diff (m3m4 v1.2).
	for _, err := range errs {
		if cachePolicyErrorDropsDiff(err.Code) {
			diff = slices.DeleteFunc(diff, func(d CachePolicyDiff) bool { return d.Field == err.Field })
		}
	}
	return out, diff, errs
}

func cachePolicyErrorDropsDiff(code string) bool {
	switch code {
	case "unsupported_in_build", "forbidden_field", "duplicate", "malformed_body", "unknown_field", "exceeds_rollout_percent", "exceeds_chain_percent":
		return true
	}
	return false
}

func m4PolicyIntRange(field string) (int, int) {
	if field == "max_inflight_per_anchor" {
		return 1, 16
	}
	return 0, 100
}

// Unlike account_ids and busy scope, an empty anchor/metadata list is disabled.
func cachePolicyExplicitScopeDirection(before, after []int64) (up, down bool) {
	old := make(map[int64]bool, len(before))
	for _, id := range before {
		old[id] = true
	}
	for _, id := range after {
		if !old[id] {
			up = true
		}
		delete(old, id)
	}
	return up, len(old) != 0
}

// Capability checks use actual leaf changes, rather than merely inspecting the
// resulting configured value. A rollback must remain possible after downgrade.
func (s *CachePolicyService) m4UnsupportedChanges(index int, spec map[string]any, diff []CachePolicyDiff) []CachePolicyFieldError {
	errs := []CachePolicyFieldError{}
	if reflect.DeepEqual(spec, m4PolicyDefaults(index)) {
		return errs
	}
	for _, change := range diff {
		if policyDirection([]CachePolicyDiff{change}) == "disable" {
			continue
		}
		if reflect.DeepEqual(change.After, m4PolicyField(m4PolicyDefaults(index), change.Field)) {
			continue
		}
		unsupported := false
		switch change.Field {
		case "anchor_group_ids", "gemini_anchor":
			unsupported = !s.m4Behaviors.L04
		case "metadata_group_ids", "metadata_account_ids":
			unsupported = !s.m4Behaviors.L03
		case "keep_sticky_on_busy.enabled", "keep_sticky_on_busy.group_ids":
			unsupported = !s.m4Behaviors.L07
		case "affinity_mode":
			unsupported = !s.m4Behaviors.L09A
		}
		if unsupported {
			errs = append(errs, CachePolicyFieldError{Field: change.Field, Code: "unsupported_in_build"})
		}
	}
	return errs
}

func (s *CachePolicyService) m4ConfiguredCapabilities(index int, spec map[string]any) (implemented, unimplemented bool) {
	if index == 4 {
		anchors, _ := spec["anchor_group_ids"].([]int64)
		metadata, _ := spec["metadata_group_ids"].([]int64)
		gemini, _ := spec["gemini_anchor"].(bool)
		anchor := len(anchors) > 0 || gemini
		accounts, _ := spec["metadata_account_ids"].([]int64)
		meta := len(metadata) > 0 || len(accounts) > 0
		return (s.m4Behaviors.L04 && anchor) || (s.m4Behaviors.L03 && meta), (!s.m4Behaviors.L04 && anchor) || (!s.m4Behaviors.L03 && meta)
	}
	enabled, _ := m4PolicyField(spec, "keep_sticky_on_busy.enabled").(bool)
	affinity := cacheAffinityMode(spec) != cacheAffinityScore
	return (enabled && s.m4Behaviors.L07) || (affinity && s.m4Behaviors.L09A), (enabled && !s.m4Behaviors.L07) || (affinity && !s.m4Behaviors.L09A)
}

func m4PolicyConfigured(index int, spec map[string]any) (configured bool, rollout int) {
	if index == 4 {
		ids, _ := spec["anchor_group_ids"].([]int64)
		gemini, _ := spec["gemini_anchor"].(bool)
		return len(ids) > 0 || gemini, policyInt(spec["rollout_percent"])
	}
	enabled, _ := m4PolicyField(spec, "keep_sticky_on_busy.enabled").(bool)
	return enabled, policyInt(m4PolicyField(spec, "keep_sticky_on_busy.rollout_percent"))
}

func (s *CachePolicyService) m4Effective(index int, stored, loaded cachePolicyEntry, p *CachePolicySnapshot) CachePolicyEffective {
	active := false
	e := CachePolicyEffective{Active: &active, Blockers: []string{}, Warnings: []string{}, ClippedFields: []string{}}
	masterInvalid := p != nil && p.entries[0].parseError
	invalid := stored.parseError || loaded.parseError || masterInvalid
	configured, rollout := m4PolicyConfigured(index, loaded.envelope.Spec)
	implemented, unimplemented := s.m4ConfiguredCapabilities(index, loaded.envelope.Spec)
	enabled := implemented && rollout > 0
	// The process guard (A5) pauses L-07 only where it would otherwise run.
	guard := false
	staticBlocked := false
	if index == 5 {
		// Routing carries L-07 and L-09a; it is on when either feature
		// takes effect (cache_policies l09a §1).
		spec := loaded.envelope.Spec
		busy, _ := m4PolicyField(spec, "keep_sticky_on_busy.enabled").(bool)
		l07 := busy && s.m4Behaviors.L07
		affinity := cacheAffinityMode(spec) != cacheAffinityScore
		staticBlocked = s.l09aStaticBlocked(spec)
		guard = l07 && s.L07GuardTripped()
		enabled = (l07 && rollout > 0 && !guard) ||
			(affinity && s.m4Behaviors.L09A && policyInt(spec["affinity_rollout_percent"]) > 0 && !staticBlocked)
	}
	blocked := (unimplemented && !implemented) || ((guard || staticBlocked) && !enabled)
	active = enabled && !invalid && !s.kill && p.MasterEnabled()
	if index == 5 {
		e.ClippedFields = s.cacheL07ClippedFields(loaded.envelope.Spec)
	}
	if s.kill {
		e.Blockers = append(e.Blockers, "kill_switch")
	}
	if invalid {
		e.Blockers = append(e.Blockers, "parse_error")
	}
	if staticBlocked {
		e.Blockers = append(e.Blockers, "static_aware_routing_disabled")
	}
	if unimplemented && !implemented {
		e.Blockers = append(e.Blockers, "not_implemented_in_build")
	}
	if guard {
		e.Blockers = append(e.Blockers, "guard_tripped")
	}
	switch {
	case s.kill:
		e.State = "killed"
	case invalid:
		e.State = "parse_error"
	case blocked:
		e.State = "blocked"
	case enabled && !p.MasterEnabled():
		e.State = "masked_by_master"
	case loaded.envelope.Rev < stored.envelope.Rev:
		e.State = "pending_reload"
	case active:
		e.State = "on"
	default:
		e.State = "off"
	}
	if index == 4 && !s.anthropicObservabilityAvailable && (e.State == "on" || e.State == "masked_by_master") {
		e.Warnings = append(e.Warnings, "anthropic_observability_missing")
	}
	if (configured && rollout == 0 || index == 5 && s.l09aRolloutZero(loaded.envelope.Spec)) && !invalid {
		e.Warnings = append(e.Warnings, "no_effect_rollout_zero")
	}
	if index == 5 && s.cacheL07ShareGroupSelected(stored.envelope.Spec) {
		e.Warnings = append(e.Warnings, "share_group_no_effect")
	}
	return e
}

func m4PolicySchema(index int, caps cacheL07Caps) []map[string]any {
	options := []cachePolicyAccountOption{}
	if index == 4 {
		return []map[string]any{
			{"field": "anchor_group_ids", "type": "id_list", "ref": "group", "options": options},
			{"field": "gemini_anchor", "type": "bool"},
			{"field": "metadata_group_ids", "type": "id_list", "ref": "group", "options": options},
			{"field": "metadata_account_ids", "type": "id_list", "ref": "account", "options": []cachePolicyAccountOption{}},
			{"field": "rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
			{"field": "metadata_rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
			{"field": "max_inflight_per_anchor", "type": "int", "min": 1, "max": 16},
		}
	}
	schema := []map[string]any{
		{"field": "keep_sticky_on_busy.enabled", "type": "bool"},
		{"field": "keep_sticky_on_busy.group_ids", "type": "id_list", "ref": "group", "options": options},
		{"field": "keep_sticky_on_busy.rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
	}
	schema = append(schema, cacheL07Schema(caps)...)
	return append(schema, map[string]any{"field": "affinity_mode", "type": "enum", "options": []string{"score", "prev_host", "prev_host+quality"}},
		map[string]any{"field": "affinity_rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"})
}

func (s *CachePolicyService) m4Schema(index int) []map[string]any {
	schema := m4PolicySchema(index, s.l07Caps)
	for _, field := range schema {
		unsupported := false
		switch field["field"] {
		case "anchor_group_ids", "gemini_anchor":
			unsupported = !s.m4Behaviors.L04
		case "metadata_group_ids", "metadata_account_ids":
			unsupported = !s.m4Behaviors.L03
		case "keep_sticky_on_busy.enabled", "keep_sticky_on_busy.group_ids":
			unsupported = !s.m4Behaviors.L07
		case "affinity_mode":
			unsupported = !s.m4Behaviors.L09A
		}
		if unsupported {
			field["not_implemented_in_build"] = true
		}
	}
	return schema
}

func m4PolicyGroupFields(index int) []string {
	if index == 4 {
		return []string{"anchor_group_ids", "metadata_group_ids"}
	}
	return []string{"keep_sticky_on_busy.group_ids"}
}

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
	return map[string]any{
		"keep_sticky_on_busy": map[string]any{"enabled": false, "group_ids": []int64{}, "rollout_percent": 0},
		"affinity_mode":       "score",
	}
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

func mergeM4Policy(index int, before map[string]any, raw json.RawMessage) (map[string]any, []CachePolicyDiff, []CachePolicyFieldError) {
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
				} else if lo, hi := m4PolicyIntRange(field); v < lo || v > hi {
					errs = append(errs, CachePolicyFieldError{Field: field, Code: "out_of_range", Limit: map[string]int{"min": lo, "max": hi}})
					continue
				}
				next = v
			case string:
				var v string
				if json.Unmarshal(value, &v) != nil {
					code = "malformed_body"
				} else if v != "score" {
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
	// Invalid leaves never participate in direction or confirmation.
	for _, err := range errs {
		diff = slices.DeleteFunc(diff, func(d CachePolicyDiff) bool { return d.Field == err.Field })
	}
	return out, diff, errs
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
	return enabled && s.m4Behaviors.L07, enabled && !s.m4Behaviors.L07
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
	blocked := unimplemented && !implemented
	enabled := implemented && rollout > 0
	active = enabled && !invalid && !s.kill && p.MasterEnabled()
	if s.kill {
		e.Blockers = append(e.Blockers, "kill_switch")
	}
	if invalid {
		e.Blockers = append(e.Blockers, "parse_error")
	}
	if blocked {
		e.Blockers = append(e.Blockers, "not_implemented_in_build")
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
	if configured && rollout == 0 && !invalid {
		e.Warnings = append(e.Warnings, "no_effect_rollout_zero")
	}
	return e
}

func m4PolicySchema(index int) []map[string]any {
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
	return []map[string]any{
		{"field": "keep_sticky_on_busy.enabled", "type": "bool"},
		{"field": "keep_sticky_on_busy.group_ids", "type": "id_list", "ref": "group", "options": options},
		{"field": "keep_sticky_on_busy.rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
		{"field": "affinity_mode", "type": "enum", "options": []string{"score", "prev_host", "prev_host+quality"}},
	}
}

func (s *CachePolicyService) m4Schema(index int) []map[string]any {
	schema := m4PolicySchema(index)
	for _, field := range schema {
		unsupported := false
		switch field["field"] {
		case "anchor_group_ids", "gemini_anchor":
			unsupported = !s.m4Behaviors.L04
		case "metadata_group_ids", "metadata_account_ids":
			unsupported = !s.m4Behaviors.L03
		case "keep_sticky_on_busy.enabled", "keep_sticky_on_busy.group_ids":
			unsupported = !s.m4Behaviors.L07
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

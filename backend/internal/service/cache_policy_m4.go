package service

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
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

// This is a build capability, not a runtime setting or request-time probe.
func WithCacheAnthropicObservability() CachePolicyOption {
	return func(s *CachePolicyService) { s.anthropicObservabilityAvailable = true }
}

func m4PolicyDefaults(index int) map[string]any {
	if index == 4 {
		return map[string]any{
			"anchor_group_ids": []int64{}, "gemini_anchor": false,
			"metadata_group_ids": []int64{}, "rollout_percent": 0,
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
				} else if v < 0 || v > 100 {
					errs = append(errs, CachePolicyFieldError{Field: field, Code: "out_of_range", Limit: map[string]int{"min": 0, "max": 100}})
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
	return out, diff, errs
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
	enabled := configured && rollout > 0
	active = enabled && !invalid && !s.kill && p.MasterEnabled()
	if s.kill {
		e.Blockers = append(e.Blockers, "kill_switch")
	}
	if invalid {
		e.Blockers = append(e.Blockers, "parse_error")
	}
	switch {
	case s.kill:
		e.State = "killed"
	case invalid:
		e.State = "parse_error"
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
			{"field": "rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
		}
	}
	return []map[string]any{
		{"field": "keep_sticky_on_busy.enabled", "type": "bool"},
		{"field": "keep_sticky_on_busy.group_ids", "type": "id_list", "ref": "group", "options": options},
		{"field": "keep_sticky_on_busy.rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
		{"field": "affinity_mode", "type": "enum", "options": []string{"score", "prev_host", "prev_host+quality"}},
	}
}

func m4PolicyGroupFields(index int) []string {
	if index == 4 {
		return []string{"anchor_group_ids", "metadata_group_ids"}
	}
	return []string{"keep_sticky_on_busy.group_ids"}
}

package service

import (
	"context"
	"errors"
	"sort"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
)

const cachePolicyGroupOptionsUnavailable = "group_options_unavailable"

var errCachePolicyGroupsUnavailable = errors.New("cache policy group lookup unavailable")

// This catalog lives only for one administrator operation. It is never cached,
// copied to a request snapshot, or consulted by the forwarding/scheduling path.
type cachePolicyGroupCatalog struct {
	groups   map[int64]Group
	fallback map[int64]bool
	options  []cachePolicyAccountOption
}

func (s *CachePolicyService) cachePolicyGroups(ctx context.Context) (*cachePolicyGroupCatalog, error) {
	if s.groups == nil {
		return nil, errCachePolicyGroupsUnavailable
	}
	catalog := &cachePolicyGroupCatalog{
		groups: map[int64]Group{}, fallback: map[int64]bool{}, options: []cachePolicyAccountOption{},
	}
	for page := 1; ; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: 500, SortBy: "id", SortOrder: pagination.SortOrderAsc}
		// A target can be referenced by any group's availability fallback.
		// Read all platforms for that relation; only Anthropic groups qualify.
		rows, _, err := s.groups.ListWithFilters(ctx, params, "", "", "", nil)
		if err != nil {
			return nil, errCachePolicyGroupsUnavailable
		}
		added := false
		for _, group := range rows {
			if group.ID <= 0 {
				continue
			}
			if _, seen := catalog.groups[group.ID]; seen {
				continue
			}
			added = true
			catalog.groups[group.ID] = group
			if group.AvailabilityFallbackGroupID != nil {
				catalog.fallback[*group.AvailabilityFallbackGroupID] = true
			}
		}
		if len(rows) < params.PageSize {
			break
		}
		if !added {
			// A broken/repeating page is an incomplete catalog, not an empty one.
			return nil, errCachePolicyGroupsUnavailable
		}
	}
	ids := make([]int64, 0, len(catalog.groups))
	for id, group := range catalog.groups {
		if group.Platform == PlatformAnthropic {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids[:min(len(ids), 500)] {
		group := catalog.groups[id]
		catalog.options = append(catalog.options, cachePolicyAccountOption{ID: id, Name: group.Name, Platform: group.Platform})
	}
	return catalog, nil
}

func setCachePolicyGroupOptions(view *CachePolicyView, options []cachePolicyAccountOption) {
	for _, field := range view.Schema {
		switch field["field"] {
		case "anchor_group_ids", "metadata_group_ids", "keep_sticky_on_busy.group_ids":
			// Each field owns a separate slice, including the empty case.
			field["options"] = append([]cachePolicyAccountOption{}, options...)
		}
	}
}

func setCachePolicyUnavailableGroupOptions(view *CachePolicyView) {
	setCachePolicyGroupOptions(view, []cachePolicyAccountOption{})
	view.Effective.Warnings = append(view.Effective.Warnings, cachePolicyGroupOptionsUnavailable)
}

func cachePolicyHasFallbackGroup(index int, spec map[string]any, catalog *cachePolicyGroupCatalog) bool {
	for _, field := range m4PolicyGroupFields(index) {
		ids, _ := m4PolicyField(spec, field).([]int64)
		for _, id := range ids {
			if catalog.fallback[id] {
				return true
			}
		}
	}
	return false
}

// Caller supplies only M4 views. One catalog covers both cards in a List.
func (s *CachePolicyService) populateCachePolicyGroupOptions(ctx context.Context, views ...*CachePolicyView) {
	if len(views) == 0 {
		return
	}
	catalog, err := s.cachePolicyGroups(ctx)
	for _, view := range views {
		if err != nil {
			setCachePolicyUnavailableGroupOptions(view)
			continue
		}
		setCachePolicyGroupOptions(view, catalog.options)
		if view.Stored != nil && cachePolicyHasFallbackGroup(policyIndex(view.Key), view.Stored.Spec, catalog) {
			view.Effective.Warnings = cachePolicyM4FallbackWarning(view.Effective.Warnings)
		}
	}
}

func (s *CachePolicyService) validateM4Groups(ctx context.Context, index int, v CachePolicyValidation, spec map[string]any) (CachePolicyValidation, error) {
	// In particular, do not consult previously fetched group facts on disable.
	if !v.Valid || v.Direction == "disable" {
		return v, nil
	}
	fields := m4PolicyGroupFields(index)
	hasIDs := false
	for _, field := range fields {
		ids, _ := m4PolicyField(spec, field).([]int64)
		hasIDs = hasIDs || len(ids) > 0
	}
	if !hasIDs {
		return v, nil
	}
	catalog, err := s.cachePolicyGroups(ctx)
	if err != nil {
		return v, err
	}
	for _, field := range fields {
		ids, _ := m4PolicyField(spec, field).([]int64)
		for _, id := range ids {
			group, found := catalog.groups[id]
			if !found {
				v.Errors = append(v.Errors, CachePolicyFieldError{Field: field, Code: "unknown_group"})
			} else if group.Platform != PlatformAnthropic {
				v.Errors = append(v.Errors, CachePolicyFieldError{Field: field, Code: "wrong_platform"})
			}
		}
	}
	v.Valid = len(v.Errors) == 0
	if !v.Valid {
		v.Warnings = []string{}
		v.EffectivePreview = nil
		v.Affected = []CachePolicyAffected{}
	} else if cachePolicyHasFallbackGroup(index, spec, catalog) {
		v.Warnings = cachePolicyM4FallbackWarning(v.Warnings)
	}
	return v, nil
}

// Warnings that follow fallback_group_selected in the contract enum order.
var cachePolicyWarningsAfterFallback = map[string]bool{
	"no_effect_rollout_zero": true, "high_rollout_without_experiment": true, "decision_pending_header_policy": true,
	"decision_pending_fallback_scope": true, "decision_pending_static_routing": true, "share_group_no_effect": true,
	"account_options_unavailable": true, cachePolicyGroupOptionsUnavailable: true,
}

// Preserve the contract enum order even when rollout=0 or a share-active
// busy group also produces a warning.
func cachePolicyM4FallbackWarning(warnings []string) []string {
	out := make([]string, 0, len(warnings)+1)
	added := false
	for _, warning := range warnings {
		if !added && cachePolicyWarningsAfterFallback[warning] {
			out = append(out, "fallback_group_selected")
			added = true
		}
		out = append(out, warning)
	}
	if !added {
		out = append(out, "fallback_group_selected")
	}
	return out
}

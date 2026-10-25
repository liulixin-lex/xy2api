package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Build capabilities are explicit; production enables northstar alongside L-01.
func (s *CachePolicyService) cachePolicySupported(index int) bool {
	switch index {
	case 0, 1, 3:
		return true
	case 2:
		return s.northstarAvailable
	case 4, 5:
		return s.m4Available
	default:
		return false
	}
}

// CachePolicyAccountLookup is used only by administrator validation and writes.
// Request snapshots never query accounts or read/write scheduling state.
type CachePolicyAccountLookup interface {
	GetByIDs(context.Context, []int64) ([]*Account, error)
}

func WithCachePolicyAccounts(accounts CachePolicyAccountLookup) CachePolicyOption {
	return func(s *CachePolicyService) { s.accounts = accounts }
}

// CacheUpstreamIdentityPolicy is a typed, immutable request policy. Its private
// allowlist is built once at publication and is never returned to callers.
// Enabled includes the kill switch, master, mode and rollout guards. It does
// not replace the forwarding layer's account/protocol/client-identity checks.
type CacheUpstreamIdentityPolicy struct {
	Mode                  string
	RolloutPercent        int
	ChatCompatUUID        bool
	HeaderOnlyUUID        bool
	ForwardSessionHeaders string
	MaxInflightPerKey     int
	active                bool
	revision              int64
	accounts              map[int64]struct{}
}

func (p *CachePolicySnapshot) UpstreamIdentity() CacheUpstreamIdentityPolicy {
	if p == nil {
		return CacheUpstreamIdentityPolicy{Mode: "off", ForwardSessionHeaders: "whitelist", MaxInflightPerKey: 2}
	}
	return p.upstream
}
func (p CacheUpstreamIdentityPolicy) Enabled() bool   { return p.active }
func (p CacheUpstreamIdentityPolicy) Revision() int64 { return p.revision }

// AllowsAccount tests the configured scope only. Empty means all eligible
// accounts; T1.2 must still require an OpenAI apikey serving account.
func (p CacheUpstreamIdentityPolicy) AllowsAccount(id int64) bool {
	if len(p.accounts) == 0 {
		return true
	}
	_, ok := p.accounts[id]
	return ok
}

func upstreamIdentityFromEntry(entry cachePolicyEntry, master bool) CacheUpstreamIdentityPolicy {
	spec := entry.envelope.Spec
	p := CacheUpstreamIdentityPolicy{revision: entry.envelope.Rev}
	p.Mode, _ = spec["mode"].(string)
	p.ForwardSessionHeaders, _ = spec["forward_session_headers"].(string)
	p.RolloutPercent = policyInt(spec["rollout_percent"])
	p.MaxInflightPerKey = policyInt(spec["max_inflight_per_key"])
	p.ChatCompatUUID, _ = spec["chat_compat_uuid"].(bool)
	p.HeaderOnlyUUID, _ = spec["header_only_uuid"].(bool)
	if ids, ok := spec["account_ids"].([]int64); ok && len(ids) > 0 {
		p.accounts = make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			p.accounts[id] = struct{}{}
		}
	}
	p.active = master && !entry.parseError && p.Mode == "uuid_fill" && p.RolloutPercent > 0
	return p
}

func mergeUpstreamIdentity(before map[string]any, raw json.RawMessage) (map[string]any, []CachePolicyDiff, []CachePolicyFieldError) {
	out := make(map[string]any, len(before))
	for k, value := range before {
		out[k] = cloneCachePolicyValue(value)
	}
	diff := []CachePolicyDiff{}
	errs := []CachePolicyFieldError{}
	var patch map[string]json.RawMessage
	if json.Unmarshal(raw, &patch) != nil || patch == nil {
		return out, diff, append(errs, CachePolicyFieldError{Field: "", Code: "malformed_body"})
	}
	fields := make([]string, 0, len(patch))
	for field := range patch {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	defaults := policyDefaults(3)
	for _, field := range fields {
		value := patch[field]
		var next any
		code := ""
		if _, known := defaults[field]; !known {
			code = "unknown_field"
			if field == "normalize_client_pck" {
				code = "forbidden_field"
			}
		} else if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			code = "malformed_body"
		} else {
			switch field {
			case "mode", "forward_session_headers":
				var str string
				if json.Unmarshal(value, &str) != nil {
					code = "malformed_body"
				} else if field == "mode" && str != "off" && str != "uuid_fill" {
					code = "malformed_body"
				} else if field == "forward_session_headers" && str != "whitelist" && str != "normalize" {
					code = "malformed_body"
				} else if str == "normalize" {
					code = "unsupported_in_build"
				}
				next = str
			case "chat_compat_uuid", "header_only_uuid":
				var flag bool
				if json.Unmarshal(value, &flag) != nil {
					code = "malformed_body"
				} else if field == "header_only_uuid" && flag {
					code = "unsupported_in_build"
				}
				next = flag
			case "rollout_percent", "max_inflight_per_key":
				var percent int
				min, max := 0, 100
				if field == "max_inflight_per_key" {
					min, max = 1, 16
				}
				if json.Unmarshal(value, &percent) != nil {
					code = "malformed_body"
				} else if percent < min || percent > max {
					errs = append(errs, CachePolicyFieldError{Field: field, Code: "out_of_range", Limit: map[string]int{"min": min, "max": max}})
					continue
				}
				next = percent
			case "account_ids":
				var ids []int64
				if json.Unmarshal(value, &ids) != nil {
					code = "malformed_body"
				} else {
					seen := make(map[int64]bool, len(ids))
					for _, id := range ids {
						if id <= 0 {
							code = "unknown_account"
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
				if field == "normalize_client_pck" {
					code = "forbidden_field"
				}
			}
		}
		if code != "" {
			errs = append(errs, CachePolicyFieldError{Field: field, Code: code})
			continue
		}
		out[field] = next
		if !reflect.DeepEqual(before[field], next) {
			diff = append(diff, CachePolicyDiff{field, cloneCachePolicyValue(before[field]), cloneCachePolicyValue(next)})
		}
	}
	if out["chat_compat_uuid"] == true && out["mode"] != "uuid_fill" {
		errs = append(errs, CachePolicyFieldError{Field: "chat_compat_uuid", Code: "requires_mode_uuid_fill"})
	}
	return out, diff, errs
}

// Empty scope means all eligible accounts. A narrowing change is a rollback;
// adding any account requires high-risk confirmation even when others are removed.
func upstreamIdentityScopeDirection(before, after []int64) (up, down bool) {
	if len(before) == 0 {
		return false, len(after) != 0
	}
	if len(after) == 0 {
		return true, false
	}
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

func (s *CachePolicyService) validationForWrite(ctx context.Context, index int, entry cachePolicyEntry, w CachePolicyWrite, bodyErrors []CachePolicyFieldError) (CachePolicyValidation, map[string]any, error) {
	v, spec := s.validation(index, entry, w, bodyErrors)
	if index == 4 || index == 5 {
		validated, err := s.validateM4Groups(ctx, index, v, spec)
		if err != nil || index != 4 {
			return validated, spec, err
		}
		validated, err = s.validateCacheMetadataAccounts(ctx, validated)
		return validated, spec, err
	}
	// Rollbacks must work even when the account repository is unavailable.
	// Direction comes from actual, strictly parsed changes: disable cannot
	// grant any new scope, and still retains field and revision validation.
	if index != 3 || !v.Valid || v.Direction == "disable" {
		return v, spec, nil
	}
	ids, _ := spec["account_ids"].([]int64)
	if len(ids) == 0 {
		return v, spec, nil
	}
	if s.accounts == nil {
		return v, spec, fmt.Errorf("cache policy account validation unavailable")
	}
	accounts, err := s.accounts.GetByIDs(ctx, ids)
	if err != nil {
		return v, spec, fmt.Errorf("validate cache policy accounts: %w", err)
	}
	found := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			found[account.ID] = account
		}
	}
	for _, id := range ids {
		account := found[id]
		if account == nil {
			v.Errors = append(v.Errors, CachePolicyFieldError{Field: "account_ids", Code: "unknown_account"})
		} else if !account.IsOpenAIApiKey() {
			// The shared contract uses wrong_platform for ineligible accounts;
			// this also excludes OpenAI OAuth/setup-token types.
			v.Errors = append(v.Errors, CachePolicyFieldError{Field: "account_ids", Code: "wrong_platform"})
		}
	}
	v.Valid = len(v.Errors) == 0
	if !v.Valid {
		// Match syntactic validation: invalid policies have no effective warnings.
		v.Warnings = []string{}
		v.EffectivePreview = nil
		v.Affected = []CachePolicyAffected{}
	}
	return v, spec, nil
}

func upstreamIdentitySchema() []map[string]any {
	return []map[string]any{
		{"field": "mode", "type": "enum", "options": []string{"off", "uuid_fill"}},
		{"field": "account_ids", "type": "id_list", "ref": "account", "options": []cachePolicyAccountOption{}},
		{"field": "rollout_percent", "type": "int", "min": 0, "max": 100, "unit": "percent"},
		{"field": "chat_compat_uuid", "type": "bool"},
		{"field": "header_only_uuid", "type": "bool"},
		{"field": "forward_session_headers", "type": "enum", "options": []string{"whitelist", "normalize"}},
		{"field": "max_inflight_per_key", "type": "int", "min": 1, "max": 16},
	}
}

func (s *CachePolicyService) upstreamIdentityEffective(stored, loaded cachePolicyEntry, p *CachePolicySnapshot) CachePolicyEffective {
	active := false
	e := CachePolicyEffective{Active: &active, Blockers: []string{}, Warnings: []string{}, ClippedFields: []string{}}
	masterInvalid := p != nil && p.entries[0].parseError
	invalid := loaded.parseError || stored.parseError || masterInvalid
	configured := loaded.envelope.Spec["mode"] == "uuid_fill"
	master := p.MasterEnabled()
	active = !s.kill && !loaded.parseError && !masterInvalid && master && configured && policyInt(loaded.envelope.Spec["rollout_percent"]) > 0
	if s.kill {
		e.Blockers = append(e.Blockers, "kill_switch")
	}
	if invalid {
		e.Blockers = append(e.Blockers, "parse_error")
	}
	if configured {
		if policyInt(loaded.envelope.Spec["rollout_percent"]) == 0 {
			e.Warnings = append(e.Warnings, "no_effect_rollout_zero")
		}
		if !p.TraceEnabled() {
			e.Warnings = append(e.Warnings, "observability_off")
		}
	}
	switch {
	case s.kill:
		e.State = "killed"
	case invalid:
		e.State = "parse_error"
	case configured && !master:
		e.State = "masked_by_master"
	case loaded.envelope.Rev < stored.envelope.Rev:
		e.State = "pending_reload"
	case active:
		e.State = "on"
	default:
		e.State = "off"
	}
	ids, _ := stored.envelope.Spec["account_ids"].([]int64)
	if stored.envelope.Spec["mode"] == "uuid_fill" && len(ids) == 0 {
		e.Warnings = append(e.Warnings, "l01_all_accounts_unverified")
	}
	return e
}

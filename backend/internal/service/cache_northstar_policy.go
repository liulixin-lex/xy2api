package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

type CacheNorthstarThresholdRule struct {
	Match     string `json:"match"`
	Model     string `json:"model"`
	Threshold int    `json:"threshold"`
}

// The policy is copied once by the rollup/query worker. The request snapshot
// itself remains immutable and its enabled check is an atomic load only.
type CacheNorthstarPolicy struct {
	RollupEnabled    bool                          `json:"rollup_enabled"`
	BackfillDays     int                           `json:"backfill_days"`
	PolicyVersion    string                        `json:"policy_version"`
	ExcludedGroupIDs []int64                       `json:"excluded_group_ids"`
	ExcludeMedia     bool                          `json:"exclude_media"`
	ModelThresholds  []CacheNorthstarThresholdRule `json:"model_thresholds"`
	UnknownThreshold string                        `json:"unknown_threshold"`
}

func (p CacheNorthstarPolicy) Threshold(model string) (int, bool) {
	for _, rule := range p.ModelThresholds {
		if rule.Match == "exact" && model == rule.Model {
			return rule.Threshold, true
		}
	}
	// Longest matching prefix wins; validation forbids duplicate match/model.
	length, threshold := -1, 0
	for _, rule := range p.ModelThresholds {
		if rule.Match == "prefix" && len(rule.Model) > length && strings.HasPrefix(model, rule.Model) {
			length, threshold = len(rule.Model), rule.Threshold
		}
	}
	return threshold, length >= 0
}
func (p CacheNorthstarPolicy) Business(row CacheNorthstarRow) bool {
	if row.PromptTokens <= 0 || p.ExcludeMedia && row.HasMedia {
		return false
	}
	for _, id := range p.ExcludedGroupIDs {
		if id == row.GroupID {
			return false
		}
	}
	return true
}
func (p CacheNorthstarPolicy) Strict(row CacheNorthstarRow) bool {
	threshold, known := p.Threshold(row.Model)
	return p.Business(row) && known && row.ThresholdBand >= threshold
}

func northstarPolicyDefaults() map[string]any {
	rules := []CacheNorthstarThresholdRule{
		{Match: "prefix", Model: "gpt-", Threshold: 1024},
		{Match: "prefix", Model: "chatgpt-", Threshold: 1024},
		{Match: "prefix", Model: "codex-", Threshold: 1024},
		{Match: "prefix", Model: "o1", Threshold: 1024},
		{Match: "prefix", Model: "o3", Threshold: 1024},
		{Match: "prefix", Model: "o4", Threshold: 1024},
	}
	official := map[string]int{
		"claude-opus-5-5": 512, "claude-opus-5": 512, "claude-sonnet-5-5": 512,
		"claude-haiku-5-5": 512, "claude-fable-5-1": 512, "claude-fable-5": 512,
		"claude-mythos-5-1": 512, "claude-mythos-5": 512, "claude-opus-4-8": 1024,
		"claude-sonnet-5": 1024, "claude-sonnet-4-6": 1024, "claude-sonnet-4-5": 1024,
		"claude-opus-4-1": 1024, "claude-opus-4": 1024, "claude-opus-4-7": 2048,
		"claude-haiku-3-5": 2048, "claude-opus-4-6": 4096, "claude-opus-4-5": 4096,
		"claude-haiku-4-5": 4096, "gemini-3.8-flash": 4096, "gemini-3.6-flash": 4096,
		"gemini-3.1-pro-preview": 4096, "gemini-2.5-flash": 2048, "gemini-2.5-pro": 2048,
	}
	names := make([]string, 0, len(official))
	for name := range official {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rules = append(rules, CacheNorthstarThresholdRule{Match: "prefix", Model: name, Threshold: official[name]})
	}
	return map[string]any{"rollup_enabled": false, "backfill_days": 7, "policy_version": "2026-10-22-v1", "excluded_group_ids": []int64{}, "exclude_media": true, "model_thresholds": rules, "unknown_threshold": "aux_only"}
}

func parseNorthstarPolicy(spec map[string]any) CacheNorthstarPolicy {
	// Only called after strict validation; JSON normalizes array representations.
	raw, _ := json.Marshal(spec)
	var p CacheNorthstarPolicy
	_ = json.Unmarshal(raw, &p)
	return p
}

func mergeNorthstarPolicy(before map[string]any, raw json.RawMessage, writing bool) (map[string]any, []CachePolicyDiff, []CachePolicyFieldError) {
	out := make(map[string]any, len(before))
	for key, value := range before {
		out[key] = value
	}
	diff := []CachePolicyDiff{}
	errs := []CachePolicyFieldError{}
	var patch map[string]json.RawMessage
	if json.Unmarshal(raw, &patch) != nil || patch == nil {
		return out, diff, append(errs, CachePolicyFieldError{Field: "", Code: "malformed_body"})
	}
	names := make([]string, 0, len(patch))
	for key := range patch {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		value := patch[key]
		if _, ok := before[key]; !ok {
			errs = append(errs, CachePolicyFieldError{Field: key, Code: "unknown_field"})
			continue
		}
		var next any
		var err error
		switch key {
		case "rollup_enabled", "exclude_media":
			var v bool
			err = json.Unmarshal(value, &v)
			next = v
		case "backfill_days":
			var v int
			err = json.Unmarshal(value, &v)
			next = v
			if err == nil && (v < 0 || v > 30) {
				errs = append(errs, CachePolicyFieldError{Field: key, Code: "out_of_range", Limit: map[string]int{"min": 0, "max": 30}})
			}
		case "policy_version", "unknown_threshold":
			var v string
			err = json.Unmarshal(value, &v)
			next = v
			if err == nil && (strings.TrimSpace(v) != v || utf8.RuneCountInString(v) == 0 || len(v) > 100 || key == "unknown_threshold" && v != "aux_only") {
				err = fmt.Errorf("invalid value")
			}
		case "excluded_group_ids":
			var v []int64
			err = json.Unmarshal(value, &v)
			next = v
			seen := map[int64]bool{}
			for _, id := range v {
				if id <= 0 || seen[id] {
					err = fmt.Errorf("invalid group list")
				}
				seen[id] = true
			}
			if len(v) > 1000 {
				err = fmt.Errorf("too many groups")
			}
		case "model_thresholds":
			var v []CacheNorthstarThresholdRule
			decoder := json.NewDecoder(strings.NewReader(string(value)))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&v)
			next = v
			seen := map[string]bool{}
			for _, rule := range v {
				identity := rule.Match + "\x00" + rule.Model
				validThreshold := rule.Threshold == 512 || rule.Threshold == 1024 || rule.Threshold == 2048 || rule.Threshold == 4096 || rule.Threshold == 6144
				if (rule.Match != "exact" && rule.Match != "prefix") || strings.TrimSpace(rule.Model) != rule.Model || len(rule.Model) == 0 || len(rule.Model) > 100 || !validThreshold || seen[identity] {
					err = fmt.Errorf("invalid threshold rule")
				}
				seen[identity] = true
			}
			if len(v) > 256 {
				err = fmt.Errorf("too many rules")
			}
		}
		if err != nil || string(value) == "null" {
			errs = append(errs, CachePolicyFieldError{Field: key, Code: "malformed_body"})
			continue
		}
		out[key] = next
		oldJSON, _ := json.Marshal(before[key])
		newJSON, _ := json.Marshal(next)
		var oldValue, newValue any
		_ = json.Unmarshal(oldJSON, &oldValue)
		_ = json.Unmarshal(newJSON, &newValue)
		if !reflect.DeepEqual(oldValue, newValue) {
			diff = append(diff, CachePolicyDiff{Field: key, Before: before[key], After: next})
		}
	}
	if writing && out["policy_version"] == before["policy_version"] {
		for _, d := range diff {
			if d.Field != "rollup_enabled" && d.Field != "backfill_days" && d.Field != "policy_version" {
				errs = append(errs, CachePolicyFieldError{Field: "policy_version", Code: "policy_version_required"})
				break
			}
		}
	}
	return out, diff, errs
}

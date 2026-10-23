package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

type CachePolicyError struct {
	Status          int
	Reason, Message string
	Metadata        map[string]string
}

func (e *CachePolicyError) Error() string { return e.Message }
func PolicyNotInBuild(key string) error {
	return &CachePolicyError{404, "CACHE_POLICY_NOT_IN_BUILD", "cache policy not available in this build", map[string]string{"key": key}}
}

type CachePolicyFieldError struct {
	Field string         `json:"field"`
	Code  string         `json:"code"`
	Limit map[string]int `json:"limit,omitempty"`
}
type CachePolicyDiff struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}
type CachePolicyConfirm struct {
	ReasonRequired   bool    `json:"reason_required"`
	CheckboxRequired bool    `json:"checkbox_required"`
	TypeToConfirm    *string `json:"type_to_confirm"`
}
type CachePolicyWrite struct {
	ExpectedRev *int64          `json:"expected_rev"`
	Spec        json.RawMessage `json:"spec"`
	Reason      string          `json:"reason"`
	Confirm     struct {
		Checkbox bool    `json:"checkbox"`
		Typed    *string `json:"typed"`
	} `json:"confirm"`
}

// DecodeCachePolicyWrite is strict at the request envelope as well as the spec.
func DecodeCachePolicyWrite(r io.Reader) (CachePolicyWrite, []CachePolicyFieldError) {
	var w CachePolicyWrite
	if r == nil {
		return w, []CachePolicyFieldError{{Field: "", Code: "malformed_body"}}
	}
	raw, err := io.ReadAll(io.LimitReader(r, 64*1024+1))
	if err != nil || len(raw) > 64*1024 || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return w, []CachePolicyFieldError{{Field: "", Code: "malformed_body"}}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&w); err != nil || d.Decode(new(any)) != io.EOF {
		return w, []CachePolicyFieldError{{Field: "", Code: "malformed_body"}}
	}
	if w.ExpectedRev == nil || *w.ExpectedRev < 0 || len(w.Spec) == 0 {
		return w, []CachePolicyFieldError{{Field: "", Code: "malformed_body"}}
	}
	w.Reason = strings.TrimSpace(w.Reason)
	return w, nil
}

type CachePolicyPreview struct {
	State    string   `json:"state"`
	Active   *bool    `json:"active"`
	Blockers []string `json:"blockers"`
}
type CachePolicyAffected struct {
	Key         string `json:"key"`
	StateBefore string `json:"state_before"`
	StateAfter  string `json:"state_after"`
	ActiveAfter *bool  `json:"active_after"`
}
type CachePolicyValidation struct {
	Valid            bool                    `json:"valid"`
	Errors           []CachePolicyFieldError `json:"errors"`
	Warnings         []string                `json:"warnings"`
	Diff             []CachePolicyDiff       `json:"diff"`
	Risk             string                  `json:"risk"`
	Direction        string                  `json:"direction"`
	Confirm          CachePolicyConfirm      `json:"confirm"`
	CurrentRev       int64                   `json:"current_rev"`
	RevStale         bool                    `json:"rev_stale"`
	EffectivePreview *CachePolicyPreview     `json:"effective_preview"`
	Affected         []CachePolicyAffected   `json:"affected"`
}

func (s *CachePolicyService) merge(index int, before map[string]any, raw json.RawMessage, writing bool) (map[string]any, []CachePolicyDiff, []CachePolicyFieldError) {
	out := make(map[string]any, len(before))
	for k, v := range before {
		out[k] = v
	}
	diff := []CachePolicyDiff{}
	errs := []CachePolicyFieldError{}
	var patch map[string]json.RawMessage
	if json.Unmarshal(raw, &patch) != nil || patch == nil {
		return out, diff, append(errs, CachePolicyFieldError{Field: "", Code: "malformed_body"})
	}
	names := make([]string, 0, len(patch))
	for field := range patch {
		names = append(names, field)
	}
	sort.Strings(names)
	defaults := policyDefaults(index)
	for _, field := range names {
		value := patch[field]
		if _, ok := defaults[field]; !ok {
			code := "unknown_field"
			if field == "normalize_client_pck" {
				code = "forbidden_field"
			}
			if index == 1 {
				future := map[string]string{"platforms": `["openai"]`, "ingress_chain_percent": "10", "persist_trace_hourly": "true"}
				if expected, ok := future[field]; ok {
					var got, want any
					_ = json.Unmarshal(value, &got)
					_ = json.Unmarshal([]byte(expected), &want)
					if reflect.DeepEqual(got, want) {
						continue
					}
					code = "unsupported_in_build"
				}
			}
			errs = append(errs, CachePolicyFieldError{Field: field, Code: code})
			continue
		}
		var next any
		if field == "enabled" {
			var v bool
			if string(value) == "null" || json.Unmarshal(value, &v) != nil {
				errs = append(errs, CachePolicyFieldError{Field: field, Code: "malformed_body"})
				continue
			}
			next = v
		} else {
			var v int
			if string(value) == "null" || json.Unmarshal(value, &v) != nil {
				errs = append(errs, CachePolicyFieldError{Field: field, Code: "malformed_body"})
				continue
			}
			next = v
			lo, hi := 0, 100
			capped := false
			switch field {
			case "byte_budget_mib":
				lo = 8
				hi = s.caps.MaxByteBudgetMiB
				capped = true
			case "rate_mib_per_s":
				lo = 1
				hi = s.caps.MaxRateMiBPerS
				capped = true
			}
			if v < lo || (!capped && v > hi) {
				errs = append(errs, CachePolicyFieldError{Field: field, Code: "out_of_range", Limit: map[string]int{"min": lo, "max": hi}})
			} else if capped && writing && v > hi {
				errs = append(errs, CachePolicyFieldError{Field: field, Code: "exceeds_static_cap", Limit: map[string]int{"max": hi}})
			}
		}
		out[field] = next
		old := before[field]
		if _, ok := next.(int); ok {
			old = policyInt(old)
		}
		if !reflect.DeepEqual(old, next) {
			diff = append(diff, CachePolicyDiff{field, old, next})
		}
	}
	return out, diff, errs
}

func policyDirection(diff []CachePolicyDiff) string {
	up, down := false, false
	for _, d := range diff {
		switch n := d.After.(type) {
		case bool:
			if n {
				up = true
			} else {
				down = true
			}
		case int:
			if n > policyInt(d.Before) {
				up = true
			} else {
				down = true
			}
		}
	}
	if up && down {
		return "mixed"
	}
	if up {
		return "enable"
	}
	return "disable"
}
func (s *CachePolicyService) validation(index int, entry cachePolicyEntry, w CachePolicyWrite, bodyErrors []CachePolicyFieldError) (CachePolicyValidation, map[string]any) {
	spec, diff, errs := s.merge(index, entry.envelope.Spec, w.Spec, true)
	if len(bodyErrors) > 0 {
		errs = bodyErrors
		diff = []CachePolicyDiff{}
	}
	if utf8.RuneCountInString(strings.TrimSpace(w.Reason)) > 200 {
		errs = append(errs, CachePolicyFieldError{Field: "reason", Code: "too_long", Limit: map[string]int{"max": 200}})
	}
	v := CachePolicyValidation{Valid: len(errs) == 0, Errors: errs, Warnings: []string{}, Diff: diff, Risk: policyRisk(index), Direction: policyDirection(diff), CurrentRev: entry.envelope.Rev, Affected: []CachePolicyAffected{}}
	v.RevStale = w.ExpectedRev != nil && *w.ExpectedRev != v.CurrentRev
	if index == 0 && v.Direction != "disable" {
		v.Confirm.ReasonRequired = true
		v.Confirm.CheckboxRequired = true
	}
	if !v.Valid {
		return v, spec
	}
	next := entry
	next.parseError = false
	next.envelope.Spec = spec
	before := s.effective(index, entry, entry)
	after := s.effective(index, next, next)
	v.EffectivePreview = &CachePolicyPreview{after.State, after.Active, after.Blockers}
	if index == 0 {
		for i := 3; i < 6; i++ {
			v.Affected = append(v.Affected, CachePolicyAffected{cachePolicyKeys[i], "not_in_build", "not_in_build", nil})
		}
	} else {
		v.Affected = append(v.Affected, CachePolicyAffected{cachePolicyKeys[index], before.State, after.State, after.Active})
	}
	return v, spec
}
func (s *CachePolicyService) Validate(ctx context.Context, key string, w CachePolicyWrite, bodyErrors []CachePolicyFieldError) (CachePolicyValidation, error) {
	i := policyIndex(key)
	if i < 0 || i > 1 {
		return CachePolicyValidation{}, PolicyNotInBuild(key)
	}
	rows, err := s.repo.GetMultiple(ctx, []string{cachePolicySettings[i]})
	if err != nil {
		return CachePolicyValidation{}, err
	}
	v, _ := s.validation(i, s.parse(i, rows[cachePolicySettings[i]]), w, bodyErrors)
	return v, nil
}
func invalidPolicy(errs []CachePolicyFieldError) error {
	b, _ := json.Marshal(errs)
	return &CachePolicyError{400, "CACHE_POLICY_INVALID", "cache policy validation failed", map[string]string{"errors": string(b)}}
}
func (s *CachePolicyService) Update(ctx context.Context, key string, w CachePolicyWrite, bodyErrors []CachePolicyFieldError, actor CachePolicyActor) (CachePolicyView, []CachePolicyDiff, error) {
	i := policyIndex(key)
	if i < 0 || i > 1 {
		return CachePolicyView{}, nil, PolicyNotInBuild(key)
	}
	if s.store == nil {
		return CachePolicyView{}, nil, fmt.Errorf("cache policy transaction store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var updated cachePolicyEntry
	var diff []CachePolicyDiff
	err := s.store.UpdateCachePolicy(ctx, cachePolicySettings[i], func(raw string) (string, error) {
		current := s.parse(i, raw)
		v, spec := s.validation(i, current, w, bodyErrors)
		if !v.Valid {
			return "", invalidPolicy(v.Errors)
		}
		if w.ExpectedRev == nil {
			return "", invalidPolicy([]CachePolicyFieldError{{Field: "expected_rev", Code: "malformed_body"}})
		}
		if v.RevStale {
			return "", &CachePolicyError{409, "CACHE_POLICY_REV_CONFLICT", "cache policy has been modified", map[string]string{"current_rev": fmt.Sprint(v.CurrentRev), "expected_rev": fmt.Sprint(*w.ExpectedRev)}}
		}
		missing := []string{}
		if v.Confirm.ReasonRequired && strings.TrimSpace(w.Reason) == "" {
			missing = append(missing, "reason")
		}
		if v.Confirm.CheckboxRequired && !w.Confirm.Checkbox {
			missing = append(missing, "checkbox")
		}
		if len(missing) > 0 {
			return "", &CachePolicyError{422, "CACHE_POLICY_CONFIRM_REQUIRED", "confirmation required", map[string]string{"missing": strings.Join(missing, ",")}}
		}
		now := s.now().UTC()
		reason := strings.TrimSpace(w.Reason)
		updated = cachePolicyEntry{envelope: CachePolicyEnvelope{v.CurrentRev + 1, &now, &actor, &reason, spec}, loadedAt: now}
		diff = v.Diff
		b, err := json.Marshal(updated.envelope)
		return string(b), err
	})
	if err != nil {
		return CachePolicyView{}, nil, err
	}
	entries := s.snapshot.Load().entries
	entries[i] = updated
	s.publishEntries(entries)
	return s.view(i, updated, s.snapshot.Load()), diff, nil
}

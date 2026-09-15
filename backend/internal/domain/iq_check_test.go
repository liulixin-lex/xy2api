package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIQCheckPartialSettingsAndCopy(t *testing.T) {
	state := IQCheck{Enabled: true, IntervalMinutes: 60, Model: "custom", ReasoningEffort: "ultra", OutputMode: "strict", Status: "degraded", Revision: "old"}
	var patch IQCheckSettings
	if err := json.Unmarshal([]byte(`{"enabled":false}`), &patch); err != nil {
		t.Fatal(err)
	}
	next := state.WithSettings(&patch)
	if next.Enabled || next.Model != "custom" || next.ReasoningEffort != "ultra" || next.IntervalMinutes != 60 || next.OutputMode != "strict" {
		t.Fatal(next)
	}
	copied := DefaultIQCheck().WithSettings(state.CopySettings())
	if copied.Enabled || copied.Status != "unknown" || copied.Revision != "" || copied.Model != state.Model || copied.OutputMode != state.OutputMode {
		t.Fatal(copied)
	}
	for _, value := range []string{`{"interval_minutes":0}`, `{"interval_minutes":1441}`, `{"model":" "}`, `{"reasoning_effort":"High Depth"}`, `{"output_mode":"invalid"}`} {
		var input IQCheckSettings
		if err := json.Unmarshal([]byte(value), &input); err != nil {
			t.Fatal(err)
		}
		if input.Validate() == nil {
			t.Errorf("accepted %s", value)
		}
	}
	var valid IQCheckSettings
	if err := json.Unmarshal([]byte(`{"model":" custom/model ","reasoning_effort":"upstream_default"}`), &valid); err != nil {
		t.Fatal(err)
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if *valid.Model != "custom/model" {
		t.Fatal(valid)
	}
}

func TestIQFixedIntervalEligibility(t *testing.T) {
	s := DefaultIQCheck()
	if s.IntervalMinutes != 5 || s.TimeoutSeconds != 120 {
		t.Fatal(s)
	}
	now := time.Date(2026, 9, 14, 23, 59, 0, 0, time.UTC)
	s.LastRunAt = &now
	s.IntervalMinutes = 15
	next, reason := s.Eligibility(now.Add(time.Minute))
	if reason != "minimum_interval" || !next.Equal(now.Add(15*time.Minute)) {
		t.Fatal(next, reason)
	}
	for _, raw := range []string{`{"timeout_seconds":301}`, `{"timeout_seconds":29}`, `{"interval_minutes":1441}`} {
		var p IQCheckSettings
		if json.Unmarshal([]byte(raw), &p) != nil || p.Validate() == nil {
			t.Fatal(raw)
		}
	}
	var legacy IQCheck
	if err := json.Unmarshal([]byte(`{"enabled":true,"interval_minutes":10,"scheduling_mode":"adaptive","smart_streak":6,"daily_request_limit":1,"budget_used":99,"quota_group":"old"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.EffectiveInterval() != 10*time.Minute {
		t.Fatal(legacy)
	}
	next, reason = legacy.Eligibility(now)
	if reason != "" || !next.Equal(now) {
		t.Fatal(next, reason)
	}
	raw, _ := json.Marshal(legacy)
	var output map[string]any
	_ = json.Unmarshal(raw, &output)
	for _, k := range []string{"scheduling_mode", "smart_streak", "daily_request_limit", "budget_used", "quota_group"} {
		if _, ok := output[k]; ok {
			t.Fatal(k)
		}
	}
}

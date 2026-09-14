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

func TestIQMonitoringBudgetAndFreshness(t *testing.T) {
	s := DefaultIQCheck()
	if s.DailyLimit() != 96 || s.TimeoutSeconds != 120 || s.SchedulingMode != "fixed" {
		t.Fatal(s)
	}
	s.IntervalMinutes = 1
	if s.DailyLimit() != 1440 {
		t.Fatal(s)
	}
	s.DailyRequestLimit = 2
	s.BudgetDay = "2026-09-14"
	s.BudgetUsed = 2
	now := time.Date(2026, 9, 14, 23, 59, 0, 0, time.UTC)
	last := now
	s.LastRunAt = &last
	s.IntervalMinutes = 15
	next, reason := s.Eligibility(now)
	if reason != "minimum_interval" || !next.Equal(now.Add(15*time.Minute)) {
		t.Fatal(next, reason)
	}
	next, _ = s.Eligibility(now.Add(time.Minute))
	if !next.Equal(now.Add(15 * time.Minute)) {
		t.Fatal("midnight bypass", next)
	}
	for _, raw := range []string{`{"timeout_seconds":301}`, `{"daily_request_limit":0}`, `{"scheduling_mode":"random"}`, `{"quota_group":"../bad"}`} {
		var p IQCheckSettings
		if json.Unmarshal([]byte(raw), &p) != nil || p.Validate() == nil {
			t.Fatal(raw)
		}
	}
}

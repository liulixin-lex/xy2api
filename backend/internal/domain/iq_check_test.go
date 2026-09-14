package domain

import (
	"encoding/json"
	"testing"
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

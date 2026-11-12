//go:build unit

package service

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSchedAdminDTOExamples(t *testing.T) {
	cases := map[string]any{
		"account_summary":      &SchedAdminSummary{},
		"account_summary_item": &SchedAdminAccountSummary{},
		"account_detail":       &SchedAdminAccountDetail{},
		"reset":                &SchedAdminReset{}, "test": &SchedAdminTest{},
		"clear_auto_state": &SchedAdminClearAutoState{}, "attention": &SchedAdminAttention{},
		"policy": &SchedAdminPolicy{}, "board": &SchedAdminBoard{},
	}
	for name, dst := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/sched_quality/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, dst); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(dst)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err = json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip mismatch: %s", encoded)
			}
		})
	}
}

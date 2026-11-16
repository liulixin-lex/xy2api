//go:build unit

package openai

import (
	"reflect"
	"testing"
)

func TestNearestSupportedReasoningEffort(t *testing.T) {
	supported := []string{"high", "low", "low"}
	before := append([]string(nil), supported...)
	for effort, want := range map[string]string{"none": "low", "minimal": "low", "low": "low", "medium": "low", "high": "high", "xhigh": "high", "max": "high"} {
		got, err := NearestSupportedReasoningEffort(effort, supported)
		if err != nil || got != want {
			t.Fatal(effort, got, err)
		}
	}
	if !reflect.DeepEqual(before, supported) {
		t.Fatal("mutated supported")
	}
	for _, tc := range []struct {
		effort    string
		supported []string
	}{{"high", nil}, {" HIGH", supported}, {"ultra", supported}, {"high", []string{"high", "bad"}}} {
		if _, err := NearestSupportedReasoningEffort(tc.effort, tc.supported); err == nil {
			t.Fatal(tc)
		}
	}
}

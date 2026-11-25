//go:build unit

package service

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedAdminDTOExamples(t *testing.T) {
	cases := map[string]any{
		"account_summary":      &SchedAdminSummary{},
		"account_summary_item": &SchedAdminAccountSummary{},
		"account_detail":       &SchedAdminAccountDetail{},
		"reset":                &SchedAdminReset{},
		"clear_auto_state":     &SchedAdminClearAutoState{}, "attention": &SchedAdminAttention{},
		"policy": &SchedAdminPolicy{}, "order": &SchedAdminOrder{},
		"policy_saved": &SchedAdminPolicySaved{}, "groups_summary": &SchedAdminGroupsSummary{}, "preview": &SchedAdminPolicyPreview{}, "ttft_rule": &SchedAdminTTFTRule{}, "ttft_rule_saved": &SchedAdminTTFTRuleSaved{}, "decisions": &[]SchedAdminDecision{},
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

func TestObserveLegacyDecisionBasis(t *testing.T) {
	raw, err := os.ReadFile("testdata/sched_quality/legacy_basis.json")
	require.NoError(t, err)
	var stored q.Basis
	require.NoError(t, json.Unmarshal(raw, &stored))
	out := schedAdminJSON(t, SchedAdminDecisionBasis(stored))
	for _, name := range []string{"samples_local", "samples_total", "trial_seats"} {
		require.Contains(t, out, name)
		require.Nil(t, out[name])
	}
	ttft := out["ttft"].(map[string]any)
	require.Contains(t, ttft, "n_local")
	require.Nil(t, ttft["n_local"])
	require.EqualValues(t, 25, ttft["n"])
}

func TestObservePolicyTripExamples(t *testing.T) {
	for _, tc := range []struct {
		rate, effective float64
		want            []SchedAdminErrorTripExample
	}{
		{.01, .01, []SchedAdminErrorTripExample{{50, 2}, {100, 3}}},
		{0, .01, []SchedAdminErrorTripExample{{50, 2}, {100, 3}}},
		{.02, .02, []SchedAdminErrorTripExample{{50, 3}, {100, 5}}},
		{.05, .05, []SchedAdminErrorTripExample{{50, 6}, {100, 10}}},
		{.95, .95, []SchedAdminErrorTripExample{{100, 100}}},
		{1, 1, []SchedAdminErrorTripExample{}},
	} {
		policy := schedAdminPolicy(SchedPolicyRecord{Policy: q.Policy{MaxErrorRate: tc.rate}})
		require.Equal(t, tc.effective, policy.EffectiveMaxErrorRate)
		require.Equal(t, tc.want, policy.ErrorTripExamples)
	}
}

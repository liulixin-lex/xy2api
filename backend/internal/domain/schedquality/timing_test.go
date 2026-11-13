//go:build unit

package schedquality

import (
	"testing"
	"time"
)

func TestEvaluateAtEventKinds(t *testing.T) {
	// Six recent slow samples must trigger only at completion, not at a
	// minute-only evaluation. Twenty older slow samples are the reverse.
	for _, tc := range []struct {
		name string
		rows []AttemptSample
		kind EvaluationKind
		want Verdict
	}{
		{"fast-on-completion", slowEvaluationSamples(6, 10, time.Minute), EvaluateFast, VerdictFail},
		{"not-fast-on-minute", slowEvaluationSamples(6, 10, time.Minute), EvaluateRegular, VerdictNoData},
		{"regular-on-minute", slowEvaluationSamples(20, 10, 10*time.Minute), EvaluateRegular, VerdictFail},
		{"not-regular-on-completion", slowEvaluationSamples(20, 10, 10*time.Minute), EvaluateFast, VerdictNoData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := EvaluateAt(tc.rows, evaluationScope, evaluationPolicy(), time.Time{}, evaluationNow, tc.kind)
			if e.Verdicts.Speed != tc.want {
				t.Fatalf("kind %v: %+v", tc.kind, e)
			}
			next, _ := AdvanceAt(evaluationState(), evaluationPolicy(), tc.rows, evaluationNow, tc.kind)
			if (next.State == StateCooling) != (tc.want == VerdictFail) {
				t.Fatalf("kind %v transitioned incorrectly: %+v", tc.kind, next)
			}
		})
	}
}

func TestEvaluateContractOutcomes(t *testing.T) {
	rows := evaluationSamples(4, 10, time.Minute)
	rows[0].Outcome = OutcomeCanceled
	rows[1].Outcome = OutcomeOther
	rows[2].Outcome = OutcomeTimeout // even a populated first-output value cannot make outcome 2 pass
	rows[3].Outcome = OutcomeAccountFault
	e := EvaluateAt(rows, evaluationScope, evaluationPolicy(), time.Time{}, evaluationNow, EvaluateRegular)
	if e.Error.N != 2 || e.Error.Bad != 2 || e.Speed.N != 4 || e.Speed.Slow != 1 {
		t.Fatalf("contract outcomes: %+v", e)
	}
}

func TestDecisionBasisUsesTriggeredEventAndAllDimensions(t *testing.T) {
	rows := slowEvaluationSamples(60, 10, time.Minute)
	for i := range rows {
		rows[i].Outcome = OutcomeAccountFault
	}
	p := evaluationPolicy()
	for _, kind := range []EvaluationKind{EvaluateRegular, EvaluateFast} {
		e := EvaluateAt(rows, evaluationScope, p, time.Time{}, evaluationNow, kind)
		b := DescribeDecision(e, rows, p)
		if kind == EvaluateFast {
			if len(b.Dims) != 1 || b.Metrics[DimensionTTFO].N != 6 || b.Metrics[DimensionTTFO].WindowMin != 5 || len(b.AttemptIDs) != 6 {
				t.Fatalf("fast basis: %+v", b)
			}
		} else if len(b.Dims) != 2 || b.Metrics[DimensionTTFO].N != 60 || len(b.AttemptIDs) != 50 {
			t.Fatalf("ordinary basis: %+v", b)
		}
		if b.AttemptIDs[0] != rows[len(rows)-1].AttemptID {
			t.Fatal("bad attempt references must be newest first")
		}
	}
}

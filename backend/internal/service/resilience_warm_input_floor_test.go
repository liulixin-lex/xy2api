//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWarmInputFloorPreservesBudgetAndProfiles(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		kib                    int
		warm, mismatch, absent bool
		effort                 string
		override, spent, want  int
		last                   bool
	}{
		{name: "warm 255", kib: 255, warm: true, want: 20},
		{name: "warm 256", kib: 256, warm: true, want: 30},
		{name: "warm 511", kib: 511, warm: true, want: 30},
		{name: "warm 512", kib: 512, warm: true, want: 40},
		{name: "cold 127", kib: 127, want: 20},
		{name: "cold 128", kib: 128, want: 45},
		{name: "cold 255", kib: 255, want: 45},
		{name: "cold 256", kib: 256, want: 75},
		{name: "cold 511", kib: 511, want: 75},
		{name: "cold 512", kib: 512, want: 75},
		{name: "high below floor", kib: 255, warm: true, effort: "high", want: 25},
		{name: "high input floor", kib: 256, warm: true, effort: "high", want: 30},
		{name: "max input floor", kib: 512, warm: true, effort: "max", want: 40},
		{name: "model longer", kib: 512, warm: true, override: 90, want: 90},
		{name: "mismatched warm", kib: 512, warm: true, mismatch: true, want: 20},
		{name: "mismatched cold", kib: 512, mismatch: true, want: 20},
		{name: "missing profile", kib: 512, absent: true, want: 20},
		{name: "budget remaining", kib: 512, warm: true, spent: 225, want: 15},
		{name: "last candidate", kib: 512, warm: true, last: true, want: 240},
		{name: "last remaining", kib: 512, warm: true, spent: 225, last: true, want: 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) {
				if tc.override > 0 {
					s.Budget.ModelOverrides = map[string]int{"gpt-test": tc.override}
				}
			})
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			start := time.Now()
			st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start.Add(-time.Duration(tc.spent)*time.Second)))
			if !tc.absent {
				accountID := int64(7)
				if tc.mismatch {
					accountID++
				}
				st.SetAttemptProfile(resilience.AttemptProfile{AccountID: accountID, AttemptStart: start, Model: "gpt-test", Warm: tc.warm, Stream: true, BodyBytes: tc.kib * 1024})
			}
			if tc.last {
				st.SetLastResort(7)
			}
			svc := &OpenAIGatewayService{}
			require.Equal(t, time.Duration(tc.want)*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, 7, "gpt-test", tc.effort, start))
		})
	}
}

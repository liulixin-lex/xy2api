package resilience

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Regression from review G-1: handler preset and m3 reset are preparation for
// one actual dispatch. Register that dispatch after the header-guard decision.
func TestG9HandlerAndRepairStartsCountOnlyDispatchedOwner(t *testing.T) {
	withPublished(t, nil)
	for i, timed := range []bool{true, false} {
		t.Run(map[bool]string{true: "timed", false: "untimed"}[timed], func(t *testing.T) {
			var logs bytes.Buffer
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(oldLogger) })
			oldCheck := undeliveredAbuseCheck.Load()
			SetUndeliveredAbuseCheck(func(int64) bool { return true })
			t.Cleanup(func() { undeliveredAbuseCheck.Store(oldCheck) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := NewRequestState()
			st.InitDelivery(ctx, time.Now().Add(-239*time.Second), nil)
			st.SetAPIKey(42)
			st.SetGroup(int64(8200 + i))
			cfg := Defaults()
			cfg.Budget.BudgetStart = BudgetStartBodyEOF
			st.SetBudget(NewBudget(ctx, &cfg, KindText, time.Now().Add(-239*time.Second)))
			forwardStart := time.Now()
			st.SetAttemptProfile(AttemptProfile{AccountID: 9, Model: "gpt", AttemptStart: forwardStart, Stream: timed})
			repairStart := forwardStart.Add(time.Millisecond)
			st.SetAttemptProfile(AttemptProfile{AccountID: 9, Model: "gpt", AttemptStart: repairStart, Stream: timed})
			if timed {
				plan := st.PlanAttemptTimeout(30*time.Second, false, repairStart)
				require.Positive(t, plan.Effective())
			}
			label := map[bool]string{true: "true:body_eof", false: "false:body_eof"}[timed]
			before := g9MetricTotal(label, int64(8200+i))
			st.ObserveAttemptStart()
			st.ObserveAttemptStart()
			timing := st.Timing()
			wantNoTimer := int64(1)
			if timed {
				wantNoTimer = 0
			}
			t.Logf("late_attempt_count=%d late_attempt_no_timer_count=%d", *timing.LateAttemptCount, *timing.LateAttemptNoTimerCount)
			require.EqualValues(t, 1, *timing.LateAttemptCount)
			require.Equal(t, wantNoTimer, *timing.LateAttemptNoTimerCount)
			require.Equal(t, before+1, g9MetricTotal(label, int64(8200+i)))
			cancel()
			st.ResolveDisconnect()
			var abuseWarn, lateWarn map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &record))
				switch record["msg"] {
				case "resilience.abuse_guard":
					abuseWarn = record
				case "resilience.late_attempt_undelivered":
					lateWarn = record
				}
			}
			require.NotNil(t, abuseWarn)
			require.EqualValues(t, 1, abuseWarn["late_attempt_count"])
			require.EqualValues(t, wantNoTimer, abuseWarn["late_attempt_no_timer_count"])
			if timed {
				require.Nil(t, lateWarn, "a timed dispatch must not enter the SQL late side")
			} else {
				require.NotNil(t, lateWarn)
				require.EqualValues(t, 1, lateWarn["late_attempt_count"])
				require.EqualValues(t, 1, lateWarn["late_attempt_no_timer_count"])
			}
		})
	}
}

func TestG9ResponseStageCannotRewriteDispatchClassification(t *testing.T) {
	now := time.Now()
	cfg := Defaults()
	st := NewRequestState()
	st.SetGroup(8211)
	st.SetBudget(NewBudget(context.Background(), &cfg, KindText, now.Add(-239*time.Second)))
	st.SetAttemptProfile(AttemptProfile{AttemptStart: now})
	before := g9MetricTotal("false:arrival", 8211)
	st.ObserveAttemptStart()
	st.ResponseStagePlan(now.Add(time.Millisecond))
	st.ObserveAttemptStart()
	require.EqualValues(t, 1, *st.Timing().LateAttemptCount)
	require.EqualValues(t, 1, *st.Timing().LateAttemptNoTimerCount)
	require.Equal(t, before+1, g9MetricTotal("false:arrival", 8211))
}

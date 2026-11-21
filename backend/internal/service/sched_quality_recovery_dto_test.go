//go:build unit

package service

import (
	"context"
	"encoding/json"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRecoveryThreeResponseFields(t *testing.T) {
	s, e, _ := schedAdminFixture()
	now := s.d.Now()
	ctx := context.Background()
	e.snapshot.Policies[1] = SchedPolicyRecord{Policy: q.Policy{Enabled: true}}
	key := schedQualityKey{1, "synthetic-model", 1}
	e.perf = &SchedPerfSnapshot{at: now, from: now.Add(-12 * time.Hour), complete: true, rows: map[schedQualityKey]*schedPerfRow{key: {}}, policies: e.snapshot.Policies}
	for _, mode := range []string{"rear", "trial", "returned"} {
		t.Run(mode, func(t *testing.T) {
			view := q.StateView{}
			switch mode {
			case "rear":
				view.TTFO = q.LineView{Identity: q.IdentityRear, Level: 4, Until: now.Add(20 * time.Minute)}
				view.Error = q.LineView{Identity: q.IdentityRear, Level: 1, Until: now.Add(40 * time.Minute)}
			case "trial":
				view.TTFO = q.LineView{Trial: true, Level: 4, TrialStartedAt: now.Add(-40 * time.Minute), TrialUntil: now.Add(80 * time.Minute)}
				view.Error = q.LineView{Trial: true, Level: 1, TrialStartedAt: now.Add(-20 * time.Minute), TrialUntil: now.Add(100 * time.Minute)}
			case "returned":
				view.Error = q.LineView{Identity: q.IdentityUnknown, Level: 1}
			}
			e.snapshot.GroupRows[1] = []SchedGroupRow{{GroupID: 1, Model: key.model, View: view}}
			detail, err := s.Detail(ctx, 1)
			require.NoError(t, err)
			detailJSON := schedAdminJSON(t, detail)
			group := detailJSON["groups"].([]any)[0].(map[string]any)
			model := group["by_model"].([]any)[0].(map[string]any)
			line := model["lines"].(map[string]any)["error"].(map[string]any)
			require.EqualValues(t, 1, line["level"])
			performance, err := s.AccountPerformance(ctx, 1)
			require.NoError(t, err)
			require.Len(t, performance.Rows, 1)
			perf := performance.Rows[0].Lines.Error
			require.Equal(t, 1, perf.Level)
			summary, err := s.Summary(ctx, []int64{1})
			require.NoError(t, err)
			require.Len(t, summary.Accounts, 1)
			account := summary.Accounts[0]
			if mode == "trial" {
				start := schedAdminStamp(view.Error.TrialStartedAt)
				require.Equal(t, start, line["trial_started_at"])
				require.Equal(t, new(start), perf.TrialStartedAt)
				require.Len(t, account.Trial, 1)
				r := account.Trial[0]
				require.Equal(t, q.DimensionError, r.UntilLine)
				require.Equal(t, 1, r.Level)
				require.Equal(t, new(start), r.TrialStartedAt)
				require.Equal(t, schedAdminStamp(view.Error.TrialUntil), r.Until)
			} else {
				require.Nil(t, line["trial_started_at"])
				require.Nil(t, perf.TrialStartedAt)
				if mode == "rear" {
					require.Len(t, account.Rear, 1)
					r := account.Rear[0]
					require.Equal(t, 1, r.Level)
					require.Equal(t, q.DimensionError, r.UntilLine)
					require.Equal(t, schedAdminStamp(view.Error.Until), r.Until)
				} else {
					require.Empty(t, account.Rear)
					require.Empty(t, account.Trial)
					require.Equal(t, "unknown", line["identity"])
					require.Equal(t, "unknown", perf.Identity)
				}
			}
			example := map[string]any{"quality_panel": line, "performance": perf, "rear": account.Rear, "trial": account.Trial}
			raw, err := json.Marshal(example)
			require.NoError(t, err)
			t.Logf("%s %s", mode, raw)
		})
	}
}

func TestRecoverySnapshotPublishesRealTrialOrigin(t *testing.T) {
	f := newQualityFixture(t)
	origin := f.now.Add(-30 * time.Minute)
	f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Trial: true, Epoch: origin, TrialUntil: origin.Add(2 * time.Hour), EvaluatedAt: f.now.Add(-time.Minute)}}
	f.cycle()
	require.Equal(t, origin, f.view().TTFO.TrialStartedAt)
	b := q.Basis{ReleaseReason: "deadline", TrialDurationSeconds: 3600, HistoryMissing: true, MissingDeadline: true, OwnSampleAt60: new(true)}
	out := SchedAdminDecisionBasis(b)
	require.Equal(t, b.ReleaseReason, out.ReleaseReason)
	require.Equal(t, int64(3600), out.TrialDurationSeconds)
	require.True(t, out.HistoryMissing)
	require.True(t, out.MissingDeadline)
	require.Equal(t, new(true), out.OwnSampleAt60)
}

//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func requireNewSessionLists(t *testing.T, event map[string]any, winner int64) []newSessionCandidateLog {
	t.Helper()
	var candidates []newSessionCandidateLog
	var excluded []newSessionExclusion
	for key, dst := range map[string]any{"candidates": &candidates, "excluded": &excluded} {
		require.IsType(t, []any{}, event[key], key)
		b, err := json.Marshal(event[key])
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(b, dst))
	}
	placements, skips, reasons := map[string]int{}, map[string]int{}, map[string]int{}
	seen := map[int64]bool{}
	for i, row := range candidates {
		require.False(t, seen[row.AccountID])
		seen[row.AccountID] = true
		require.Equal(t, i+1, row.Rank)
		require.Contains(t, []string{"front", "trial_with_seat", "trial_full", "trial_unknown", "rear"}, row.Placement)
		placements[row.Placement]++
		for _, skip := range row.Skip {
			skips[skip]++
		}
	}
	for _, row := range excluded {
		reasons[row.Reason]++
	}
	if event["pool_rounds"] == float64(1) && event["rescue_attempted"] == nil {
		require.Equal(t, float64(len(candidates)), event["eligible_candidate_count"])
		for field, count := range map[string]int{
			"front_or_trial_with_seat_count": placements["front"] + placements["trial_with_seat"],
			"trial_full_count":               placements["trial_full"],
			"trial_unknown_count":            placements["trial_unknown"],
			"rear_count":                     placements["rear"],
		} {
			require.GreaterOrEqual(t, float64(count), event[field], field)
		}
		for field, count := range map[string]int{
			"load_full_skipped_count":            skips["load_full"],
			"slot_unavailable_skipped_count":     skips["slot_unavailable"],
			"filtered_model_not_supported_count": reasons["model"],
			"filtered_profit_floor_count":        reasons["profit_floor"],
			"filtered_profit_ceiling_count":      reasons["profit_ceiling"],
			"filtered_profit_invalid_rate_count": reasons["profit_invalid_account_rate"],
		} {
			require.Equal(t, float64(count), event[field], field)
		}
	}
	if winner != 0 {
		require.Equal(t, float64(winner), event["selected_account_id"])
		if rescue, ok := event["rescue"].(map[string]any); ok {
			require.Equal(t, float64(winner), rescue["account_id"])
			require.Equal(t, event["selected_quality_position"], rescue["placement"])
		} else {
			require.NotEmpty(t, candidates)
			require.Equal(t, winner, candidates[0].AccountID)
		}
	} else {
		require.Nil(t, event["selected_account_id"])
	}
	return candidates
}

func TestOpenAINewSessionDispatchList(t *testing.T) {
	t.Run("all rear, no truncation", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		accounts := make([]Account, 24)
		for i := range accounts {
			accounts[i] = selectionFixtureAccount(int64(i+1), 1)
		}
		f := newSelectionRuntimeFixture(t, accounts...)
		for _, a := range accounts {
			f.view.States[fakeSchedViewKey{7, a.ID, "m"}] = schedquality.StateView{
				Cache: schedquality.LineView{Identity: schedquality.IdentityRear}, TTFO: schedquality.LineView{Trial: true},
			}
		}
		selected, err := selectOrdinaryNewSessionForTest(t, f, context.Background(), "ordinary", nil, false)
		require.NoError(t, err)
		defer selected.ReleaseFunc()
		require.Equal(t, int64(1), selected.Account.ID)
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		rows := requireNewSessionLists(t, events[0], 1)
		require.Len(t, rows, 24)
		for _, row := range rows {
			require.Equal(t, "rear", row.Placement)
			require.Equal(t, map[string]string{"cache": "rear", "ttfo": "trial"}, row.Lines)
		}
	})

	t.Run("model and all profit exclusions", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		ok, model := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
		low, high, invalid := selectionFixtureAccount(3, 1), selectionFixtureAccount(4, 1), selectionFixtureAccount(5, 1)
		ok.RateMultiplier, model.RateMultiplier, low.RateMultiplier, high.RateMultiplier = floatPtr(.4), floatPtr(.4), floatPtr(.2), floatPtr(.8)
		model.Credentials = map[string]any{"model_mapping": map[string]any{"other": "other"}}
		f := newSelectionRuntimeFixture(t, ok, model, low, high, invalid)
		ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
			ceilingOn: true, groupID: 7, platform: PlatformOpenAI, floor: .3, threshold: .5, sell: 1, pricingAt: f.view.At(),
		})
		selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "ordinary", nil, true)
		require.NoError(t, err)
		defer selected.ReleaseFunc()
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		require.Len(t, requireNewSessionLists(t, events[0], 1), 1)
		require.ElementsMatch(t, []any{
			map[string]any{"account_id": float64(2), "reason": "model"},
			map[string]any{"account_id": float64(3), "reason": "profit_floor"},
			map[string]any{"account_id": float64(4), "reason": "profit_ceiling"},
			map[string]any{"account_id": float64(5), "reason": "profit_invalid_account_rate"},
		}, events[0]["excluded"])
		require.Equal(t, []any{map[string]any{"account_id": float64(1), "placement": "front", "rank": float64(1)}}, events[0]["candidates"])
	})

	t.Run("compact round before collaboration preference", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		a, b, c, d := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1), selectionFixtureAccount(3, 1), selectionFixtureAccount(4, 1)
		c.Extra = map[string]any{"openai_compact_supported": true}
		d.Extra = map[string]any{"openai_compact_supported": false}
		f := newSelectionRuntimeFixture(t, a, b, c, d)
		ctx := resilience.WithRequestState(context.WithValue(context.Background(), openAICollabHintKey{}, int64(2)), resilience.NewRequestState())
		selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "ordinary", "m", nil,
			OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, true, false, false, PlatformOpenAI)
		require.NoError(t, err)
		defer selected.ReleaseFunc()
		require.Equal(t, []int64{3}, f.attempts)
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		rows := requireNewSessionLists(t, events[0], 3)
		require.Equal(t, []newSessionCandidateLog{
			{AccountID: 3, Placement: "front", Rank: 1}, {AccountID: 2, Placement: "front", Rank: 2},
			{AccountID: 1, Placement: "front", Rank: 3}, {AccountID: 4, Placement: "front", Rank: 4},
		}, rows)
	})

	t.Run("capacity retry retains both reasons without reading skipped seats", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1), selectionFixtureAccount(3, 1), selectionFixtureAccount(4, 3))
		f.view.States[fakeSchedViewKey{7, 3, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Trial: true}}
		leases := newSelectionTrialLeaseFake()
		f.svc.selectionBoard.leases = leases
		f.loads.loadMap = map[int64]*AccountLoadInfo{3: {LoadRate: 100}, 4: {LoadRate: 100}}
		f.loads.acquireResults = map[int64]bool{1: false, 2: false}
		f.loads.after = func(id int64) {
			if id == 2 {
				f.loads.loadMap[1] = &AccountLoadInfo{LoadRate: 100}
				delete(f.loads.loadMap, 4)
				f.loads.acquireResults[2] = true
			}
		}
		selected, err := selectOrdinaryNewSessionForTest(t, f, context.Background(), "ordinary", nil, false)
		require.NoError(t, err)
		defer selected.ReleaseFunc()
		require.Equal(t, []int64{1, 2, 2}, f.attempts)
		require.Empty(t, leases.calls)
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		rows := requireNewSessionLists(t, events[0], 2)
		require.Equal(t, float64(0), events[0]["trial_unknown_count"], "the load-full trial never reached a sort")
		require.Equal(t, []newSessionCandidateLog{
			{AccountID: 2, Placement: "front", Rank: 1, Skip: []string{"slot_unavailable"}},
			{AccountID: 4, Placement: "front", Rank: 2, Skip: []string{"load_full"}},
			{AccountID: 1, Placement: "front", Rank: 3, Skip: []string{"load_full", "slot_unavailable"}},
			{AccountID: 3, Placement: "trial_unknown", Rank: 4, Lines: map[string]string{"ttfo": "trial"}, Skip: []string{"load_full"}},
		}, rows)
	})
}

func TestOpenAINewSessionDispatchPoolBoundaries(t *testing.T) {
	t.Run("quarantine retry replaces only the new snapshot", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		a, b := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
		proxy := int64(9)
		b.ProxyID = &proxy
		f := newSelectionRuntimeFixture(t, a, b)
		f.repo.onList = func(round int) ([]Account, error) {
			if round == 1 {
				return []Account{a}, nil
			}
			return []Account{b}, nil
		}
		f.svc.openaiProxyStreamCircuit = newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: time.Minute, maxEntries: 4,
		})
		f.svc.openaiProxyStreamCircuit.recordFailure(proxy, time.Now())
		f.loads.acquireResults = map[int64]bool{a.ID: false, b.ID: true}
		ctx := WithOpenAISelectionNoWait(context.Background())
		selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "ordinary", nil, false)
		require.NoError(t, err)
		defer selected.ReleaseFunc()
		require.Equal(t, b.ID, selected.Account.ID)
		require.Equal(t, 2, f.repo.lists)
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		event := events[0]
		require.Equal(t, float64(2), event["pool_rounds"])
		require.Equal(t, float64(1), event["eligible_candidate_count"])
		require.Equal(t, float64(2), event["front_or_trial_with_seat_count"])
		require.Equal(t, float64(1), event["slot_unavailable_skipped_count"])
		require.Equal(t, []newSessionCandidateLog{{AccountID: 2, Placement: "front", Rank: 1}}, requireNewSessionLists(t, event, b.ID))
	})

	t.Run("load full falls through to ordinary wait", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		f.loads.loadMap = map[int64]*AccountLoadInfo{1: {LoadRate: 100}}
		state := resilience.NewRequestState()
		state.EnableGatewayRequest(time.Now())
		selected, err := selectOrdinaryNewSessionForTest(t, f, resilience.WithRequestState(context.Background(), state), "ordinary", nil, false)
		require.NoError(t, err)
		require.False(t, selected.Acquired)
		require.Equal(t, int64(1), selected.WaitPlan.AccountID)
		require.Empty(t, f.attempts)
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		final, persist := state.ClaimGatewayRequest(false)
		require.True(t, persist)
		require.Equal(t, resilience.RequestFailed, final.Result)
		require.Zero(t, final.AccountID, "a wait plan did not send an upstream request")
		require.Equal(t, final.RequestID, events[0]["gateway_request_id"])
		require.Equal(t, []newSessionCandidateLog{{AccountID: 1, Placement: "front", Rank: 1, Skip: []string{"load_full"}}}, requireNewSessionLists(t, events[0], 1))
	})

	t.Run("failed reopen retains the last successful pool", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		a := selectionFixtureAccount(1, 1)
		f := newSelectionRuntimeFixture(t, a)
		listErr := errors.New("synthetic reopen failure")
		f.repo.onList = func(round int) ([]Account, error) {
			if round == 1 {
				return []Account{a}, nil
			}
			return nil, listErr
		}
		f.svc.openaiProxyStreamCircuit = newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{
			failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: time.Minute, maxEntries: 4,
		})
		f.svc.openaiProxyStreamCircuit.recordFailure(9, time.Now())
		f.loads.acquireResults = map[int64]bool{1: false}
		selected, err := selectOrdinaryNewSessionForTest(t, f, WithOpenAISelectionNoWait(context.Background()), "ordinary", nil, false)
		require.ErrorIs(t, err, listErr)
		require.Nil(t, selected)
		require.Equal(t, 2, f.repo.lists)
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		require.Equal(t, float64(1), events[0]["pool_rounds"])
		require.Equal(t, []newSessionCandidateLog{{AccountID: 1, Placement: "front", Rank: 1, Skip: []string{"slot_unavailable"}}}, requireNewSessionLists(t, events[0], 0))
	})

	for _, takeSlot := range []bool{true, false} {
		name := "rescue cannot take a slot"
		if takeSlot {
			name = "only rescue takes a slot"
		}
		t.Run(name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			logs := captureSelectionNewSessionLog(t)
			ordinary, soft := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
			softPenalise(&soft, time.Now())
			f := newSelectionRuntimeFixture(t, ordinary, soft)
			f.loads.acquireResults = map[int64]bool{1: false, 2: takeSlot}
			ctx := WithOpenAISelectionNoWait(context.Background())
			selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "ordinary", nil, false)
			var winner int64
			if takeSlot {
				require.NoError(t, err)
				require.Equal(t, soft.ID, selected.Account.ID)
				defer selected.ReleaseFunc()
				winner = soft.ID
			} else {
				require.ErrorIs(t, err, ErrNoAvailableAccounts)
				require.Nil(t, selected)
			}
			events := selectionNewSessionEvents(t, logs.String())
			require.Len(t, events, 1)
			event := events[0]
			require.Equal(t, float64(1), event["pool_rounds"])
			require.Equal(t, true, event["rescue_attempted"])
			require.Equal(t, float64(2), event["front_or_trial_with_seat_count"])
			require.Equal(t, []newSessionCandidateLog{{AccountID: 1, Placement: "front", Rank: 1, Skip: []string{"slot_unavailable"}}}, requireNewSessionLists(t, event, winner))
			if takeSlot {
				require.Equal(t, map[string]any{"account_id": float64(2), "placement": "front"}, event["rescue"])
				require.Equal(t, float64(1), event["slot_unavailable_skipped_count"])
			} else {
				require.NotContains(t, event, "rescue")
				require.Equal(t, float64(2), event["slot_unavailable_skipped_count"])
			}
		})
	}

	t.Run("member read error leaves the pool unknown", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		f := newSelectionRuntimeFixture(t)
		listErr := errors.New("synthetic listing error")
		f.repo.onList = func(int) ([]Account, error) { return nil, listErr }
		selected, err := selectOrdinaryNewSessionForTest(t, f, WithOpenAISelectionNoWait(context.Background()), "ordinary", nil, false)
		require.ErrorIs(t, err, listErr)
		require.Nil(t, selected)
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		event := events[0]
		require.Equal(t, "candidate_read_failed", event["no_candidate_reason"])
		require.Equal(t, float64(0), event["pool_rounds"])
		require.Contains(t, event, "candidates")
		require.Contains(t, event, "excluded")
		require.Nil(t, event["candidates"])
		require.Nil(t, event["excluded"])
	})
}

type failingDispatchLogWriter struct{ attempts int }

func (w *failingDispatchLogWriter) Write([]byte) (int, error) {
	w.attempts++
	return 0, errors.New("synthetic log write failure")
}

func TestOpenAINewSessionDispatchLogFailureDoesNotChangeSelection(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	writer := &failingDispatchLogWriter{}
	for _, handler := range []slog.Handler{
		slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}), slog.NewJSONHandler(writer, nil),
	} {
		slog.SetDefault(slog.New(handler))
		if !handler.Enabled(context.Background(), slog.LevelInfo) {
			ctx, req := context.Background(), OpenAIAccountScheduleRequest{}
			require.Nil(t, newOpenAINewSessionOutcome(ctx, req))
			require.Zero(t, testing.AllocsPerRun(10, func() { newOpenAINewSessionOutcome(ctx, req) }))
		}
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		selected, err := selectOrdinaryNewSessionForTest(t, f, context.Background(), "ordinary", nil, false)
		require.NoError(t, err)
		require.Equal(t, int64(1), selected.Account.ID)
		selected.ReleaseFunc()
	}
	require.Positive(t, writer.attempts)
}

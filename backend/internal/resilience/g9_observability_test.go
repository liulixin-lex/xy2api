package resilience

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// The optional interface lets the identical regression run on the release
// baseline, where dispatch observations do not exist yet.
func observeG9Dispatch(st *RequestState) {
	if observer, ok := any(st).(interface{ ObserveAttemptStart() }); ok {
		observer.ObserveAttemptStart()
	}
}

func g9LogState(t *testing.T, mode string, abusive, billed bool) (*RequestState, []map[string]any) {
	t.Helper()
	withPublished(t, nil)
	var output bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	oldCheck := undeliveredAbuseCheck.Load()
	SetUndeliveredAbuseCheck(func(int64) bool { return abusive })
	t.Cleanup(func() { undeliveredAbuseCheck.Store(oldCheck) })
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "g9-request")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "g9-client")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t0 := time.Now().Add(-239 * time.Second)
	cfg := Defaults()
	cfg.Budget.BudgetStart = mode
	st := NewRequestState()
	st.SetGroup(53)
	st.SetAPIKey(42)
	st.InitDelivery(ctx, t0, nil)
	st.SetBudget(NewBudget(ctx, &cfg, KindText, t0))
	st.MarkBodyReceived(t0.Add(10*time.Second), "body_eof", 1024)
	at := time.Now()
	st.SetAttemptProfile(AttemptProfile{AttemptStart: at.Add(-time.Millisecond)})
	st.NoteAttemptTimer(at.Add(-time.Millisecond))
	st.ObserveAttemptStart()
	st.SetAttemptProfile(AttemptProfile{AttemptStart: at})
	st.ObserveAttemptStart()
	st.NoteUndeliveredUsage(nil, 0, billed)
	// Logs must retain the request snapshot even after a settings replacement.
	newer := Defaults()
	newer.Budget.BudgetStart = BudgetStartArrival
	if mode == BudgetStartArrival {
		newer.Budget.BudgetStart = BudgetStartBodyEOF
	}
	Publish(newer)
	cancel()
	disconnected, delivered, abuse := st.ResolveDisconnect()
	require.True(t, disconnected)
	require.False(t, delivered)
	require.Equal(t, abusive, abuse)
	st.ResolveDisconnect()
	records := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		records = append(records, record)
	}
	return st, records
}

func g9Record(t *testing.T, records []map[string]any, message string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, r := range records {
		if r["msg"] == message {
			found = append(found, r)
		}
	}
	require.Len(t, found, 1, "each warning is emitted once")
	return found[0]
}

func TestG9LateWarnIncludesDisconnectSnapshot(t *testing.T) {
	for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
		for _, billed := range []bool{false, true} {
			t.Run(mode+"/billed="+map[bool]string{false: "false", true: "true"}[billed], func(t *testing.T) {
				st, records := g9LogState(t, mode, true, billed)
				r := g9Record(t, records, "resilience.late_attempt_undelivered")
				require.Equal(t, "g9-request", r["request_id"])
				require.Equal(t, "g9-client", r["client_request_id"])
				require.Equal(t, mode, r["budget_start_mode"])
				require.Equal(t, mode, r["anchor_basis"])
				require.EqualValues(t, 2, r["late_attempt_count"])
				require.EqualValues(t, 1, r["late_attempt_no_timer_count"])
				require.Equal(t, true, r["abuse"])
				require.Equal(t, billed, r["billed"])
				require.EqualValues(t, st.d.disconnectAt.UnixMilli(), r["disconnect_unix_ms"])
				// Later billing must not mutate the already emitted disconnect snapshot.
				st.NoteUndeliveredUsage(nil, 1, !billed)
				require.Equal(t, billed, r["billed"])
			})
		}
	}
}

func TestG9AbuseWarnCarriesLateCountsAndFrozenMode(t *testing.T) {
	for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
		t.Run(mode, func(t *testing.T) {
			_, records := g9LogState(t, mode, true, false)
			r := g9Record(t, records, "resilience.abuse_guard")
			require.EqualValues(t, 2, r["late_attempt_count"])
			require.EqualValues(t, 1, r["late_attempt_no_timer_count"])
			require.Equal(t, mode, r["budget_start_mode"])
		})
	}
}

func g9MetricTotal(label string, group int64) int64 {
	var n int64
	for _, r := range Snapshot(time.Minute) {
		if r.Name == "late_attempt_start" && r.GroupID == group && r.Label == label {
			n += r.Count
		}
	}
	return n
}

func TestG9LateMetricVisibleBeforeFinishAndFrozenAtDispatch(t *testing.T) {
	withPublished(t, nil)
	for i, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
		for j, timed := range []bool{false, true} {
			t.Run(mode+"/timer="+map[bool]string{false: "false", true: "true"}[timed], func(t *testing.T) {
				group := int64(7300 + i*2 + j)
				label := map[bool]string{false: "false:", true: "true:"}[timed] + mode
				before := g9MetricTotal(label, group)
				cfg := Defaults()
				cfg.Budget.BudgetStart = mode
				now := time.Now()
				st := NewRequestState()
				st.SetGroup(group)
				b := NewBudget(context.Background(), &cfg, KindText, now.Add(-239*time.Second))
				st.SetBudget(b)
				owner := now
				profile := AttemptProfile{AttemptStart: owner, TimerOwnerStart: owner}
				st.SetAttemptProfile(profile)
				require.Equal(t, before, g9MetricTotal(label, group), "profile preparation alone is not a dispatch")
				if timed {
					st.NoteAttemptTimer(owner)
				}
				next := Defaults()
				next.Budget.BudgetStart = BudgetStartArrival
				if mode == BudgetStartArrival {
					next.Budget.BudgetStart = BudgetStartBodyEOF
				}
				Publish(next)
				deadline, attempts := b.Deadline(), b.Attempts()
				observeG9Dispatch(st)
				require.Equal(t, before+1, g9MetricTotal(label, group), "visible while request is unfinished")
				require.False(t, st.Finished())
				require.Equal(t, deadline, b.Deadline())
				require.Equal(t, attempts, b.Attempts())
				profile.AttemptStart = now.Add(time.Millisecond)
				profile.Warm = true
				st.SetAttemptProfile(profile)
				var wg sync.WaitGroup
				for k := 0; k < 8; k++ {
					wg.Add(1)
					go func() { defer wg.Done(); observeG9Dispatch(st) }()
				}
				wg.Wait()
				st.NoteAttemptTimer(owner)
				observeG9Dispatch(st)
				st.FinishTiming()
				st.FinishTiming()
				require.Equal(t, before+1, g9MetricTotal(label, group), "send/Warm updates, late timer and FinishTiming do not count again")
			})
		}
	}
}

func TestG9MetricExcludesNonLateAndNonText(t *testing.T) {
	now := time.Now()
	group := int64(7400)
	for _, kind := range []RequestKind{KindText, KindImage} {
		cfg := Defaults()
		st := NewRequestState()
		st.SetGroup(group)
		st.SetBudget(NewBudget(context.Background(), &cfg, kind, now))
		st.SetAttemptProfile(AttemptProfile{AttemptStart: now.Add(238 * time.Second)})
		observeG9Dispatch(st)
	}
	observeG9Dispatch(nil)
	observeG9Dispatch(NewRequestState())
	require.Zero(t, g9MetricTotal("false:arrival", group))
}

func TestG9ReceiptSourceUsesFirstReceiptIndependentlyOfAnchor(t *testing.T) {
	for _, source := range []string{"preread", "body_eof", "no_body", "handler_read"} {
		t.Run(source, func(t *testing.T) {
			now := time.Now()
			cfg := Defaults()
			st := NewRequestState()
			st.SetBudget(NewBudget(context.Background(), &cfg, KindText, now))
			var absent map[string]any
			require.NoError(t, json.Unmarshal([]byte(*st.TimingJSON()), &absent))
			require.NotContains(t, absent, "receipt_source")
			st.MarkBodyReceived(now.Add(2*time.Second), source, 1024)
			st.MarkBodyReceived(now.Add(3*time.Second), "later", 2048)
			for _, raw := range []string{*st.TimingJSON(), func() string { b, e := json.Marshal(st.Timing()); require.NoError(t, e); return string(b) }()} {
				var timing map[string]any
				require.NoError(t, json.Unmarshal([]byte(raw), &timing))
				require.Equal(t, source, timing["receipt_source"])
				require.Equal(t, "arrival", timing["anchor_basis"])
			}
		})
	}
}

func TestG9ObservationPreservesBudgetAndDisconnectDecisions(t *testing.T) {
	withPublished(t, nil)
	oldCheck := undeliveredAbuseCheck.Load()
	t.Cleanup(func() { undeliveredAbuseCheck.Store(oldCheck) })
	SetUndeliveredAbuseCheck(func(int64) bool { return true })
	for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
		t.Run(mode, func(t *testing.T) {
			t0 := time.Unix(2000, 0)
			cfg := Defaults()
			cfg.Budget.BudgetStart = mode
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := NewRequestState()
			st.InitDelivery(ctx, t0, nil)
			st.SetAPIKey(42)
			b := NewBudget(ctx, &cfg, KindText, t0)
			st.SetBudget(b)
			st.MarkBodyReceived(t0.Add(10*time.Second), "body_eof", 123)
			at := t0.Add(239 * time.Second)
			st.SetAttemptProfile(AttemptProfile{AttemptStart: at})
			p := st.PlanAttemptTimeout(30*time.Second, false, at)
			deadline, attempts := b.Deadline(), b.Attempts()
			can := b.CanAttempt(at)
			observeG9Dispatch(st)
			_ = st.TimingJSON()
			st.FinishTiming()
			require.Equal(t, deadline, b.Deadline())
			require.Equal(t, attempts, b.Attempts())
			require.Equal(t, can, b.CanAttempt(at))
			cancel()
			gone, delivered, abuse := st.ResolveDisconnect()
			require.True(t, gone)
			require.False(t, delivered)
			require.True(t, abuse)
			require.False(t, p.Applied())
			t.Logf("BEHAVIOR mode=%s deadline_ms=%d attempts=%d can=%t effective_ms=%d applied=%t disconnected=%t delivered=%t abuse=%t", mode, deadline.Sub(t0).Milliseconds(), attempts, can, p.Effective().Milliseconds(), p.Applied(), gone, delivered, abuse)
		})
	}
}

func TestG9OpenAIDispatchCoversProfilesWithoutRepairReset(t *testing.T) {
	cfg := Defaults()
	cfg.Budget.BudgetStart = BudgetStartBodyEOF
	now := time.Now()
	st := NewRequestState()
	st.SetGroup(7603)
	st.SetBudget(NewBudget(context.Background(), &cfg, KindText, now.Add(-239*time.Second)))
	// Non-native OpenAI protocol branches retain the handler's profile rather
	// than passing through resetOpenAIRepairAttempt in the native main loop.
	st.SetAttemptProfile(AttemptProfile{AttemptStart: now})
	before := g9MetricTotal("false:body_eof", 7603)
	deadline := st.Budget().Deadline()
	st.NoteOpenAIDispatch(now, "direct")
	require.Equal(t, before+1, g9MetricTotal("false:body_eof", 7603))
	st.NoteOpenAIDispatch(now.Add(time.Millisecond), "plugin")
	require.Equal(t, before+1, g9MetricTotal("false:body_eof", 7603), "same owner is counted once across transport observations")
	require.EqualValues(t, 2, *st.Timing().DispatchCount)
	require.Equal(t, deadline, st.Budget().Deadline())
	require.Zero(t, st.Budget().Attempts())
}

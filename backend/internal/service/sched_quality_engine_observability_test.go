//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type qualityCycleOutcomeStore struct {
	*fakeSchedQualityStore
	miss bool
	err  error
}

func (s *qualityCycleOutcomeStore) RunCycle(ctx context.Context, fn func(SchedQualityTx) error) (bool, error) {
	if s.miss {
		return false, nil
	}
	return s.fakeSchedQualityStore.RunCycle(ctx, func(tx SchedQualityTx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return s.err
	})
}

func (s *qualityCycleOutcomeStore) LoadRows(ctx context.Context) ([]q.StateRow, []SchedOverride, map[int64]time.Time, error) {
	if s.err != nil {
		return nil, nil, nil, s.err
	}
	return s.fakeSchedQualityStore.LoadRows(ctx)
}

func TestSchedQualityEngineCycleObservability(t *testing.T) {
	for _, stage := range []string{"success", "ledger", "tests", "rollback", "rollback_release", "follower", "follower_read"} {
		t.Run(stage, func(t *testing.T) {
			logs := captureSchedObsLogs(t)
			f := newQualityFixture(t)
			obs := NewSchedObservabilityService()
			f.e.obs = obs
			failure := errors.New("private cycle failure")
			switch stage {
			case "ledger":
				f.e.attempts = &qualityPageStore{fakeSchedAttemptStore: f.ledger, read: func(time.Time, int) ([]q.AttemptSample, time.Time, error) {
					return nil, time.Time{}, failure
				}}
			case "tests":
				f.runner.Err = failure
			case "rollback":
				f.samples(16)
				f.e.store = &qualityCycleOutcomeStore{fakeSchedQualityStore: f.store, err: failure}
			case "rollback_release":
				f.store.Rows = []q.StateRow{{AccountID: 1, Model: "gpt-6.1-sol", GroupID: 10, State: q.StateCooling, Level: 1, Reason: q.ReasonSpeed, Since: f.now.Add(-5 * time.Minute), Until: f.now}}
				f.switches.tests = false
				f.e.store = &qualityCycleOutcomeStore{fakeSchedQualityStore: f.store, err: failure}
			case "follower", "follower_read":
				s := &qualityCycleOutcomeStore{fakeSchedQualityStore: f.store, miss: true}
				if stage == "follower_read" {
					s.err = failure
				}
				f.e.store = s
			}
			calls := 0
			f.e.now = func() time.Time {
				calls++
				if calls == 1 {
					return f.now
				}
				return f.now.Add(125 * time.Millisecond)
			}
			err := f.e.cycle(context.Background())
			failed := stage != "success" && stage != "follower"
			if failed {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
			}
			if stage == "rollback_release" {
				require.Len(t, f.store.Rows, 1)
				require.Equal(t, q.StateCooling, f.store.Rows[0].State)
			} else {
				require.Empty(t, f.store.Rows)
			}
			require.Empty(t, logs.String(), "normal cycles and rolled-back transitions do not emit events")
			obs.summary(context.Background())
			wantFailed := float64(0)
			if failed {
				wantFailed = 1
			}
			require.Equal(t, []map[string]any{{
				"level": "INFO", "msg": "sched_quality.summary", "component": "sched_quality",
				"cycles": float64(1), "cycles_failed": wantFailed, "cycle_ms_max": float64(125),
				"tests": float64(0), "tests_aborted": float64(0), "cooled": float64(0), "released": float64(0),
			}}, schedObsLogRecords(t, logs))
		})
	}
}

func TestSchedQualityEngineCommittedTransitions(t *testing.T) {
	f := newQualityFixture(t)
	obs := NewSchedObservabilityService()
	f.e.obs = obs
	f.cool()
	require.EqualValues(t, 1, obs.window.cooled)
	f.cycle()
	require.EqualValues(t, 1, obs.window.cooled, "an unchanged cooling row is not another transition")
	f.due()
	f.test(2, false)
	f.cycle()
	require.EqualValues(t, 2, obs.window.cooled, "recooling counts once")
	f.due()
	f.test(0, false)
	f.cycle()
	require.EqualValues(t, 1, obs.window.released)
	f.now = f.now.Add(q.LevelDecayAfter)
	f.cycle()
	require.Zero(t, f.view(10).Level)
	require.EqualValues(t, 2, obs.window.cooled)
	require.EqualValues(t, 1, obs.window.released, "level decay is not a release")
	require.EqualValues(t, 5, obs.window.cycles)
}

func TestSchedQualityEngineFiveGroupsRequestOnce(t *testing.T) {
	f := newQualityFixture(t)
	for group := int64(10); group < 15; group++ {
		f.store.Members[group] = []int64{1}
		f.store.Policies[group] = SchedPolicyRecord{GroupID: group, Policy: f.store.Policies[10].Policy}
		f.store.Rows = append(f.store.Rows, q.StateRow{AccountID: 1, Model: "gpt-6.1-sol", GroupID: group, State: q.StateCooling, Level: 1, Reason: q.ReasonSpeed, Since: f.now.Add(-5 * time.Minute), Until: f.now})
	}
	f.cycle()
	require.Equal(t, []fakeSchedTestRequest{{AccountID: 1, Model: "gpt-6.1-sol", Auto: true}}, f.runner.Requests)
	require.Len(t, f.e.Snapshot().GroupRows[1], 5)
	for _, row := range f.e.Snapshot().GroupRows[1] {
		require.True(t, row.View.WaitingTest)
	}
}

func TestSchedQualityEngineStateEvents(t *testing.T) {
	for _, via := range []q.Via{q.ViaGateOff, q.ViaRealPass, q.ViaTestPass, q.ViaScreenClean, q.ViaTestsOff, q.ViaWaitMax, q.ViaReset} {
		t.Run(string(via), func(t *testing.T) {
			logs := captureSchedObsLogs(t)
			f := newQualityFixture(t)
			f.cool()
			cooled := schedObsLogRecords(t, logs)
			require.Len(t, cooled, 1)
			require.Equal(t, f.view(10).Until.Format(time.RFC3339Nano), cooled[0]["until"])
			require.NotContains(t, cooled[0], "via")
			f.due()
			switch via {
			case q.ViaGateOff:
				p := f.store.Policies[10]
				p.Policy.Enabled = false
				f.store.Policies[10] = p
			case q.ViaRealPass:
				f.samples(0)
				f.test(0, false)
			case q.ViaTestPass:
				f.test(0, false)
			case q.ViaScreenClean:
				f.test(0, false)
				pair := SchedAccountModel{1, "gpt-6.1-sol"}
				f.runner.Records[pair][0].Turns = f.runner.Records[pair][0].Turns[:3]
			case q.ViaTestsOff:
				f.switches.tests = false
				f.now = f.now.Add(q.TestWaitMax)
			case q.ViaWaitMax:
				f.now = f.now.Add(q.TestWaitMax)
			}
			logs.Reset()
			if via == q.ViaReset {
				_, err := f.e.Reset(context.Background(), 1)
				require.NoError(t, err)
			} else {
				f.cycle()
			}
			require.Equal(t, q.StateNormal, f.view(10).State)
			released := schedObsLogRecords(t, logs)
			require.Len(t, released, 1)
			require.Equal(t, "sched_quality.state", released[0]["msg"])
			require.Equal(t, string(via), released[0]["via"])
			require.NotContains(t, released[0], "until")
			delete(cooled[0], "until")
			delete(released[0], "via")
			require.ElementsMatch(t, qualityEventKeys(cooled[0]), qualityEventKeys(released[0]))
		})
	}
	t.Run("recooling", func(t *testing.T) {
		f := newQualityFixture(t)
		f.cool()
		f.due()
		f.test(2, false)
		logs := captureSchedObsLogs(t)
		f.cycle()
		events := schedObsLogRecords(t, logs)
		require.Len(t, events, 1)
		require.Equal(t, f.view(10).Until.Format(time.RFC3339Nano), events[0]["until"])
		require.NotContains(t, events[0], "via")
	})
}

func qualityEventKeys(event map[string]any) []string {
	keys := make([]string, 0, len(event))
	for key := range event {
		keys = append(keys, key)
	}
	return keys
}

type qualityCleanupStore struct {
	*fakeSchedAttemptStore
	delete func(context.Context, time.Time) (int64, error)
}

func (s *qualityCleanupStore) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	return s.delete(ctx, before)
}

type qualityReapRunner struct {
	*fakeSchedTestRunner
	reap func(context.Context) (int64, error)
}

func (r *qualityReapRunner) ReapStale(ctx context.Context) (int64, error) { return r.reap(ctx) }

func TestSchedQualityEngineCleanupEvents(t *testing.T) {
	for _, kind := range []string{"ledger", "stale"} {
		for _, tc := range []struct {
			name     string
			rows     int64
			err      error
			category string
		}{
			{"deleted", 4, nil, ""}, {"empty", 0, nil, ""},
			{"database", 0, errors.New("private database details"), "db"},
			{"timeout", 0, fmt.Errorf("private wrapper: %w", context.DeadlineExceeded), "timeout"},
			{"internal", 0, fmt.Errorf("private wrapper: %w", context.Canceled), "internal"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				logs := captureSchedObsLogs(t)
				f := newQualityFixture(t)
				started := f.now
				if kind == "ledger" {
					for i := int64(0); i < tc.rows; i++ {
						f.ledger.Rows = append(f.ledger.Rows, q.AttemptSample{StartedAt: started.Add(-q.LedgerRetention - time.Second)})
					}
					f.e.attempts = &qualityCleanupStore{fakeSchedAttemptStore: f.ledger, delete: func(_ context.Context, before time.Time) (int64, error) {
						require.Equal(t, started.Add(-q.LedgerRetention), before)
						f.now = f.now.Add(25 * time.Millisecond)
						if tc.err != nil {
							return 0, tc.err
						}
						return f.ledger.DeleteBefore(context.Background(), before)
					}}
					f.e.cleanup(context.Background())
				} else {
					f.runner.ReapRows = tc.rows
					f.e.runner = &qualityReapRunner{fakeSchedTestRunner: f.runner, reap: func(ctx context.Context) (int64, error) {
						f.now = f.now.Add(25 * time.Millisecond)
						rows, _ := f.runner.ReapStale(ctx)
						return rows, tc.err
					}}
					f.cycle()
				}
				if kind == "stale" && tc.rows == 0 && tc.err == nil {
					require.Empty(t, logs.String())
					return
				}
				want := map[string]any{"level": "INFO", "msg": "sched_quality.cleanup", "component": "sched_quality", "kind": kind, "rows": float64(tc.rows), "duration_ms": float64(25)}
				if tc.err != nil {
					want["level"], want["error"] = "ERROR", tc.category
				}
				require.Equal(t, []map[string]any{want}, schedObsLogRecords(t, logs))
				require.NotContains(t, logs.String(), "private")
			})
		}
	}
}

func TestSchedQualityEngineCleanupOnlyWhenExecuted(t *testing.T) {
	f := newQualityFixture(t)
	f.e.lockCache = schedTestLeaderLock{acquired: false}
	f.e.attempts = &qualityCleanupStore{fakeSchedAttemptStore: f.ledger, delete: func(context.Context, time.Time) (int64, error) {
		t.Fatal("ledger cleanup must not run without the lock")
		return 0, nil
	}}
	f.e.store = &qualityCycleOutcomeStore{fakeSchedQualityStore: f.store, miss: true}
	f.e.runner = &qualityReapRunner{fakeSchedTestRunner: f.runner, reap: func(context.Context) (int64, error) {
		t.Fatal("a follower must not reap tests")
		return 0, nil
	}}
	logs := captureSchedObsLogs(t)
	f.e.cleanup(context.Background())
	f.cycle()
	require.Empty(t, logs.String())
}

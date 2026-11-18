//go:build unit

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestFirstOutputFollowBorrowPersistsThroughSlog(t *testing.T) {
	withResilienceSettings(t, nil)
	previous := slog.Default()
	require.NoError(t, logger.Init(logger.InitOptions{Level: "info", Format: "json", ServiceName: "follow-test", Environment: "test",
		Output: logger.OutputOptions{ToStdout: true}}))
	t.Cleanup(func() {
		logger.SetSink(nil)
		slog.SetDefault(previous)
	})
	var saved []*OpsInsertSystemLogInput
	sink := NewOpsSystemLogSink(&opsRepoMock{BatchInsertSystemLogsFn: func(_ context.Context, rows []*OpsInsertSystemLogInput) (int64, error) {
		saved = append(saved, rows...)
		return int64(len(rows)), nil
	}})
	defer sink.Stop()
	sink.SetPersistAccessLogs(false)
	logger.SetSink(sink)

	var borrowID string
	for _, scope := range []string{"session", firstOutputAvoidSession("session")} {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(11, 2))
		f.cache.sessionBindings["openai:session"] = 10
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), st)
		require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, scope, 10, time.Minute))
		sel := selectTrialRuntime(t, f, ctx, "session")
		require.EqualValues(t, 11, sel.Account.ID)
		sel.ReleaseFunc()
		if scope == "session" {
			require.Equal(t, "owner_excluded", st.StickyBypassReason())
		} else {
			borrowID = st.GatewayRequestID()
		}
	}
	var batch []*logger.LogEvent
	for len(sink.queue) > 0 {
		batch = append(batch, <-sink.queue)
	}
	written, err := sink.flushBatch(context.Background(), batch)
	require.NoError(t, err)
	require.Equal(t, len(batch), written)
	var bypass []*OpsInsertSystemLogInput
	for _, row := range saved {
		if row.Message == "openai.sticky_bypass" {
			bypass = append(bypass, row)
		}
	}
	require.Len(t, bypass, 1)
	row := bypass[0]
	require.Equal(t, "info", row.Level)
	require.Equal(t, "sched_quality", row.Component)
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.ExtraJSON), &fields))
	require.Equal(t, "first_output_avoid", fields["reason"])
	require.Equal(t, borrowID, fields["gateway_request_id"])
	require.Equal(t, float64(10), fields["owner"])
	require.Equal(t, float64(11), fields["selected"])
}

type followAccountReadBarrier struct {
	AccountRepository
	hold func()
}

type followAccountReads struct {
	AccountRepository
	reads map[int64]int
}

func (r *followAccountReads) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.reads[id]++
	return r.AccountRepository.GetByID(ctx, id)
}

func followSnapshotFixture(t *testing.T) (*selectionRuntimeFixture, *followAccountReads) {
	t.Helper()
	x, y := selectionFixtureAccount(10, 1), selectionFixtureAccount(11, 2)
	f := newSelectionRuntimeFixture(t, x, y)
	f.cache.sessionBindings["openai:session"] = x.ID
	reads := &followAccountReads{AccountRepository: f.repo, reads: make(map[int64]int)}
	f.svc.accountRepo = reads
	f.svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&x, &y}, accountsByID: map[int64]*Account{x.ID: &x, y.ID: &y},
	}}
	return f, reads
}

func TestFirstOutputFollowOwnerReadsBeforeDelivery(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name                 string
		readX, readY         int
		selected, finalOwner int64
	}{
		{"no_avoid", 1, 0, 10, 10},
		{"first_output", 1, 1, 11, 10},
		{"strict_overlap", 0, 1, 11, 10},
		{"request_overlap", 0, 1, 11, 10},
		{"disabled_only_in_database", 1, 1, 11, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, repo := followSnapshotFixture(t)
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			if tc.name != "no_avoid" {
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), 10, time.Minute))
			}
			var excluded map[int64]struct{}
			switch tc.name {
			case "strict_overlap":
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, stickyChangeAvoidSession("session"), 10, time.Minute))
			case "request_overlap":
				excluded = map[int64]struct{}{10: {}}
			case "disabled_only_in_database":
				f.repo.accounts[0].Schedulable = false
			}
			sel, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", excluded,
				OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
			require.NoError(t, err)
			require.NotNil(t, sel)
			defer sel.ReleaseFunc()
			require.Equal(t, tc.selected, sel.Account.ID)
			require.Equal(t, tc.readX, repo.reads[10])
			require.Equal(t, tc.readY, repo.reads[11])
			followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
			require.Equal(t, tc.finalOwner, f.cache.sessionBindings["openai:session"])
		})
	}
}

func (r followAccountReadBarrier) GetByID(ctx context.Context, id int64) (*Account, error) {
	account, err := r.AccountRepository.GetByID(ctx, id)
	r.hold()
	return account, err
}

func TestFirstOutputFollowSwitchDuringRecheck(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled_during_read"} {
		t.Run(mode, func(t *testing.T) {
			withResilienceSettings(t, nil)
			synctest.Test(t, func(t *testing.T) {
				f := selectionNewSessionFixture()
				rt := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(1, 2))
				ctx := followState(t, f, true)
				y := &rt.repo.accounts[1]
				followSelect(rt.svc, ctx, y)
				entered, resume := make(chan struct{}), make(chan struct{})
				rt.svc.accountRepo = followAccountReadBarrier{rt.repo, sync.OnceFunc(func() {
					close(entered)
					<-resume
				})}
				logs := captureOpenAIObservationLogs(t)
				done := make(chan struct{})
				go func() {
					followDelivery(rt.svc, ctx, y, time.Millisecond, &OpenAIForwardResult{})
					close(done)
				}()
				<-entered
				if mode == "disabled_during_read" {
					settings := *resilience.Current()
					settings.Session.Enabled = false
					resilience.Publish(settings)
				}
				close(resume)
				<-done
				FinishOpenAIStickyForward(ctx, y.ID, &OpenAIForwardResult{}, nil)
				events := schedObsLogRecords(t, logs)
				require.Len(t, events, 1)
				require.Equal(t, true, events[0]["faster_complete"])
				require.Equal(t, mode == "enabled", events[0]["commit_attempted"])
				if mode == "enabled" {
					require.Len(t, f.binding.casKeys, 1)
					require.Equal(t, y.ID, f.binding.owner)
				} else {
					require.Empty(t, f.binding.casKeys)
					require.Equal(t, f.owner.ID, f.binding.owner)
					require.Equal(t, "session_disabled", events[0]["reason"])
				}
			})
		})
	}
}

func TestFirstOutputFollowSimpleModeFullPool(t *testing.T) {
	withResilienceSettings(t, nil)
	y := selectionFixtureAccount(11, 2)
	y.GroupIDs = []int64{99}
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), y)
	f.svc.cfg.RunMode = config.RunModeSimple
	const key = "openai:session"
	f.cache.sessionBindings[key] = 10
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	x := selectTrialRuntime(t, f, ctx, "session")
	require.EqualValues(t, 10, x.Account.ID)
	x.ReleaseFunc()
	change := pendingOpenAIStickyChange(ctx)
	fact := resilience.OwnerFirstOutputCutoff{GroupID: 7, Session: "session", BindingKey: change.key, AccountID: x.Account.ID,
		Generation: change.expected.Generation, AttemptSeq: st.MarkOpsUpstreamSendBoundary(x.Account.ID), FiredAt: time.Now(), Effective: time.Second, CanTransfer: change.canTransfer}
	require.True(t, st.RecordOwnerCutoff(fact))
	recordFirstOutputAvoid(ctx, fact)
	sel, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", map[int64]struct{}{x.Account.ID: {}},
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
	require.NoError(t, err)
	require.NotNil(t, sel)
	defer sel.ReleaseFunc()
	require.Equal(t, y.ID, sel.Account.ID)
	require.Equal(t, stickyFollow, pendingOpenAIStickyChange(ctx).kind)
	followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
	require.Equal(t, y.ID, f.cache.sessionBindings[key])
	require.Equal(t, []string{key}, f.cache.casKeys)
}

func TestFirstOutputFollowOwnerReleasePrecedesBorrow(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, reason   string
		healthy, avoid bool
		owner          int64
	}{
		{"hard_release", "owner_should_clear", false, false, 11},
		{"hard_release_with_avoid", "owner_should_clear", false, true, 11},
		{"healthy_borrow", "first_output_avoid", true, true, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := selectionFixtureAccount(10, 1)
			x.Schedulable = tc.healthy
			f := newSelectionRuntimeFixture(t, x, selectionFixtureAccount(11, 2))
			const key = "openai:session"
			f.cache.sessionBindings[key] = x.ID
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			if tc.avoid {
				require.NoError(t, f.cache.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession("session"), x.ID, time.Minute))
			}
			logs := captureOpenAIObservationLogs(t)
			sel := selectTrialRuntime(t, f, ctx, "session")
			defer sel.ReleaseFunc()
			require.EqualValues(t, 11, sel.Account.ID)
			require.Equal(t, tc.reason, st.StickyBypassReason())
			require.Contains(t, logs.String(), `"reason":"`+tc.reason+`"`)
			followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
			require.Equal(t, tc.owner, f.cache.sessionBindings[key])
			if tc.healthy {
				require.Empty(t, f.cache.casKeys)
			} else {
				require.Equal(t, "owner_released", pendingOpenAIStickyChange(ctx).kind)
				require.Equal(t, []string{key}, f.cache.casKeys)
			}
		})
	}
}

func TestFirstOutputFollowStickyOwnerEligibilityOrder(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"model", "session_hash"} {
		for _, tc := range []struct {
			name, reason         string
			healthy, firstOutput bool
		}{
			{"eligible", "first_output_avoid", true, true},
			{"disabled", "owner_should_clear", false, true},
			{"ordinary_exclusion", "owner_excluded", false, false},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				x := selectionFixtureAccount(10, 1)
				x.Schedulable = tc.healthy
				f := newSelectionRuntimeFixture(t, x)
				f.cache.sessionBindings["openai:session"] = x.ID
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				group := int64(7)
				f.svc.prepareOpenAIStickyChange(ctx, &group, "session")
				if tc.firstOutput {
					ctx = context.WithValue(ctx, firstOutputOnlyOwnerKey{}, x.ID)
				}
				ex := map[int64]struct{}{x.ID: {}}
				if path == "model" {
					require.Nil(t, f.svc.tryStickySessionHit(ctx, &group, PlatformOpenAI, "session", "m", ex, false, x.ID, OpenAIEndpointCapabilityResponses))
				} else {
					sel, _, err := f.svc.selectBySessionHash(ctx, OpenAIAccountScheduleRequest{
						GroupID: &group, Platform: PlatformOpenAI, SessionHash: "session", RequestedModel: "m", StickyAccountID: x.ID, ExcludedIDs: ex,
						RequiredTransport: OpenAIUpstreamTransportHTTPSSE, RequiredCapability: OpenAIEndpointCapabilityResponses,
					})
					require.NoError(t, err)
					require.Nil(t, sel)
				}
				require.Equal(t, tc.reason, st.StickyBypassReason())
				require.Equal(t, tc.reason == "owner_should_clear", pendingOpenAIStickyChange(ctx).ownerReleased)
				require.Empty(t, f.attempts)
			})
		}
	}
}

func TestFirstOutputFollowEmptyScopePreservesBindingChanges(t *testing.T) {
	withResilienceSettings(t, nil)
	const key = "openai:session"
	for _, kind := range []string{"create", "owner_released", "takeover"} {
		t.Run(kind, func(t *testing.T) {
			type outcome struct {
				selected, target, owner int64
				kind, reason            string
				slots                   []int64
				cas                     []string
			}
			run := func(readScope bool) outcome {
				x := selectionFixtureAccount(10, 5)
				x.Schedulable = kind != "owner_released"
				f := newSelectionRuntimeFixture(t, x, selectionFixtureAccount(11, 1))
				if kind != "create" {
					f.cache.sessionBindings[key] = x.ID
				}
				ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
				if !readScope {
					ctx = WithOpenAITransferProtected(ctx)
				}
				sel := selectTrialRuntime(t, f, ctx, "session")
				defer sel.ReleaseFunc()
				change := pendingOpenAIStickyChange(ctx)
				followDelivery(f.svc, ctx, sel.Account, time.Millisecond, &OpenAIForwardResult{})
				return outcome{sel.Account.ID, change.target, f.cache.sessionBindings[key], change.kind, change.reason, f.attempts, f.cache.casKeys}
			}
			before := run(false)
			require.Equal(t, kind, before.kind)
			require.EqualValues(t, 11, before.owner)
			require.Equal(t, []string{key}, before.cas)
			require.Equal(t, before, run(true))
		})
	}
}

//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/stretchr/testify/require"
)

// flakyListMonitorSvc 前 failures 次 ListEnabledMonitors 返回错误，之后返回 enabled。
type flakyListMonitorSvc struct {
	stubMonitorSvc
	failures atomic.Int64
	mu       sync.Mutex
	list     []*ChannelMonitor
}

func (s *flakyListMonitorSvc) ListEnabledMonitors(_ context.Context) ([]*ChannelMonitor, error) {
	if s.failures.Add(-1) >= 0 {
		return nil, errors.New("db not ready")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list, nil
}

func TestStart_RetriesUntilInitialLoadSucceeds(t *testing.T) {
	svc := &flakyListMonitorSvc{list: []*ChannelMonitor{{ID: 1, Enabled: true, IntervalSeconds: 60}}}
	svc.failures.Store(2)
	r := newRunnerForTest(svc)
	r.retryInitial = 10 * time.Millisecond
	r.Start()

	waitFor(t, 2*time.Second, "task scheduled after load retries", func() bool { return runnerTaskCount(r) == 1 })
	stoppedWithin(t, r, 3*time.Second)
}

func TestReconcile_AddsMissingAndRemovesStaleTasks(t *testing.T) {
	svc := &stubMonitorSvc{enabled: []*ChannelMonitor{
		{ID: 1, Enabled: true, IntervalSeconds: 60},
		{ID: 2, Enabled: true, IntervalSeconds: 60},
	}}
	r := newRunnerForTest(svc)
	r.Start()
	waitFor(t, time.Second, "initial tasks", func() bool { return runnerTaskCount(r) == 2 })

	added, removed := r.reconcile(time.Now(), []*ChannelMonitor{
		{ID: 2, Enabled: true, IntervalSeconds: 60},
		{ID: 3, Enabled: true, IntervalSeconds: 60},
		{ID: 4, Enabled: true, IntervalSeconds: 60, APIKeyDecryptFailed: true},
	})
	require.Equal(t, 1, added)
	require.Equal(t, 1, removed)
	require.Nil(t, runnerTaskPtr(r, 1))
	require.NotNil(t, runnerTaskPtr(r, 2))
	require.NotNil(t, runnerTaskPtr(r, 3))
	require.Nil(t, runnerTaskPtr(r, 4))

	stoppedWithin(t, r, 3*time.Second)
}

// 对账与 CRUD 回调竞态：快照之后才建立或撤销的任务，对账不得改动。
func TestReconcile_RespectsChangesAfterSnapshot(t *testing.T) {
	r := newRunnerForTest(&stubMonitorSvc{})
	r.Start()

	snapshot := time.Now()
	time.Sleep(2 * time.Millisecond)
	r.Schedule(&ChannelMonitor{ID: 7, Enabled: true, IntervalSeconds: 60}) // 快照后新建，库快照里还没有
	r.Unschedule(8)                                                        // 快照后删除，库快照里还有

	added, removed := r.reconcile(snapshot, []*ChannelMonitor{{ID: 8, Enabled: true, IntervalSeconds: 60}})
	require.Zero(t, added)
	require.Zero(t, removed)
	require.NotNil(t, runnerTaskPtr(r, 7))
	require.Nil(t, runnerTaskPtr(r, 8))

	stoppedWithin(t, r, 3*time.Second)
}

// claimingMonitorSvc 额外实现 monitorCheckClaimer。
type claimingMonitorSvc struct {
	stubMonitorSvc
	claim    bool
	claimErr error
	gaps     chan time.Duration
}

func (s *claimingMonitorSvc) ClaimScheduledCheck(_ context.Context, _ int64, minGap time.Duration) (bool, error) {
	select {
	case s.gaps <- minGap:
	default:
	}
	return s.claim, s.claimErr
}

func TestRunOne_SkipsRoundClaimedByAnotherInstance(t *testing.T) {
	svc := &claimingMonitorSvc{claim: false, gaps: make(chan time.Duration, 1)}
	r := newRunnerForTest(svc)
	r.Start()
	r.scheduleTask(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60, JitterSeconds: 10}, 0, false)

	select {
	case gap := <-svc.gaps:
		require.Equal(t, 40*time.Second, gap) // 0.8 × (60 - 10)
	case <-time.After(2 * time.Second):
		t.Fatal("claim was never attempted")
	}
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, svc.runCount.Load(), "claimed elsewhere: RunCheck must not run")
	stoppedWithin(t, r, 3*time.Second)
}

func TestRunOne_ChecksWhenClaimErrors(t *testing.T) {
	svc := &claimingMonitorSvc{claimErr: errors.New("db down"), gaps: make(chan time.Duration, 1)}
	svc.runCalled = make(chan int64, 1)
	r := newRunnerForTest(svc)
	r.Start()
	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})

	select {
	case <-svc.runCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("claim error must fail open and still run the check")
	}
	stoppedWithin(t, r, 3*time.Second)
}

func TestMonitorScheduleHelpers(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := monitorStartupStagger(30 * time.Second)
		require.True(t, d >= 0 && d <= 30*time.Second, d)
		d = monitorStartupStagger(10 * time.Minute)
		require.True(t, d >= 0 && d <= monitorStartupStaggerMax, d)
	}
	require.Zero(t, monitorStartupStagger(0))

	require.Equal(t, 96*time.Second, monitorClaimGap(120*time.Second, 0))
	require.Equal(t, 12*time.Second, monitorClaimGap(20*time.Second, 10*time.Second)) // 下限 15s × 0.8

	now := time.Now()
	require.False(t, monitorQueuedTooLong(now.Add(-29*time.Second), now, time.Minute))
	require.True(t, monitorQueuedTooLong(now.Add(-31*time.Second), now, time.Minute))

	require.Equal(t, 10*time.Second, nextMonitorRetryDelay(5*time.Second))
	require.Equal(t, monitorStartupRetryMax, nextMonitorRetryDelay(4*time.Minute))
}

// --- RunCheck：并发合并、取消不落库、last_checked_at 记本轮开始时间 ---

type markTimeRepoStub struct {
	quotaModeRepoStub
	mu        sync.Mutex
	markTimes []time.Time
}

func (r *markTimeRepoStub) InsertHistoryBatch(ctx context.Context, rows []*ChannelMonitorHistoryRow) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quotaModeRepoStub.InsertHistoryBatch(ctx, rows)
}

func (r *markTimeRepoStub) MarkChecked(ctx context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markTimes = append(r.markTimes, at)
	return r.quotaModeRepoStub.MarkChecked(ctx, id, at)
}

func newBlockingQuotaMonitorService(t *testing.T, usage *stubMonitorUsageSource) (*ChannelMonitorService, *markTimeRepoStub) {
	t.Helper()
	repo := &markTimeRepoStub{quotaModeRepoStub: quotaModeRepoStub{monitor: &ChannelMonitor{
		ID:              1,
		Name:            "claude-quota",
		Provider:        MonitorProviderAnthropic,
		PrimaryModel:    "quota",
		Enabled:         true,
		IntervalSeconds: 60,
		CheckMode:       MonitorCheckModeQuota,
		AccountID:       int64Ptr(9),
	}}}
	svc := NewChannelMonitorService(repo, &duplicateChannelMonitorEncryptor{})
	svc.SetRuntimeReader(channelMonitorRuntimeStub{rt: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV1}})
	svc.SetQuotaFetcher(newQuotaModeFetcher(map[int64]*Account{
		9: {ID: 9, Platform: domain.PlatformAnthropic, Type: AccountTypeOAuth},
	}, usage))
	return svc, repo
}

func TestRunCheck_ConcurrentCallsShareOneRound(t *testing.T) {
	usage := &stubMonitorUsageSource{usage: &UsageInfo{}, block: make(chan struct{})}
	svc, repo := newBlockingQuotaMonitorService(t, usage)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.RunCheck(context.Background(), 1)
		}(i)
	}
	waitFor(t, time.Second, "first round started", func() bool { return usage.getCalls() == 1 })
	time.Sleep(20 * time.Millisecond) // 让第二个调用进入合并等待
	close(usage.block)
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Len(t, repo.history, 1, "the manual check must reuse the in-flight round")
	require.Len(t, repo.markTimes, 1)
}

func TestRunCheck_CancelledRoundIsNotPersisted(t *testing.T) {
	usage := &stubMonitorUsageSource{usage: &UsageInfo{}}
	svc, repo := newBlockingQuotaMonitorService(t, usage)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.RunCheck(ctx, 1)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, repo.history)
	require.Empty(t, repo.markTimes)
}

func TestRunCheck_MarksRoundStartTime(t *testing.T) {
	usage := &stubMonitorUsageSource{usage: &UsageInfo{}, block: make(chan struct{})}
	svc, repo := newBlockingQuotaMonitorService(t, usage)

	before := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.RunCheck(context.Background(), 1)
	}()
	waitFor(t, time.Second, "round started", func() bool { return usage.getCalls() == 1 })
	time.Sleep(100 * time.Millisecond)
	close(usage.block)
	<-done

	require.Len(t, repo.markTimes, 1)
	require.True(t, repo.markTimes[0].Before(before.Add(50*time.Millisecond)),
		"last_checked_at must be the round start, not its end")
}

// 新建、编辑、启用触发的首轮不受认领间隔限制，之后各轮照常认领。
func TestSchedule_FirstRoundAfterEditIsForced(t *testing.T) {
	svc := &claimingMonitorSvc{claim: true, gaps: make(chan time.Duration, 4)}
	r := newRunnerForTest(svc)
	r.Start()
	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})

	select {
	case gap := <-svc.gaps:
		require.Zero(t, gap, "the first round after an edit must not wait for the claim gap")
	case <-time.After(2 * time.Second):
		t.Fatal("claim was never attempted")
	}
	task := runnerTaskPtr(r, 1)
	require.NotNil(t, task)
	require.False(t, task.forceNext.Load(), "only the first round is forced")
	stoppedWithin(t, r, 3*time.Second)
}

func TestRunCheckManual_SurvivesCallerCancelAndKeepsLastCheckedAt(t *testing.T) {
	usage := &stubMonitorUsageSource{usage: &UsageInfo{}, block: make(chan struct{})}
	svc, repo := newBlockingQuotaMonitorService(t, usage)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := svc.RunCheckManual(ctx, 1)
		done <- err
	}()
	waitFor(t, time.Second, "manual round started", func() bool { return usage.getCalls() == 1 })
	cancel() // 浏览器断开
	close(usage.block)
	require.NoError(t, <-done)

	require.Len(t, repo.history, 1, "the manual round still completes and is written")
	require.Empty(t, repo.markTimes, "a manual check must not move last_checked_at")
}

func TestRunCheck_ClaimedRoundIsNotMarkedAgain(t *testing.T) {
	svc, repo := newBlockingQuotaMonitorService(t, &stubMonitorUsageSource{usage: &UsageInfo{}})

	_, err := svc.RunCheck(withMonitorRound(context.Background(), monitorRoundClaimed), 1)
	require.NoError(t, err)
	require.Len(t, repo.history, 1)
	require.Empty(t, repo.markTimes, "the claim already recorded the round start")
}

func TestRunCheck_TimedOutRoundIsStillWritten(t *testing.T) {
	usage := &stubMonitorUsageSource{usage: &UsageInfo{}, block: make(chan struct{})}
	svc, repo := newBlockingQuotaMonitorService(t, usage)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(80 * time.Millisecond)
		close(usage.block)
	}()
	_, err := svc.RunCheck(ctx, 1)
	require.NoError(t, err)
	require.Len(t, repo.history, 1, "a round that ran out of budget is a result, not a cancellation")
}

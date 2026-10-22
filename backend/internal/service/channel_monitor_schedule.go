package service

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"
)

// xy2api: runner 的调度可靠性参数与辅助逻辑（错峰、启动自愈、对账、跨实例认领）。
// 放在独立文件，channel_monitor_runner.go 只保留调用点，便于与上游同步。
const (
	// monitorRunnerWorkers 同时执行检测的监控数。探测是 I/O 型，16 个足够覆盖常见规模；
	// 排队过久的检测会被跳过（见 monitorQueuedTooLong），不会返回陈旧结果。
	monitorRunnerWorkers = 16
	// monitorStartupStaggerMax 进程启动或对账补建任务时首轮的最大随机延后。
	monitorStartupStaggerMax = 60 * time.Second
	// monitorStartupRetryInitial / monitorStartupRetryMax 启动加载失败后的退避重试区间。
	monitorStartupRetryInitial = 5 * time.Second
	monitorStartupRetryMax     = 5 * time.Minute
	// monitorReconcileInterval 对账周期：比对库中启用的监控与内存任务表。
	monitorReconcileInterval = 5 * time.Minute
	// monitorClaimGapRatio 跨实例认领的最小间隔占（间隔 - 抖动）的比例。
	monitorClaimGapRatio = 0.8
)

// monitorCheckClaimer 是 runner 依赖的可选能力：认领一轮定时检测（跨实例去重）。
// *ChannelMonitorService 实现它；测试 stub 不实现时视为总能认领。
type monitorCheckClaimer interface {
	ClaimScheduledCheck(ctx context.Context, id int64, minGap time.Duration) (bool, error)
}

// monitorStartupStagger 返回 [0, min(interval, monitorStartupStaggerMax)] 内的随机延后，
// 避免进程启动时所有监控同时首发。
func monitorStartupStagger(interval time.Duration) time.Duration {
	limit := interval
	if limit > monitorStartupStaggerMax {
		limit = monitorStartupStaggerMax
	}
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(limit) + 1))
}

// monitorClaimGap 返回认领一轮检测所需的最小间隔：0.8 × (间隔 - 抖动)，
// 抖动让相邻两轮最短相隔 interval - jitter，取其 80% 留出时钟与调度误差。
func monitorClaimGap(interval, jitter time.Duration) time.Duration {
	base := interval - jitter
	if floor := monitorMinIntervalSeconds * time.Second; base < floor {
		base = floor
	}
	return time.Duration(float64(base) * monitorClaimGapRatio)
}

// monitorQueuedTooLong 判断一次检测在 worker 池里排队是否超过半个间隔；
// 超过就跳过本轮，等下一轮按时执行，而不是在很晚的时刻补测。
func monitorQueuedTooLong(enqueuedAt, now time.Time, interval time.Duration) bool {
	return interval > 0 && now.Sub(enqueuedAt) > interval/2
}

// nextMonitorRetryDelay 启动加载失败后的下一次退避时长（翻倍，封顶）。
func nextMonitorRetryDelay(current time.Duration) time.Duration {
	next := current * 2
	if next > monitorStartupRetryMax {
		return monitorStartupRetryMax
	}
	return next
}

// loadAndSchedule 加载库中启用的监控并对账内存任务表；成功返回 true。
func (r *ChannelMonitorRunner) loadAndSchedule() bool {
	snapshot := time.Now()
	ctx, cancel := context.WithTimeout(r.parentCtx, monitorStartupLoadTimeout)
	defer cancel()
	enabled, err := r.svc.ListEnabledMonitors(ctx)
	if err != nil {
		if r.parentCtx.Err() == nil {
			slog.Error("channel_monitor: load enabled monitors failed, will retry", "error", err)
		}
		return false
	}
	added, removed := r.reconcile(snapshot, enabled)
	if added > 0 || removed > 0 {
		slog.Info("channel_monitor: tasks reconciled", "added", added, "removed", removed)
	}
	return true
}

// reconcile 让任务表与库中启用的监控一致：补建缺失的任务（首轮错峰），撤销已不在库中的任务。
// 只撤销早于快照建立的任务，也不补回快照之后才撤销的监控，避免与 CRUD 回调竞态。
func (r *ChannelMonitorRunner) reconcile(snapshot time.Time, enabled []*ChannelMonitor) (added, removed int) {
	want := make(map[int64]*ChannelMonitor, len(enabled))
	for _, m := range enabled {
		if m != nil && m.Enabled && !m.APIKeyDecryptFailed {
			want[m.ID] = m
		}
	}

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return 0, 0
	}
	var toAdd []*ChannelMonitor
	for id, m := range want {
		if _, ok := r.tasks[id]; ok {
			continue
		}
		if removedAt, ok := r.removedAt[id]; ok && removedAt.After(snapshot) {
			continue
		}
		toAdd = append(toAdd, m)
	}
	var toRemove []int64
	for id, task := range r.tasks {
		if _, ok := want[id]; !ok && task.scheduledAt.Before(snapshot) {
			toRemove = append(toRemove, id)
		}
	}
	for id, at := range r.removedAt {
		if at.Before(snapshot) {
			delete(r.removedAt, id)
		}
	}
	r.mu.Unlock()

	for _, m := range toAdd {
		r.scheduleTask(m, monitorStartupStagger(time.Duration(m.IntervalSeconds)*time.Second), false)
	}
	for _, id := range toRemove {
		r.Unschedule(id)
	}
	return len(toAdd), len(toRemove)
}

// supervise 启动加载失败时按退避重试，加载成功后每 monitorReconcileInterval 对账一次。
func (r *ChannelMonitorRunner) supervise(loaded bool) {
	defer r.wg.Done()
	retry := r.retryInitial
	for {
		wait := r.reconcileEvery
		if !loaded {
			wait = retry
		}
		timer := time.NewTimer(wait)
		select {
		case <-r.parentCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if r.loadAndSchedule() {
			loaded = true
			retry = r.retryInitial
			continue
		}
		if !loaded {
			retry = nextMonitorRetryDelay(retry)
		}
	}
}

// monitorPersistTimeout 写本轮历史与 last_checked_at 的时限（不占用探测预算）。
const monitorPersistTimeout = 5 * time.Second

// monitorRoundKind 标记一轮检测的来源，决定写历史时是否推进 last_checked_at。
type monitorRoundKind int

// 零值表示未标记：未经认领的检测，写历史时照常补记 last_checked_at。
const (
	// monitorRoundClaimed 定时检测，认领时已把 last_checked_at 记为本轮开始。
	monitorRoundClaimed monitorRoundKind = iota + 1
	// monitorRoundManual 管理端手动检测，不推进 last_checked_at。
	monitorRoundManual
)

type monitorRoundKindKey struct{}

func withMonitorRound(ctx context.Context, kind monitorRoundKind) context.Context {
	return context.WithValue(ctx, monitorRoundKindKey{}, kind)
}

func monitorRoundKindOf(ctx context.Context) monitorRoundKind {
	kind, _ := ctx.Value(monitorRoundKindKey{}).(monitorRoundKind)
	return kind
}

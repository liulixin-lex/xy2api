package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 本文件覆盖：worker 池已停止（进程关停窗口）时，计费任务不得静默丢失，
// 必须降级为内联同步执行；drop/sample 仅适用于非财务任务，计费必须同步交接。

func newStoppedUsageRecordPoolForTest() *service.UsageRecordWorkerPool {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:    1,
		QueueSize:      1,
		TaskTimeout:    time.Second,
		OverflowPolicy: config.UsageRecordOverflowPolicySync,
	})
	pool.Stop()
	return pool
}

func TestGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync(t *testing.T) {
	h := &GatewayHandler{usageRecordWorkerPool: newStoppedUsageRecordPoolForTest()}

	executed := false
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		executed = true
	})
	require.True(t, executed, "池已停止时计费任务必须内联同步执行")
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync(t *testing.T) {
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: newStoppedUsageRecordPoolForTest()}

	executed := false
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		executed = true
	})
	require.True(t, executed, "池已停止时计费任务必须内联同步执行")
}

func TestGatewayHandlerSubmitUsageRecordTask_DropPolicyOverflowStillBills(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:    1,
		QueueSize:      1,
		TaskTimeout:    time.Minute,
		OverflowPolicy: config.UsageRecordOverflowPolicyDrop,
	})
	t.Cleanup(pool.Stop)
	h := &GatewayHandler{usageRecordWorkerPool: pool}

	started := make(chan struct{})
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	// 占满 worker 槽位后再填满队列，保证第三个任务触发溢出。
	require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(started)
		<-block
	}))
	<-started
	require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		<-block
	}))

	var executed atomic.Bool
	h.submitUsageRecordTask(context.Background(), func(ctx context.Context) {
		executed.Store(true)
	})
	time.Sleep(50 * time.Millisecond)
	require.True(t, executed.Load(), "队列已满时财务任务仍必须执行")
}

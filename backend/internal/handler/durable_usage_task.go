package handler

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

// Financial tasks must reach the persistent intent before the handler returns.
// An accepted in-memory queue item is not a durable handoff. Run the bounded
// accounting tail inline, detached from client cancellation; DB recovery owns
// failures after intent creation. The general worker pool remains available for
// non-financial work, but its drop/sample policies cannot discard money events.
func runDurableUsageRecordTask(parent context.Context, task service.UsageRecordTask, component string) {
	if task == nil {
		return
	}
	task, abandon := wrapUsageRecordTaskContext(parent, task)
	defer abandon()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().With(zap.String("component", component), zap.Any("panic", recovered)).Error("billing.usage_task_panic")
		}
	}()
	task(ctx)
}

package service

import (
	"context"
	"time"
)

type SchedulerOutboxEvent struct {
	ID        int64
	EventType string
	AccountID *int64
	GroupID   *int64
	Payload   map[string]any
	CreatedAt time.Time
}

// SchedulerOutboxRepository exposes pending queue health independently of its
// transactional consumer, so contending instances can still observe lag.
type SchedulerOutboxRepository interface {
	OldestPendingCreatedAt(ctx context.Context) (time.Time, bool, error)
	PendingCount(ctx context.Context) (int64, error)
	TryAcquireConsumerLock(ctx context.Context) (SchedulerOutboxConsumerLease, bool, error)
}

// SchedulerOutboxConsumerLease owns one bounded PostgreSQL transaction. Commit
// acknowledges the batch and releases its transaction-scoped lock; Release
// rolls back any unfinished batch, including a partially applied projection.
type SchedulerOutboxConsumerLease interface {
	ListPendingAndReleaseDedup(ctx context.Context, limit int) ([]SchedulerOutboxEvent, error)
	DeleteProcessed(ctx context.Context, ids []int64) error
	Commit() error
	Release()
}
